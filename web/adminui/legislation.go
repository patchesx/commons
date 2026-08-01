package adminui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/legislation"
	"commons/store"
	admintempl "commons/web/templ"
)

func (d Deps) LegislationPage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bodyFilter := r.URL.Query().Get("bodyId")
		data, err := loadLegislationPageData(r.Context(), d.Pool, bodyFilter)
		if err != nil {
			http.Error(w, "failed to load legislation", http.StatusInternalServerError)
			return
		}
		admintempl.LegislationPage(data).Render(r.Context(), w)
	}
}

func (d Deps) LegislationBillsTable() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		bodyFilter := r.URL.Query().Get("bodyId")
		var bills []store.Bill
		var err error
		if bodyFilter != "" {
			bills, err = store.ListBillsByBody(ctx, d.Pool, bodyFilter)
		} else {
			bills, err = store.ListBills(ctx, d.Pool)
		}
		if err != nil {
			FragmentError(w, r, "failed to load bills")
			return
		}
		admintempl.LegislationBillsTable(bills, bodyFilter).Render(ctx, w)
	}
}

func (d Deps) AddBillForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bodies, _ := store.ListLegislativeBodies(r.Context(), d.Pool)
		admintempl.AddBillFormModal(bodies, "").Render(r.Context(), w)
	}
}

func (d Deps) BillsCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		bodyID := r.FormValue("body_id")
		identifier := r.FormValue("identifier")
		title := r.FormValue("title")
		if bodyID == "" || identifier == "" || title == "" {
			bodies, _ := store.ListLegislativeBodies(ctx, d.Pool)
			admintempl.AddBillFormModal(bodies, "Body, identifier, and title are required.").Render(ctx, w)
			return
		}
		b := &store.Bill{
			BodyID:     bodyID,
			Identifier: identifier,
			Title:      title,
			Following:  true,
		}
		if v := r.FormValue("chapter_position"); v != "" {
			b.ChapterPosition = &v
		}
		if v := r.FormValue("link"); v != "" {
			b.Link = &v
		}
		if v := r.FormValue("notes"); v != "" {
			b.Notes = &v
		}
		if err := store.CreateBill(ctx, d.Pool, b); err != nil {
			bodies, _ := store.ListLegislativeBodies(ctx, d.Pool)
			admintempl.AddBillFormModal(bodies, "Failed to add bill.").Render(ctx, w)
			return
		}
		w.Header().Set("HX-Redirect", "/admin/legislation")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) BillForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		bill, err := store.GetBill(ctx, d.Pool, r.URL.Query().Get("billId"))
		if err != nil || bill == nil {
			FragmentError(w, r, "bill not found")
			return
		}
		tags, _ := store.ListTags(ctx, d.Pool)
		billTags, _ := store.ListBillTags(ctx, d.Pool, bill.ID)
		admintempl.EditBillFormModal(*bill, tags, billTags, "").Render(ctx, w)
	}
}

func (d Deps) BillsUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		bill, err := store.GetBill(ctx, d.Pool, id)
		if err != nil || bill == nil {
			FragmentError(w, r, "bill not found")
			return
		}
		bill.Identifier = r.FormValue("identifier")
		bill.Title = r.FormValue("title")
		if v := r.FormValue("chapter_position"); v != "" {
			bill.ChapterPosition = &v
		} else {
			bill.ChapterPosition = nil
		}
		if v := r.FormValue("link"); v != "" {
			bill.Link = &v
		} else {
			bill.Link = nil
		}
		if v := r.FormValue("notes"); v != "" {
			bill.Notes = &v
		} else {
			bill.Notes = nil
		}
		if err := store.UpdateBill(ctx, d.Pool, bill); err != nil {
			tags, _ := store.ListTags(ctx, d.Pool)
			billTags, _ := store.ListBillTags(ctx, d.Pool, id)
			admintempl.EditBillFormModal(*bill, tags, billTags, "Failed to update bill.").Render(ctx, w)
			return
		}
		tagIDs := r.Form["tags[]"]
		store.SetBillTags(ctx, d.Pool, id, tagIDs)
		w.Header().Set("HX-Redirect", "/admin/legislation")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) BillsDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.SetBillFollowing(r.Context(), d.Pool, r.PathValue("id"), false); err != nil {
			FragmentError(w, r, "failed to dismiss bill")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (d Deps) BodyForm() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		bodyID := r.URL.Query().Get("bodyId")
		if bodyID == "" {
			admintempl.BodyFormModal(nil, "").Render(ctx, w)
			return
		}
		bodies, err := store.ListLegislativeBodies(ctx, d.Pool)
		if err != nil {
			FragmentError(w, r, "failed to load bodies")
			return
		}
		for _, b := range bodies {
			if b.ID == bodyID {
				admintempl.BodyFormModal(&b, "").Render(ctx, w)
				return
			}
		}
		FragmentError(w, r, "body not found")
	}
}

