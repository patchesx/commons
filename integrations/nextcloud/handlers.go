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
		locRows = `<p class="text-sm text-gray-500 dark:text-gray-400 mb-3">No storage locations added. Add a Nextcloud folder path to set where recordings are saved.</p>`
	} else {
		locRows = `<ul class="divide-y divide-gray-100 dark:divide-gray-700 mb-3">`
		for _, loc := range locations {
			locRows += fmt.Sprintf(`
<li class="py-2 flex items-center justify-between" x-data="{ confirmDel: false }">
  <div class="min-w-0 flex-1">
    <div class="text-sm font-medium text-gray-800 dark:text-gray-100">%s</div>
    <div class="text-xs text-gray-500 dark:text-gray-400 font-mono break-all">%s</div>
  </div>
  <div class="flex items-center gap-2 ml-3">
    <span x-show="!confirmDel">
      <button x-on:click.stop="confirmDel = true" class="text-xs text-red-500 hover:text-red-700">Delete</button>
    </span>
    <span x-show="confirmDel" style="display:none" class="flex items-center gap-1">
      <button
        hx-delete="/api/storage-locations/%s"
        hx-swap="none"
        hx-on:htmx:after-request="if(event.detail.successful){ window.location.reload() }"
        class="text-xs text-red-600 font-semibold hover:text-red-800"
      >Confirm</button>
      <button x-on:click.stop="confirmDel = false" class="text-xs text-gray-500 hover:text-gray-700">Cancel</button>
    </span>
  </div>
</li>`, html.EscapeString(loc.Name), html.EscapeString(loc.Path), html.EscapeString(loc.ID))
		}
		locRows += `</ul>`
	}

	addForm := fmt.Sprintf(`
<div class="mt-3 border-t border-gray-100 dark:border-gray-700 pt-3">
  <button
    x-show="!adding"
    x-on:click="adding = true"
    class="text-xs text-accent-600 hover:text-accent-800 dark:text-accent-400"
  >+ Add Storage Location</button>
  <div x-show="adding" style="display:none" class="space-y-2">
    <h4 class="text-xs font-medium text-gray-700 dark:text-gray-300">Add Storage Location</h4>
    <div class="grid grid-cols-1 sm:grid-cols-2 gap-2">
      <div>
        <label class="block text-xs text-gray-600 dark:text-gray-400 mb-1">Name</label>
        <input
          type="text"
          x-model="name"
          placeholder="e.g., Meeting Recordings"
          class="w-full border border-gray-300 dark:border-gray-600 rounded px-2 py-1 text-sm focus:outline-none focus:ring-1 focus:ring-accent-500 dark:bg-gray-700 dark:text-white dark:placeholder-gray-400"
        />
      </div>
      <div>
        <label class="block text-xs text-gray-600 dark:text-gray-400 mb-1">Path</label>
        <input
          type="text"
          x-model="path"
          placeholder="e.g., /Recordings/DSA/"
          class="w-full border border-gray-300 dark:border-gray-600 rounded px-2 py-1 text-sm font-mono focus:outline-none focus:ring-1 focus:ring-accent-500 dark:bg-gray-700 dark:text-white dark:placeholder-gray-400"
        />
      </div>
    </div>
    <span x-show="errMsg" x-text="errMsg" role="alert" class="text-xs text-red-600 dark:text-red-400"></span>
    <div class="flex gap-2">
      <button
        :disabled="saving || !name.trim() || !path.trim()"
        x-on:click="saving = true; errMsg = '';
          fetch('/api/storage-locations', { method: 'POST', headers: {'Content-Type':'application/json'}, body: JSON.stringify({integration_id: '%s', name: name.trim(), path: path.trim()}) })
            .then(r => r.ok ? window.location.reload() : r.json().then(d => { errMsg = d.error || 'Failed to add'; saving = false; }))
            .catch(() => { errMsg = 'Failed to add'; saving = false; })"
        class="bg-accent-600 text-white text-xs rounded px-3 py-1.5 hover:bg-accent-700 disabled:opacity-50"
      >Add</button>
      <button x-on:click="adding = false; name = ''; path = ''" class="text-xs text-gray-500 dark:text-gray-400 hover:text-gray-700">Cancel</button>
    </div>
  </div>
</div>`, integID)

	return fmt.Sprintf(`
<div class="bg-white dark:bg-gray-800 rounded-lg shadow">
  <div class="px-4 py-3 border-b border-gray-100 dark:border-gray-700">
    <h2 class="text-base font-semibold text-gray-800 dark:text-white">Nextcloud — Storage Locations</h2>
  </div>
  <div class="px-4 py-3" x-data="{ adding: false, name: '', path: '', saving: false, errMsg: '' }">
    %s
    %s
  </div>
</div>`, locRows, addForm)
}
