package slack

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// Browse Inventory is a read-only list open to every member, so people can see who has an item and get in touch:
// search, a few filters, and paging. It uses the same query as the web grid and offers no way to change anything.

const (
	ActionInventoryBrowseOpen   = "inventory_browse_open"
	ActionInventoryBrowseSearch = "inventory_browse_search" // dispatch_action input: Enter searches
	ActionInventoryBrowseFilter = "inventory_browse_filter" // static select
	ActionInventoryBrowsePrev   = "inventory_browse_prev"   // value: page:N
	ActionInventoryBrowseNext   = "inventory_browse_next"   // value: page:N

	ModalIDInventoryBrowse = "inventory_browse"
)

// inventoryBrowseUser resolves the clicking user to a member, creating them from their Slack profile if the app
// hasn't seen them. No permission is needed to browse; the web grid, which also edits and exports, still needs
// inventory.view.
func inventoryBrowseUser(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, slackID string) (*store.User, bool) {
	user, err := resolveSlackUser(ctx, pool, client, slackID)
	if err != nil {
		log.Printf("slack/inventory: browse: resolve user %s: %v", slackID, err)
		return nil, false
	}
	return user, true
}

// buildInventoryBrowseModal runs the grid query for one page of the list. A page past the end shows the last.
func buildInventoryBrowseModal(ctx context.Context, pool *pgxpool.Pool, user *store.User, search, filter string, page int) (slacklib.ModalViewRequest, error) {
	search = strings.TrimSpace(search)
	q := store.InventoryGridQuery{Search: search, Limit: inventoryBrowsePageSize}
	switch filter {
	case browseFilterAwaiting:
		q.AwaitingVerification = true
	case browseFilterPending:
		q.PendingOnly = true
	case browseFilterMine:
		q.HeldBy = user.ID
	default:
		filter = browseFilterAll
	}

	page = max(page, 1)
	q.Offset = (page - 1) * inventoryBrowsePageSize
	rows, total, err := store.ListInventoryGrid(ctx, pool, q)
	if err != nil {
		return slacklib.ModalViewRequest{}, err
	}
	pages := max(1, (total+inventoryBrowsePageSize-1)/inventoryBrowsePageSize)
	if page > pages {
		page = pages
		q.Offset = (page - 1) * inventoryBrowsePageSize
		if rows, _, err = store.ListInventoryGrid(ctx, pool, q); err != nil {
			return slacklib.ModalViewRequest{}, err
		}
	}
	return InventoryBrowseModal(inventoryBrowseData{
		Search: search, Filter: filter, Page: page, Pages: pages, Total: total, Rows: rows,
	}), nil
}

func handleOpenInventoryBrowse(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback) {
	defer w.WriteHeader(http.StatusOK)
	user, ok := inventoryBrowseUser(ctx, pool, client, payload.User.ID)
	if !ok {
		return
	}
	modal, err := buildInventoryBrowseModal(ctx, pool, user, "", browseFilterAll, 1)
	if err != nil {
		log.Printf("slack/inventory: browse: build modal: %v", err)
		return
	}
	if _, err := client.OpenViewContext(ctx, payload.TriggerID, modal); err != nil {
		log.Printf("slack/inventory: open browse modal: %s", slackErrDetail(err))
	}
}

// browseSearchAndFilter reads what the user currently has in the search box and the filter from the view.
func browseSearchAndFilter(payload *slacklib.InteractionCallback) (search, filter string) {
	filter = browseFilterAll
	if payload.View.State == nil {
		return "", filter
	}
	values := payload.View.State.Values
	search = values[inventoryBrowseSearchBlockID][ActionInventoryBrowseSearch].Value
	if v := values[inventoryBrowseFilterBlockID][ActionInventoryBrowseFilter].SelectedOption.Value; v != "" {
		filter = v
	}
	return search, filter
}

// handleInventoryBrowseUpdate re-renders the list after a search, a filter change, or a paging click. Searching
// and filtering go back to page one; the paging buttons carry their page as "page:N".
func handleInventoryBrowseUpdate(ctx context.Context, w http.ResponseWriter, pool *pgxpool.Pool, client *slacklib.Client, payload *slacklib.InteractionCallback, actionID, value string) {
	defer w.WriteHeader(http.StatusOK)
	user, ok := inventoryBrowseUser(ctx, pool, client, payload.User.ID)
	if !ok {
		return
	}
	search, filter := browseSearchAndFilter(payload)
	page := 1
	if actionID == ActionInventoryBrowsePrev || actionID == ActionInventoryBrowseNext {
		if n, err := strconv.Atoi(strings.TrimPrefix(value, "page:")); err == nil {
			page = n
		}
	}
	modal, err := buildInventoryBrowseModal(ctx, pool, user, search, filter, page)
	if err != nil {
		log.Printf("slack/inventory: browse: build modal: %v", err)
		return
	}
	if _, err := client.UpdateViewContext(ctx, modal, "", payload.View.Hash, payload.View.ID); err != nil {
		log.Printf("slack/inventory: update browse modal: %s", slackErrDetail(err))
	}
}
