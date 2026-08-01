package nextcloud

import (
	"fmt"
	"html"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// HandleStorageCard returns the Nextcloud storage locations card fragment.
// GET /admin/fragments/settings/nextcloud-storage
// Returns empty body if the integration is disabled and has no locations.
func HandleStorageCard(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		integ, err := store.GetIntegrationByType(ctx, pool, "nextcloud")
		if err != nil || integ == nil || !integ.Enabled {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		locations, _ := store.ListStorageLocationsByType(ctx, pool, "nextcloud")

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, renderStorageCard(integ.ID, locations))
	}
}

func renderStorageCard(integrationID string, locations []store.StorageLocation) string {
	integID := html.EscapeString(integrationID)

	var locRows string
	if len(locations) == 0 {
		locRows = `<p class="is-size-7 has-text-grey mb-3">No storage locations added. Add a Nextcloud folder path to set where recordings are saved.</p>`
	} else {
		locRows = `<ul class="mb-3">`
		for _, loc := range locations {
			locRows += fmt.Sprintf(`
<li class="is-flex is-justify-content-space-between is-align-items-center py-2" style="border-bottom: 1px solid var(--bulma-border)" x-data="{ confirmDel: false }">
  <div style="flex: 1; min-width: 0">
    <div class="has-text-weight-semibold is-size-7">%s</div>
    <div class="is-family-monospace is-size-7 has-text-grey" style="word-break: break-all">%s</div>
  </div>
  <div class="is-flex is-align-items-center ml-3" style="gap: 0.5rem">
    <span x-show="!confirmDel">
      <button x-on:click.stop="confirmDel = true" class="button is-danger is-outlined is-small">Delete</button>
    </span>
    <span x-show="confirmDel" style="display:none" class="is-flex is-align-items-center" style="gap: 0.25rem">
      <button
        hx-delete="/api/storage-locations/%s"
        hx-swap="none"
        hx-on:htmx:after-request="if(event.detail.successful){ window.location.reload() }"
        class="button is-danger is-small"
      >Confirm</button>
      <button x-on:click.stop="confirmDel = false" class="button is-small">Cancel</button>
    </span>
  </div>
</li>`, html.EscapeString(loc.Name), html.EscapeString(loc.Path), html.EscapeString(loc.ID))
		}
		locRows += `</ul>`
	}

	addForm := fmt.Sprintf(`
<div class="mt-3 pt-3" style="border-top: 1px solid var(--bulma-border)">
  <button
    x-show="!adding"
    x-on:click="adding = true"
    class="button is-ghost is-small has-text-link px-0"
  >+ Add Storage Location</button>
  <div x-show="adding" style="display:none">
    <h4 class="is-size-7 has-text-weight-semibold mb-2">Add Storage Location</h4>
    <div class="columns is-mobile">
      <div class="column">
        <label class="label is-small">Name</label>
        <div class="control">
          <input
            type="text"
            x-model="name"
            placeholder="e.g., Meeting Recordings"
            class="input is-small"
          />
        </div>
      </div>
      <div class="column">
        <label class="label is-small">Path</label>
        <div class="control">
          <input
            type="text"
            x-model="path"
            placeholder="e.g., /Recordings/DSA/"
            class="input is-small is-family-monospace"
          />
        </div>
      </div>
    </div>
    <span x-show="errMsg" x-text="errMsg" role="alert" class="help is-danger"></span>
    <div class="buttons are-small">
      <button
        :disabled="saving || !name.trim() || !path.trim()"
        x-on:click="saving = true; errMsg = '';
          fetch('/api/storage-locations', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({integration_id: '%s', name: name.trim(), path: path.trim()}) })
            .then(r => r.ok ? window.location.reload() : r.json().then(d => { errMsg = d.error || 'Failed to add'; saving = false; }))
            .catch(() => { errMsg = 'Failed to add'; saving = false; })"
        class="button is-primary is-small"
      >Add</button>
      <button x-on:click="adding = false; name = ''; path = ''" class="button is-small">Cancel</button>
    </div>
  </div>
</div>`, integID)

	return fmt.Sprintf(`
<div class="box">
  <div class="is-flex is-justify-content-space-between is-align-items-center mb-4">
    <h2 class="title is-6 mb-0">Nextcloud — Storage Locations</h2>
  </div>
  <div x-data="{ adding: false, name: '', path: '', saving: false, errMsg: '' }">
    %s
    %s
  </div>
</div>`, locRows, addForm)
}