func (d Deps) BodiesCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		name := r.FormValue("name")
		if name == "" {
			admintempl.BodyFormModal(nil, "Name is required.").Render(ctx, w)
			return
		}
		b := &store.LegislativeBody{
			Name:       name,
			Level:      r.FormValue("level"),
			State:      r.FormValue("state"),
			DataSource: r.FormValue("data_source"),
			Active:     true,
		}
		if err := store.CreateLegislativeBody(ctx, d.Pool, b); err != nil {
			admintempl.BodyFormModal(nil, "Failed to create body.").Render(ctx, w)
			return
		}
		w.Header().Set("HX-Redirect", "/admin/legislation")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) BodiesUpdate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		name := r.FormValue("name")
		if name == "" {
			bodies, _ := store.ListLegislativeBodies(ctx, d.Pool)
			for _, b := range bodies {
				if b.ID == id {
					admintempl.BodyFormModal(&b, "Name is required.").Render(ctx, w)
					return
				}
			}
			FragmentError(w, r, "body not found")
			return
		}
		b := &store.LegislativeBody{
			ID:         id,
			Name:       name,
			Level:      r.FormValue("level"),
			State:      r.FormValue("state"),
			DataSource: r.FormValue("data_source"),
			Active:     r.FormValue("active") == "true",
		}
		if err := store.UpdateLegislativeBody(ctx, d.Pool, b); err != nil {
			admintempl.BodyFormModal(b, "Failed to update body.").Render(ctx, w)
			return
		}
		w.Header().Set("HX-Redirect", "/admin/legislation")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) FiltersCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if err := r.ParseForm(); err != nil {
			FragmentError(w, r, "bad request")
			return
		}
		value := r.FormValue("value")
		if value == "" {
			FragmentError(w, r, "value required")
			return
		}
		f := &store.ImportFilter{
			BodyID:     r.PathValue("id"),
			FilterType: r.FormValue("filter_type"),
			Value:      value,
		}
		if err := store.CreateImportFilter(ctx, d.Pool, f); err != nil {
			FragmentError(w, r, "failed to create filter")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (d Deps) FiltersDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.DeleteImportFilter(r.Context(), d.Pool, r.PathValue("id")); err != nil {
			FragmentError(w, r, "failed to delete filter")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (d Deps) MatterTypes() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		body, err := store.GetLegislativeBody(ctx, d.Pool, r.PathValue("id"))
		inputClass := "w-full border border-gray-300 dark:border-gray-600 rounded px-2 py-0.5 text-xs bg-white dark:bg-gray-700 dark:text-white focus:outline-none focus:ring-1 focus:ring-accent-500"
		selectClass := inputClass
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err != nil || body == nil || body.LegistarClient == nil {
			fmt.Fprintf(w, `<input type="text" name="value" placeholder="e.g. Ordinance" required class="%s"/>`, inputClass)
			return
		}
		apiURL := fmt.Sprintf("https://webapi.legistar.com/v1/%s/mattertypes", *body.LegistarClient)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			fmt.Fprintf(w, `<input type="text" name="value" placeholder="e.g. Ordinance" required class="%s"/>`, inputClass)
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			fmt.Fprintf(w, `<input type="text" name="value" placeholder="e.g. Ordinance" required class="%s"/>`, inputClass)
			return
		}
		defer resp.Body.Close()
		var matterTypes []struct {
			MatterTypeName string `json:"MatterTypeName"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&matterTypes); err != nil || len(matterTypes) == 0 {
			fmt.Fprintf(w, `<input type="text" name="value" placeholder="e.g. Ordinance" required class="%s"/>`, inputClass)
			return
		}
		fmt.Fprintf(w, `<select name="value" required class="%s">`, selectClass)
		fmt.Fprintf(w, `<option value="" disabled selected>Select matter type</option>`)
		for _, mt := range matterTypes {
			fmt.Fprintf(w, `<option value="%s">%s</option>`, mt.MatterTypeName, mt.MatterTypeName)
		}
		fmt.Fprintf(w, `</select>`)
	}
}

func (d Deps) LegislationSync() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		go legislation.Sync(context.Background(), d.Pool, d.EncKey, d.Notifier)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (d Deps) TagsCreate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		name := r.FormValue("name")
		if name == "" {
			FragmentError(w, r, "name required")
			return
		}
		if _, err := store.CreateTag(ctx, d.Pool, name); err != nil {
			FragmentError(w, r, "failed to create tag")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func (d Deps) TagsDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := store.DeleteTag(r.Context(), d.Pool, r.PathValue("id")); err != nil {
			FragmentError(w, r, "failed to delete tag")
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func loadLegislationPageData(ctx context.Context, pool *pgxpool.Pool, bodyFilter string) (admintempl.LegislationPageData, error) {
	var bills []store.Bill
	var err error
	if bodyFilter != "" {
		bills, err = store.ListBillsByBody(ctx, pool, bodyFilter)
	} else {
		bills, err = store.ListBills(ctx, pool)
	}
	if err != nil {
		return admintempl.LegislationPageData{}, err
	}
	bodies, err := store.ListLegislativeBodies(ctx, pool)
	if err != nil {
		return admintempl.LegislationPageData{}, err
	}
	tags, err := store.ListTags(ctx, pool)
	if err != nil {
		return admintempl.LegislationPageData{}, err
	}
	filtersMap := make(map[string][]store.ImportFilter)
	subjectsMap := make(map[string][]string)
	for _, body := range bodies {
		filters, ferr := store.ListImportFilters(ctx, pool, body.ID)
		if ferr != nil {
			continue
		}
		filtersMap[body.ID] = filters
		if body.DataSource == "openstates" {
			subjects, _ := store.ListBodySubjects(ctx, pool, body.ID)
			subjectsMap[body.ID] = subjects
		}
	}
	trackedBills, _ := store.CountTrackedBills(ctx, pool)
	return admintempl.LegislationPageData{
		Bills:        bills,
		Bodies:       bodies,
		Tags:         tags,
		FiltersMap:   filtersMap,
		SubjectsMap:  subjectsMap,
		BodyFilter:   bodyFilter,
		TrackedBills: trackedBills,
	}, nil
}
