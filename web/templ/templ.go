package admintempl

//go:generate go tool templ generate

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"net/http"
)

//go:embed assets/*
var assets embed.FS

// assetVersion is a short hash of all embedded asset content, computed once at startup.
// It is appended as ?v=<hash> to asset URLs so browsers fetch updated files automatically.
var assetVersion string

func init() {
	h := sha256.New()
	fs.WalkDir(assets, "assets", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, err := assets.ReadFile(path)
		if err != nil {
			return nil
		}
		h.Write(data)
		return nil
	})
	assetVersion = fmt.Sprintf("%x", h.Sum(nil))[:8]
}

// AssetURL returns the versioned URL for a named asset under /admin/assets/.
func AssetURL(name string) string {
	return "/admin/assets/" + name + "?v=" + assetVersion
}

// Mount registers the admin static asset handler on mux.
// Assets are served at /admin/assets/ with long-lived cache headers.
// Cache busting is handled via versioned URLs (see AssetURL).
func Mount(mux *http.ServeMux) {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic("admintempl: failed to sub assets FS: " + err.Error())
	}
	hdlr := http.StripPrefix("/admin/assets/", http.FileServer(http.FS(sub)))
	hdlr = unchangingCache(hdlr)
	mux.Handle("GET /admin/assets/", hdlr)
}

func unchangingCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		h.ServeHTTP(w, r)
	})
}
