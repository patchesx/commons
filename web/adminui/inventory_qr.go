package adminui

import (
	"bytes"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	qrcode "github.com/skip2/go-qrcode"

	"commons/store"
	admintempl "commons/web/templ"
)

const (
	defaultLabelWidthIn = 2.0
	minLabelWidthIn     = 0.75
	maxLabelWidthIn     = 6.0

	defaultLabelGapIn    = 0.125
	defaultLabelMarginIn = 0.5
	minLabelMarginIn     = 0.25
	maxLabelMarginIn     = 1.0
	maxLabelGapIn        = 0.5
	minLabelHeightIn     = 0.75
	autoLabelHeightRatio = admintempl.LabelHeightRatio

	// US Letter. The printable height is a hair under 11in so a full sheet never spills a blank page.
	letterWidthIn  = 8.5
	letterPrintHIn = 10.95

	defaultQRPixels = 512
	minQRPixels     = 128
	maxQRPixels     = 2048
)

const adminURLNotConfigured = "The admin URL is not configured (bot/admin_url), so QR codes can't point at this site yet. Set it in the app configuration first."

// inventoryClaimURL is the address a label's QR code encodes.
func inventoryClaimURL(base, code string) string {
	return strings.TrimRight(base, "/") + "/inventory/" + code
}

// labelQRDPI is the resolution of a label's QR code image, which is sized for the label it goes on.
const labelQRDPI = 300

func clampedInt(s string, def, lo, hi int) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return min(max(n, lo), hi)
}

func clampedFloat(s string, def, lo, hi float64) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return def
	}
	return min(max(f, lo), hi)
}

// InventoryQRPNG serves an item's QR code as a PNG download (?size=pixels, default 512).
func (d Deps) InventoryQRPNG() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !d.inventoryAccess(r).Manage {
			forbidden(w)
			return
		}
		item, err := store.GetInventoryItem(ctx, d.Pool, r.PathValue("id"))
		if err != nil || item.ArchivedAt != nil {
			http.NotFound(w, r)
			return
		}
		base, err := store.BaseURL(ctx, d.Pool, d.EncKey)
		if err != nil {
			http.Error(w, adminURLNotConfigured, http.StatusConflict)
			return
		}
		size := clampedInt(r.URL.Query().Get("size"), defaultQRPixels, minQRPixels, maxQRPixels)
		png, err := qrcode.Encode(inventoryClaimURL(base, item.Code), qrcode.Medium, size)
		if err != nil {
			log.Printf("inventory: encode QR for item %s: %v", item.ID, err)
			http.Error(w, "failed to generate QR code", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Disposition", `attachment; filename="inventory-`+item.Code+`.png"`)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Write(png)
	}
}

// InventoryLabels renders a printable sheet of labels for the selected items (?id=...&id=...), each with the
// item code above the QR code and the item name below, on US Letter pages (see layoutLabelSheet for the settings).
func (d Deps) InventoryLabels() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sheet, ok := d.loadLabelSheet(w, r)
		if !ok {
			return
		}
		admintempl.InventoryLabelsPage(sheet).Render(r.Context(), w)
	}
}

// InventoryLabelsPDF is the same sheet as InventoryLabels, with the same settings in the query, as a US Letter
// PDF for the browser to show, download, or print. A PDF keeps its layout whatever printer is chosen.
func (d Deps) InventoryLabelsPDF() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sheet, ok := d.loadLabelSheet(w, r)
		if !ok {
			return
		}
		var buf bytes.Buffer
		if err := renderLabelsPDF(&buf, sheet); err != nil {
			log.Printf("inventory: render labels PDF: %v", err)
			http.Error(w, "failed to generate the PDF", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `inline; filename="inventory-labels.pdf"`)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Write(buf.Bytes())
	}
}

