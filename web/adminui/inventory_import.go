package adminui

import (
	"encoding/base64"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"

	"commons/store"
	admintempl "commons/web/templ"
)

const (
	// The upload may be slightly bigger than the CSV it carries (multipart framing). The confirm step carries
	// the CSV again as base64, which is a third larger.
	maxImportUploadBytes  = store.MaxInventoryImportBytes + 1<<20
	maxImportConfirmBytes = store.MaxInventoryImportBytes*4/3 + 1<<20
	importPreviewRows     = 50
)

const importTemplateCSV = "Name,Code,Location,Description,Verify every (days),Holder email,Accessories\n" +
	"Projector,,Closet,Epson 1080p,7,,\"HDMI cable, Remote, Carry case\"\n"

// InventoryImportPage shows how an import works and the upload form. inventory.manage.
func (d Deps) InventoryImportPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acc := d.inventoryAccess(r)
		if !acc.Manage {
			forbidden(w)
			return
		}
		admintempl.InventoryImportPage("", acc.Admin).Render(r.Context(), w)
	}
}

// InventoryImportTemplate downloads a CSV with the column headings and one example row.
func (d Deps) InventoryImportTemplate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !d.inventoryAccess(r).Manage {
			forbidden(w)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="inventory-import-template.csv"`)
		w.Write([]byte(importTemplateCSV))
	}
}

func previewData(plan *store.InventoryImportPlan, fileName string, csvData []byte, token string) admintempl.InventoryImportPreviewData {
	rows := plan.Rows
	more := 0
	if len(rows) > importPreviewRows {
		more = len(rows) - importPreviewRows
		rows = rows[:importPreviewRows]
	}
	return admintempl.InventoryImportPreviewData{
		FileName:      fileName,
		Creates:       plan.Creates(),
		Updates:       plan.Updates(),
		HolderChanges: plan.HolderChanges(),
		NewFields:     plan.NewFields,
		Errors:        plan.Errors,
		Warnings:      plan.Warnings,
		Rows:          rows,
		MoreRows:      more,
		Payload:       base64.StdEncoding.EncodeToString(csvData),
		Token:         token,
	}
}

// InventoryImportUpload reads the uploaded CSV and shows a preview of what importing it would do. Nothing is
// saved. Holders in the file are only honored for inventory admins.
func (d Deps) InventoryImportUpload() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		acc := d.inventoryAccess(r)
		if !acc.Manage {
			forbidden(w)
			return
		}
		showError := func(msg string) {
			admintempl.InventoryImportPage(msg, acc.Admin).Render(ctx, w)
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxImportUploadBytes)
		file, header, err := r.FormFile("file")
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				showError("That file is too large. Imports can be up to 2 MB.")
				return
			}
			showError("Choose a CSV file to upload.")
			return
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, store.MaxInventoryImportBytes+1))
		if err != nil {
			showError("The file couldn't be read.")
			return
		}

		plan, err := store.PlanInventoryImport(ctx, d.Pool, data, acc.Admin)
		if err != nil {
			log.Printf("inventory: plan import: %v", err)
			showError("Something went wrong reading the file. Try again.")
			return
		}
		admintempl.InventoryImportPreview(previewData(plan, header.Filename, data, newImportToken())).Render(ctx, w)
	}
}

// InventoryImportConfirm re-reads the file the preview carried, checks it again, and applies it in one
// transaction. If anything has gone wrong since the preview it shows the preview again and saves nothing.
func (d Deps) InventoryImportConfirm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		acc := d.inventoryAccess(r)
		if !acc.Manage {
			forbidden(w)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxImportConfirmBytes)
		data, err := base64.StdEncoding.DecodeString(r.FormValue("csv_b64"))
		token := r.FormValue("token")
		if err != nil || len(data) == 0 || (d.Imports != nil && token == "") {
			admintempl.InventoryImportPage("The preview expired or was incomplete. Upload the file again.", acc.Admin).Render(ctx, w)
			return
		}

		plan, err := store.PlanInventoryImport(ctx, d.Pool, data, acc.Admin)
		if err != nil {
			log.Printf("inventory: re-plan import: %v", err)
			admintempl.InventoryImportPage("Something went wrong checking the file. Try again.", acc.Admin).Render(ctx, w)
			return
		}
		if len(plan.Errors) > 0 || len(plan.Rows) == 0 {
			admintempl.InventoryImportPreview(previewData(plan, "", data, "")).Render(ctx, w)
			return
		}

		// A preview can be confirmed once. Resubmitting it (a reload, the back button, a second tab) would import
		// its blank-code rows again as new items.
		if d.Imports != nil && !d.Imports.Use(token) {
			admintempl.InventoryImportPage("That import already ran. Check the grid before uploading the file again.", acc.Admin).Render(ctx, w)
			return
		}

		result, err := store.ApplyInventoryImport(ctx, d.Pool, plan)
		if err != nil {
			log.Printf("inventory: apply import: %v", err)
			admintempl.InventoryImportPage("The import didn't finish, so nothing was changed. "+importFailure(err), acc.Admin).Render(ctx, w)
			return
		}
		if d.Imports == nil {
			admintempl.InventoryImportResultPage(*result).Render(ctx, w)
			return
		}
		// Redirect to a read-only page so reloading shows the result instead of resubmitting the form.
		d.Imports.StoreResult(token, *result)
		http.Redirect(w, r, "/admin/inventory/import/result?t="+url.QueryEscape(token), http.StatusSeeOther)
	}
}

// InventoryImportResult shows the result of a confirmed import. It is a plain GET, so it can be reloaded.
func (d Deps) InventoryImportResult() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		acc := d.inventoryAccess(r)
		if !acc.Manage {
			forbidden(w)
			return
		}
		if d.Imports != nil {
			if result, ok := d.Imports.Result(r.URL.Query().Get("t")); ok {
				admintempl.InventoryImportResultPage(result).Render(r.Context(), w)
				return
			}
		}
		admintempl.InventoryImportPage("That result is no longer available. The imported items are in the grid.", acc.Admin).Render(r.Context(), w)
	}
}

func importFailure(err error) string {
	if errors.Is(err, store.ErrInventoryCodeTaken) {
		return "Couldn't generate unused codes. Try again."
	}
	return "Check that no one changed the same items in the meantime, then upload the file again."
}
