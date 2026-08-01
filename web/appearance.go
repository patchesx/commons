package web

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"commons/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AppearanceConfig holds the live UI branding values read from config_store service "ui".
// Empty string for any field means "use compiled-in default."
type AppearanceConfig struct {
	OrgName    string
	AccentCSS  string // pre-built inline <style> block, or empty
	FaviconURL string // URL or data URI, or empty
	SidebarCSS string // pre-built CSS block for sidebar vars and rules, or empty
	PageBgCSS  string // pre-built CSS block for page background vars, or empty
}

// AppearanceFromContext returns the AppearanceConfig stored by AppearanceMiddleware.
// Returns a zero-value struct (all empty strings) if not present — callers treat empty as "use default."
func AppearanceFromContext(ctx context.Context) AppearanceConfig {
	v, _ := ctx.Value(appearanceKey).(AppearanceConfig)
	return v
}

func contextWithAppearance(ctx context.Context, cfg AppearanceConfig) context.Context {
	return context.WithValue(ctx, appearanceKey, cfg)
}

// AppearanceMiddleware reads ui.* from config_store on each request and stores the
// result in the request context. Must run after auth (needs an authenticated request).
// Errors reading config are silently ignored — callers fall back to compiled-in defaults.
func AppearanceMiddleware(pool *pgxpool.Pool, encKey []byte, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := loadAppearanceConfig(r.Context(), pool, encKey)
		r = r.WithContext(contextWithAppearance(r.Context(), cfg))
		next.ServeHTTP(w, r)
	})
}

func loadAppearanceConfig(ctx context.Context, pool *pgxpool.Pool, encKey []byte) AppearanceConfig {
	get := func(key string) string {
		v, err := store.GetServiceConfig(ctx, pool, "ui", key, encKey)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return ""
		}
		return v
	}

	orgName := get("org_name")
	accentHex := get("accent_hex")
	faviconURL := get("favicon_url")
	sidebarHex := get("sidebar_hex")
	sidebarTextHex := get("sidebar_text_hex")
	bgHex := get("bg_hex")
	bgDarkHex := get("bg_dark_hex")

	var accentCSS string
	if accentHex != "" {
		if css, err := HexToAccentCSS(accentHex); err == nil {
			accentCSS = css
		}
	}

	var sidebarCSS string
	if sidebarHex != "" && sidebarTextHex != "" {
		if css, err := HexToSidebarCSS(sidebarHex, sidebarTextHex); err == nil {
			sidebarCSS = css
		}
	}

	// Always apply defaults before calling HexToPageBgCSS
	if bgHex == "" {
		bgHex = "#F3F4F6"
	}
	if bgDarkHex == "" {
		bgDarkHex = "#1A1718"
	}
	var pageBgCSS string
	if css, err := HexToPageBgCSS(bgHex, bgDarkHex); err == nil {
		pageBgCSS = css
	}

	return AppearanceConfig{
		OrgName:    orgName,
		AccentCSS:  accentCSS,
		FaviconURL: faviconURL,
		SidebarCSS: sidebarCSS,
		PageBgCSS:  pageBgCSS,
	}
}

// HexToAccentCSS converts a CSS hex color (with or without leading #) into an inline
// <style> block overriding the --brand-accent-* custom properties used by app.css.
// Returns an error if the input is not a valid 6-digit hex color.
//
// NOTE: broken since the Tailwind→Bulma conversion. Outputs --brand-accent-* variables
// that no longer exist in app.css. Needs to be rewritten to target Bulma's HSL primitive
// variables (--bulma-primary-h, --bulma-primary-s, --bulma-primary-l) before appearance
// customization is re-enabled.
func HexToAccentCSS(hex string) (string, error) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		return "", errors.New("hex color must be 6 digits")
	}
	r, err1 := strconv.ParseUint(hex[0:2], 16, 8)
	g, err2 := strconv.ParseUint(hex[2:4], 16, 8)
	b, err3 := strconv.ParseUint(hex[4:6], 16, 8)
	if err1 != nil || err2 != nil || err3 != nil {
		return "", errors.New("invalid hex color digits")
	}

	h, s, l := rgbToHSL(float64(r)/255, float64(g)/255, float64(b)/255)

	// Lightness deltas from base (accent-500) for each token.
	deltas := map[string]float64{
		"50": 45, "100": 38, "200": 28, "300": 18, "400": 9,
		"500": 0,
		"600": -10, "700": -20, "800": -30,
	}
	// Dark-mode lightness additions for 500, 600, 700.
	darkShift := 12.0

	token := func(name string) string {
		d := deltas[name]
		lAdj := clamp(l+d, 5, 95)
		return fmt.Sprintf("  --brand-accent-%s: hsl(%.1f, %.1f%%, %.1f%%);\n", name, h, s*100, lAdj)
	}
	darkToken := func(name string) string {
		d := deltas[name]
		lAdj := clamp(l+d+darkShift, 5, 95)
		return fmt.Sprintf("    --brand-accent-%s: hsl(%.1f, %.1f%%, %.1f%%);\n", name, h, s*100, lAdj)
	}

	var sb strings.Builder
	sb.WriteString(":root {\n")
	for _, name := range []string{"50", "100", "200", "300", "400", "500", "600", "700", "800"} {
		sb.WriteString(token(name))
	}
	sb.WriteString("}\n")
	// Media query block — fires when OS is in dark mode
	sb.WriteString("@media (prefers-color-scheme: dark) {\n  :root {\n")
	for _, name := range []string{"500", "600", "700"} {
		sb.WriteString(darkToken(name))
	}
	sb.WriteString("  }\n}\n")
	// Class block — fires when .dark ancestor is present (used by preview panel)
	sb.WriteString(".dark {\n")
	for _, name := range []string{"500", "600", "700"} {
		d := deltas[name]
		lAdj := clamp(l+d+darkShift, 5, 95)
		sb.WriteString(fmt.Sprintf("  --brand-accent-%s: hsl(%.1f, %.1f%%, %.1f%%);\n", name, h, s*100, lAdj))
	}
	sb.WriteString("}\n")

	return sb.String(), nil
}

