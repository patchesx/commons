package adminui

import (
	"fmt"
	"log"
	"net/http"
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

	defaultQRPixels = 512
	minQRPixels     = 128
	maxQRPixels     = 2048
)

const adminURLNotConfigured = "The admin URL is not configured (bot/admin_url), so QR codes can't point at this site yet. Set it in the app configuration first."

// inventoryClaimURL is the address a label's QR code encodes.
func inventoryClaimURL(base, code string) string {
	return strings.TrimRight(base, "/") + "/inventory/" + code
}

// qrSVG renders content as an SVG with one unit per module, quiet zone included, on a white background.
func qrSVG(content string) (string, error) {
	q, err := qrcode.New(content, qrcode.Medium)
	if err != nil {
		return "", err
	}
	bitmap := q.Bitmap()
	n := len(bitmap)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="QR code" style="width:100%%;height:auto;display:block">`, n, n)
	b.WriteString(`<rect width="100%" height="100%" fill="#fff"/><path fill="#000" d="`)
	for y, row := range bitmap {
		for x, on := range row {
			if on {
				fmt.Fprintf(&b, "M%d %dh1v1h-1z", x, y)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String(), nil
}

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
// item code above the QR code and the item name below. ?width= sets the label width in inches.
func (d Deps) InventoryLabels() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if !d.inventoryAccess(r).Manage {
			forbidden(w)
			return
		}
		ids := r.URL.Query()["id"]
		if len(ids) == 0 {
			http.Error(w, "Select at least one item to print labels for.", http.StatusBadRequest)
			return
		}
		base, err := store.BaseURL(ctx, d.Pool, d.EncKey)
		if err != nil {
			http.Error(w, adminURLNotConfigured, http.StatusConflict)
			return
		}
		width := clampedFloat(r.URL.Query().Get("width"), defaultLabelWidthIn, minLabelWidthIn, maxLabelWidthIn)

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
			svg, err := qrSVG(inventoryClaimURL(base, item.Code))
			if err != nil {
				log.Printf("inventory: render QR for item %s: %v", item.ID, err)
				http.Error(w, "failed to generate QR code", http.StatusInternalServerError)
				return
			}
			labels = append(labels, admintempl.InventoryLabel{
				Code: store.FormatInventoryCode(item.Code),
				Name: item.Name,
				SVG:  svg,
			})
			used = append(used, item.ID)
		}
		if len(labels) == 0 {
			http.Error(w, "None of the selected items can be labeled (they may be archived or deleted).", http.StatusNotFound)
			return
		}
		admintempl.InventoryLabelsPage(labels, used, width).Render(ctx, w)
	}
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
