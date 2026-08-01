package api

import (
	"net/http"

	"commons/internal/httpx"
	"commons/plugin"
)

type pluginResponse struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Version  string   `json:"version"`
	Provides []string `json:"provides"`
	Enabled  bool     `json:"enabled"`
}

// ListPlugins returns the registered plugins and their metadata.
func ListPlugins() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		plugins := plugin.All()
		resp := make([]pluginResponse, 0, len(plugins))
		for _, p := range plugins {
			resp = append(resp, pluginResponse{
				Name:     p.Name(),
				Label:    p.Label(),
				Version:  p.Version(),
				Provides: p.Provides(),
				Enabled:  true,
			})
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	}
}