// HexToSidebarCSS generates a self-contained CSS block for sidebar theming.
// bgHex is the sidebar background color; textHex is the sidebar text color.
// Both may include or omit the leading #.
// The returned block contains :root variable declarations and #sidebar-nav
// targeting rules (including .sidebar-mobile-bar for the mobile top bar).
func HexToSidebarCSS(bgHex, textHex string) (string, error) {
	bgHex = strings.TrimPrefix(bgHex, "#")
	if len(bgHex) != 6 {
		return "", errors.New("sidebar bg hex color must be 6 digits")
	}
	textHex = strings.TrimPrefix(textHex, "#")
	if len(textHex) != 6 {
		return "", errors.New("sidebar text hex color must be 6 digits")
	}

	parseHex := func(h string) (float64, float64, float64, error) {
		r, err1 := strconv.ParseUint(h[0:2], 16, 8)
		g, err2 := strconv.ParseUint(h[2:4], 16, 8)
		b, err3 := strconv.ParseUint(h[4:6], 16, 8)
		if err1 != nil || err2 != nil || err3 != nil {
			return 0, 0, 0, errors.New("invalid hex color digits")
		}
		return float64(r), float64(g), float64(b), nil
	}

	bgR, bgG, bgB, err := parseHex(bgHex)
	if err != nil {
		return "", fmt.Errorf("sidebar bg: %w", err)
	}
	textR, textG, textB, err := parseHex(textHex)
	if err != nil {
		return "", fmt.Errorf("sidebar text: %w", err)
	}

	bgH, bgS, bgL := rgbToHSL(bgR/255, bgG/255, bgB/255)

	// Hover: ±8% lightness; border: ±15%. Lighten dark backgrounds, darken light ones.
	sign := 1.0
	if bgL >= 50 {
		sign = -1.0
	}

	hoverL := clamp(bgL+sign*8, 5, 95)
	activeL := clamp(bgL+sign*16, 5, 95)
	borderL := clamp(bgL+sign*15, 5, 95)

	bgCSS := fmt.Sprintf("#%s", bgHex)
	textCSS := fmt.Sprintf("#%s", textHex)
	mutedCSS := fmt.Sprintf("rgba(%d, %d, %d, 0.6)", int(textR), int(textG), int(textB))
	hoverCSS := fmt.Sprintf("hsl(%.1f, %.1f%%, %.1f%%)", bgH, bgS*100, hoverL)
	activeCSS := fmt.Sprintf("hsl(%.1f, %.1f%%, %.1f%%)", bgH, bgS*100, activeL)
	borderCSS := fmt.Sprintf("hsl(%.1f, %.1f%%, %.1f%%)", bgH, bgS*100, borderL)

	var sb strings.Builder
	sb.WriteString(":root {\n")
	sb.WriteString(fmt.Sprintf("  --brand-sidebar-bg: %s;\n", bgCSS))
	sb.WriteString(fmt.Sprintf("  --brand-sidebar-text: %s;\n", textCSS))
	sb.WriteString(fmt.Sprintf("  --brand-sidebar-muted: %s;\n", mutedCSS))
	sb.WriteString(fmt.Sprintf("  --brand-sidebar-hover: %s;\n", hoverCSS))
	sb.WriteString(fmt.Sprintf("  --brand-sidebar-active: %s;\n", activeCSS))
	sb.WriteString(fmt.Sprintf("  --brand-sidebar-border: %s;\n", borderCSS))
	sb.WriteString("}\n")
	sb.WriteString(`
#sidebar-nav,
.sidebar-mobile-bar {
  background-color: var(--brand-sidebar-bg) !important;
  color: var(--brand-sidebar-text) !important;
}
#sidebar-nav > div {
  border-color: var(--brand-sidebar-border) !important;
}
#sidebar-nav a,
#sidebar-nav button,
.sidebar-mobile-bar a,
.sidebar-mobile-bar button {
  color: var(--brand-sidebar-text) !important;
}
#sidebar-nav a:hover,
#sidebar-nav button:hover {
  background-color: var(--brand-sidebar-hover) !important;
  color: var(--brand-sidebar-text) !important;
}
#sidebar-nav .sidebar-nav-active {
  background-color: var(--brand-sidebar-active) !important;
}
#sidebar-nav .sidebar-group-label {
  color: var(--brand-sidebar-muted) !important;
}
`)
	return sb.String(), nil
}