// loadLabelSheet checks permission and builds the laid-out sheet for ?id=... and the layout settings. On any
// problem it has already written the response and returns false.
func (d Deps) loadLabelSheet(w http.ResponseWriter, r *http.Request) (admintempl.InventoryLabelSheet, bool) {
	ctx := r.Context()
	if !d.inventoryAccess(r).Manage {
		forbidden(w)
		return admintempl.InventoryLabelSheet{}, false
	}
	ids := r.URL.Query()["id"]
	if len(ids) == 0 {
		http.Error(w, "Select at least one item to print labels for.", http.StatusBadRequest)
		return admintempl.InventoryLabelSheet{}, false
	}
	base, err := store.BaseURL(ctx, d.Pool, d.EncKey)
	if err != nil {
		http.Error(w, adminURLNotConfigured, http.StatusConflict)
		return admintempl.InventoryLabelSheet{}, false
	}
	// The QR image is sized for the label it will be printed on, which depends only on the settings.
	pixels := min(max(int(math.Ceil(layoutLabelSheet(r.URL.Query(), nil).QRSide()*labelQRDPI)), minQRPixels), maxQRPixels)

	seen := map[string]bool{}
	var labels []admintempl.InventoryLabel
	var used []string
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		item, err := store.GetInventoryItem(ctx, d.Pool, id)
		if err != nil || item.ArchivedAt != nil {
			continue
		}
		png, err := qrcode.Encode(inventoryClaimURL(base, item.Code), qrcode.Medium, pixels)
		if err != nil {
			log.Printf("inventory: render QR for item %s: %v", item.ID, err)
			http.Error(w, "failed to generate QR code", http.StatusInternalServerError)
			return admintempl.InventoryLabelSheet{}, false
		}
		labels = append(labels, admintempl.InventoryLabel{
			Code: store.FormatInventoryCode(item.Code),
			Name: item.Name,
			PNG:  png,
		})
		used = append(used, item.ID)
	}
	if len(labels) == 0 {
		http.Error(w, "None of the selected items can be labeled (they may be archived or deleted).", http.StatusNotFound)
		return admintempl.InventoryLabelSheet{}, false
	}
	sheet := layoutLabelSheet(r.URL.Query(), labels)
	sheet.IDs = used
	return sheet, true
}

// InventoryRotateCode gives an item a new code. Labels printed with the old code stop working.
func (d Deps) InventoryRotateCode() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !d.inventoryAccess(r).Manage {
			forbidden(w)
			return
		}
		item, err := store.GetInventoryItem(ctx, d.Pool, r.PathValue("id"))
		if err != nil || item.ArchivedAt != nil {
			FragmentError(w, r, "item not found")
			return
		}
		if _, err := store.RotateInventoryItemCodeRandom(ctx, d.Pool, item.ID); err != nil {
			log.Printf("inventory: rotate code for item %s: %v", item.ID, err)
			FragmentError(w, r, "failed to rotate code")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// layoutLabelSheet fits labels onto US Letter pages in a grid, so every label is whole and none straddles a
// page break. Settings come from the query: width, height (blank works it out from the width), gap, margin
// (inches), and start (the 1-based position on the first sheet to begin at, for a partly used sheet of stickers).
// Anything out of range is pulled back in, and a label never exceeds the area inside the margins.
func layoutLabelSheet(q url.Values, labels []admintempl.InventoryLabel) admintempl.InventoryLabelSheet {
	margin := clampedFloat(q.Get("margin"), defaultLabelMarginIn, minLabelMarginIn, maxLabelMarginIn)
	gap := clampedFloat(q.Get("gap"), defaultLabelGapIn, 0, maxLabelGapIn)
	usableW := letterWidthIn - 2*margin
	usableH := letterPrintHIn - 2*margin

	width := min(clampedFloat(q.Get("width"), defaultLabelWidthIn, minLabelWidthIn, maxLabelWidthIn), usableW)
	s := admintempl.InventoryLabelSheet{Width: width, Gap: gap, Margin: margin, Total: len(labels), HeightAuto: true}
	if h, err := strconv.ParseFloat(strings.TrimSpace(q.Get("height")), 64); err == nil {
		s.Height, s.HeightAuto = min(max(h, minLabelHeightIn), usableH), false
	} else {
		s.Height = min(width*autoLabelHeightRatio, usableH)
	}

	// The small epsilon keeps a grid that fits exactly (say 4 x 2in plus gaps) from losing a column to rounding.
	s.Cols = max(int((usableW+gap)/(width+gap)+1e-9), 1)
	s.Rows = max(int((usableH+gap)/(s.Height+gap)+1e-9), 1)

	if n, err := strconv.Atoi(strings.TrimSpace(q.Get("start"))); err == nil {
		s.Skip = min(max(n-1, 0), s.PerPage()-1)
	}

	cells := make([]*admintempl.InventoryLabel, 0, s.Skip+len(labels))
	for range s.Skip {
		cells = append(cells, nil)
	}
	for i := range labels {
		cells = append(cells, &labels[i])
	}
	for len(cells) > 0 {
		n := min(s.PerPage(), len(cells))
		s.Pages = append(s.Pages, cells[:n])
		cells = cells[n:]
	}
	return s
}
