package adminui

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/go-pdf/fpdf"

	admintempl "commons/web/templ"
)

// Label proportions, as fractions of the sheet's Scale. They match the on-screen sheet (invLabelPadCSS,
// invCodeCSS, invNameCSS in the templates), so the PDF looks like the preview.
const (
	labelPadRatio       = 0.06
	labelCodeSizeRatio  = 0.15
	labelCodeGapRatio   = 0.04
	labelNameSizeRatio  = 0.075
	labelNameGapRatio   = 0.04
	labelNameLineHeight = 1.15
	labelNameMaxLines   = 2
	pointsPerInch       = 72.0
)

// renderLabelsPDF draws the laid-out sheet on US Letter pages, one PDF page per sheet. Positions come from the
// same settings as the HTML sheet; skipped positions are left blank and there are no outlines. The standard PDF
// fonts only cover Western European characters, so other characters in an item name are replaced.
func renderLabelsPDF(w io.Writer, s admintempl.InventoryLabelSheet) error {
	pdf := fpdf.New("P", "in", "Letter", "")
	pdf.SetTitle("Inventory labels", true)
	pdf.SetMargins(0, 0, 0)
	pdf.SetCellMargin(0)
	pdf.SetAutoPageBreak(false, 0)
	utf8 := pdf.UnicodeTranslatorFromDescriptor("")

	k := s.Scale()
	pad := k * labelPadRatio
	codeSize := k * labelCodeSizeRatio
	nameSize := k * labelNameSizeRatio
	nameLine := nameSize * labelNameLineHeight
	qr := s.QRSide()
	gridW := float64(s.Cols)*s.Width + float64(s.Cols-1)*s.Gap
	left := max((letterWidthIn-gridW)/2, 0)

	image := 0
	for _, page := range s.Pages {
		pdf.AddPage()
		for i, l := range page {
			if l == nil {
				continue
			}
			x := left + float64(i%s.Cols)*(s.Width+s.Gap)
			y := s.Margin + float64(i/s.Cols)*(s.Height+s.Gap)

			pdf.SetFont("Courier", "B", codeSize*pointsPerInch)
			pdf.SetXY(x, y+pad)
			pdf.CellFormat(s.Width, codeSize, l.Code, "", 0, "C", false, 0, "")

			qrTop := y + pad + codeSize + k*labelCodeGapRatio
			image++
			name := fmt.Sprintf("qr%d", image)
			pdf.RegisterImageOptionsReader(name, fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(l.PNG))
			pdf.ImageOptions(name, x+(s.Width-qr)/2, qrTop, qr, qr, false, fpdf.ImageOptions{ImageType: "PNG"}, 0, "")

			pdf.SetFont("Helvetica", "", nameSize*pointsPerInch)
			lines := wrapPDFText(pdf, utf8(l.Name), s.Width-2*pad, labelNameMaxLines)
			nameTop := qrTop + qr + k*labelNameGapRatio
			for n, line := range lines {
				pdf.SetXY(x+pad, nameTop+float64(n)*nameLine)
				pdf.CellFormat(s.Width-2*pad, nameLine, line, "", 0, "C", false, 0, "")
			}
		}
	}
	return pdf.Output(w)
}

// wrapPDFText breaks text (already converted to the PDF font's single-byte encoding) into at most maxLines lines
// no wider than width, at spaces where it can and mid-word where a single word is too long. Whatever doesn't
// fit in maxLines is dropped, as the on-screen label does. fpdf's own SplitText can't be used: it panics on
// the accented characters the font encoding produces.
func wrapPDFText(pdf *fpdf.Fpdf, text string, width float64, maxLines int) []string {
	var lines []string
	cur := ""
	push := func(s string) { lines = append(lines, s) }
	for _, word := range strings.Fields(text) {
		if len(lines) >= maxLines {
			break
		}
		if cand := strings.TrimSpace(cur + " " + word); cur != "" && pdf.GetStringWidth(cand) <= width {
			cur = cand
			continue
		}
		if cur != "" {
			push(cur)
			cur = ""
		}
		for len(lines) < maxLines && pdf.GetStringWidth(word) > width && len(word) > 1 {
			n := len(word)
			for n > 1 && pdf.GetStringWidth(word[:n]) > width {
				n--
			}
			push(word[:n])
			word = word[n:]
		}
		cur = word
	}
	if cur != "" && len(lines) < maxLines {
		push(cur)
	}
	return lines
}