// HexToPageBgCSS generates a :root block for the light-mode page background and a
// .dark block for the dark-mode page background. Both inputs may include or omit
// the leading #. Returns an error if either is not a valid 6-digit hex color.
// Callers must apply defaults before calling — never pass empty strings.
//
// NOTE: broken since the Tailwind→Bulma conversion. Outputs `.dark { --brand-page-bg }`
// but layouts now set data-theme="dark" (not a .dark class), and --brand-page-bg no longer
// exists in app.css. Needs to be rewritten to target [data-theme="dark"] and the
// appropriate Bulma body background variable before appearance customization is re-enabled.
func HexToPageBgCSS(lightHex, darkHex string) (string, error) {
	lightHex = strings.TrimPrefix(lightHex, "#")
	if len(lightHex) != 6 {
		return "", errors.New("light bg hex color must be 6 digits")
	}
	for i := 0; i < 3; i++ {
		if _, err := strconv.ParseUint(lightHex[i*2:i*2+2], 16, 8); err != nil {
			return "", errors.New("invalid light bg hex color digits")
		}
	}

	darkHex = strings.TrimPrefix(darkHex, "#")
	if len(darkHex) != 6 {
		return "", errors.New("dark bg hex color must be 6 digits")
	}
	for i := 0; i < 3; i++ {
		if _, err := strconv.ParseUint(darkHex[i*2:i*2+2], 16, 8); err != nil {
			return "", errors.New("invalid dark bg hex color digits")
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(":root {\n  --brand-page-bg: #%s;\n}\n", lightHex))
	sb.WriteString(fmt.Sprintf(".dark {\n  --brand-page-bg: #%s;\n}\n", darkHex))
	return sb.String(), nil
}

// rgbToHSL converts r, g, b in [0,1] to H [0,360), S [0,1], L [0,100].
func rgbToHSL(r, g, b float64) (h, s, l float64) {
	max := math.Max(r, math.Max(g, b))
	min := math.Min(r, math.Min(g, b))
	l = (max + min) / 2 * 100

	if max == min {
		return 0, 0, l
	}

	d := max - min
	if l > 50 {
		s = d / (2 - max - min)
	} else {
		s = d / (max + min)
	}

	switch max {
	case r:
		h = (g - b) / d
		if g < b {
			h += 6
		}
	case g:
		h = (b-r)/d + 2
	case b:
		h = (r-g)/d + 4
	}
	h *= 60
	return h, s, l
}

func clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// faviconMIMEAllowlist are the only MIME types accepted for uploaded favicon data URIs.
var faviconMIMEAllowlist = map[string]bool{
	"image/x-icon":  true,
	"image/png":     true,
	"image/svg+xml": true,
	"image/webp":    true,
	"image/gif":     true,
}

// ServeFavicon handles GET /favicon.ico.
// If favicon_url is a data URI: validates MIME + serves decoded bytes.
// If it is an http/https URL: issues a 302 redirect.
// If unset or invalid: returns 204.
func ServeFavicon(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		faviconURL, err := store.GetServiceConfig(r.Context(), pool, "ui", "favicon_url", encKey)
		if err != nil || faviconURL == "" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if strings.HasPrefix(faviconURL, "data:") {
			// data:<mime>;base64,<data>
			rest := strings.TrimPrefix(faviconURL, "data:")
			parts := strings.SplitN(rest, ";base64,", 2)
			if len(parts) != 2 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			mime := parts[0]
			if !faviconMIMEAllowlist[mime] {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			data, err := base64.StdEncoding.DecodeString(parts[1])
			if err != nil {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if len(data) > 256*1024 {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("Content-Type", mime)
			w.Write(data)
			return
		}

		if strings.HasPrefix(faviconURL, "http://") || strings.HasPrefix(faviconURL, "https://") {
			http.Redirect(w, r, faviconURL, http.StatusFound)
			return
		}

		w.WriteHeader(http.StatusNoContent)
	}
}
