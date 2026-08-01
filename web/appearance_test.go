package web

import (
	"context"
	"strings"
	"testing"
)

func TestHexToAccentCSS_ValidRed(t *testing.T) {
	css, err := HexToAccentCSS("#EC1C24")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(css, "--brand-accent-500") {
		t.Error("expected CSS to contain --brand-accent-500")
	}
	if !strings.Contains(css, "@media (prefers-color-scheme: dark)") {
		t.Error("expected CSS to contain @media dark mode block")
	}
	if !strings.Contains(css, ".dark {") {
		t.Error("expected CSS to contain .dark { } class block")
	}
	// All 9 accent tokens must be present
	for _, token := range []string{"50", "100", "200", "300", "400", "500", "600", "700", "800"} {
		if !strings.Contains(css, "--brand-accent-"+token) {
			t.Errorf("expected CSS to contain --brand-accent-%s", token)
		}
	}
}

func TestHexToAccentCSS_InvalidInput(t *testing.T) {
	css, err := HexToAccentCSS("not-a-color")
	if err == nil {
		t.Error("expected error for invalid hex")
	}
	if css != "" {
		t.Error("expected empty string on error")
	}
}

func TestHexToAccentCSS_WithoutHash(t *testing.T) {
	// Should work with or without leading #
	_, err := HexToAccentCSS("EC1C24")
	if err != nil {
		t.Fatalf("unexpected error for hex without #: %v", err)
	}
}

func TestAppearanceFromContext_Empty(t *testing.T) {
	ctx := context.Background()
	cfg := AppearanceFromContext(ctx)
	// Zero value is fine — empty strings mean "use defaults"
	if cfg.OrgName != "" || cfg.AccentCSS != "" || cfg.FaviconURL != "" || cfg.SidebarCSS != "" || cfg.PageBgCSS != "" {
		t.Error("expected zero-value AppearanceConfig from empty context")
	}
}

func TestAppearanceContextRoundTrip(t *testing.T) {
	ctx := context.Background()
	want := AppearanceConfig{OrgName: "Test Org", AccentCSS: ":root{}", FaviconURL: "https://example.com/fav.ico"}
	ctx = contextWithAppearance(ctx, want)
	got := AppearanceFromContext(ctx)
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestHexToSidebarCSS_DarkBackground(t *testing.T) {
	// #111827 is near-black — hover/border should be lighter
	css, err := HexToSidebarCSS("#111827", "#FFFFFF")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, v := range []string{
		"--brand-sidebar-bg",
		"--brand-sidebar-text",
		"--brand-sidebar-muted",
		"--brand-sidebar-hover",
		"--brand-sidebar-border",
		"#sidebar-nav",
		".sidebar-mobile-bar",
		".sidebar-nav-active",
		".sidebar-group-label",
	} {
		if !strings.Contains(css, v) {
			t.Errorf("expected CSS to contain %q", v)
		}
	}
}

func TestHexToSidebarCSS_LightBackground(t *testing.T) {
	// #FFFFFF is white — hover/border should be darker
	css, err := HexToSidebarCSS("#FFFFFF", "#1F2937")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(css, "--brand-sidebar-bg") {
		t.Error("expected CSS to contain --brand-sidebar-bg")
	}
}

func TestHexToSidebarCSS_InvalidBg(t *testing.T) {
	_, err := HexToSidebarCSS("not-a-color", "#FFFFFF")
	if err == nil {
		t.Error("expected error for invalid bg hex")
	}
}

func TestHexToSidebarCSS_InvalidText(t *testing.T) {
	_, err := HexToSidebarCSS("#111827", "not-a-color")
	if err == nil {
		t.Error("expected error for invalid text hex")
	}
}

func TestHexToSidebarCSS_WithoutHash(t *testing.T) {
	_, err := HexToSidebarCSS("111827", "FFFFFF")
	if err != nil {
		t.Fatalf("unexpected error for hex without #: %v", err)
	}
}

func TestHexToPageBgCSS_Valid(t *testing.T) {
	css, err := HexToPageBgCSS("#F3F4F6", "#1A1718")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(css, "--brand-page-bg") {
		t.Error("expected CSS to contain --brand-page-bg")
	}
	if !strings.Contains(css, "#F3F4F6") {
		t.Error("expected CSS to contain light hex value")
	}
	if !strings.Contains(css, "#1A1718") {
		t.Error("expected CSS to contain dark hex value")
	}
	if !strings.Contains(css, ".dark {") {
		t.Error("expected CSS to contain .dark { } block")
	}
}

func TestHexToPageBgCSS_InvalidLight(t *testing.T) {
	_, err := HexToPageBgCSS("not-a-color", "#1A1718")
	if err == nil {
		t.Error("expected error for invalid light hex")
	}
}

func TestHexToPageBgCSS_InvalidDark(t *testing.T) {
	_, err := HexToPageBgCSS("#F3F4F6", "not-a-color")
	if err == nil {
		t.Error("expected error for invalid dark hex")
	}
}

func TestHexToPageBgCSS_WithoutHash(t *testing.T) {
	_, err := HexToPageBgCSS("F3F4F6", "1A1718")
	if err != nil {
		t.Fatalf("unexpected error for hex without #: %v", err)
	}
}
