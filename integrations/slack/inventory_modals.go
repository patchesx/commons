package slack

import (
	"fmt"
	"strconv"
	"strings"

	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// inventoryHeldPageSize is how many held items the Inventory modal shows at a time. Each takes two blocks and
// Slack caps a view at 100, so a big hand-over pages instead of overflowing.
const inventoryHeldPageSize = 10

// inventoryHeldPagePrefix starts the block ID of the pager, which ends in the page shown (1-based).
const inventoryHeldPagePrefix = "inventory_held_page_"

const (
	inventoryCodeBlockID       = "inventory_code_block"
	inventoryHandOffBlockID    = "inventory_hand_off_block"
	inventoryHandOffUserAction = "inventory_hand_off_user"
)

// inventoryModalData is everything the Inventory modal renders.
type inventoryModalData struct {
	Code          string                // what the user typed in the code box
	Found         *store.InventoryItem  // the item the code matched, if any
	FoundHolder   string                // display name of that item's holder, if someone else holds it
	AlreadyHolder bool                  // the viewing user already holds Found
	Notice        string                // mrkdwn status line shown above the code box
	Held          []store.InventoryItem // items the viewing user holds
	Page          int                   // which page of Held to show, 1-based; out of range shows the nearest
}

func invPlain(s string) *slacklib.TextBlockObject {
	return slacklib.NewTextBlockObject(slacklib.PlainTextType, s, true, false)
}

func invMD(s string) *slacklib.TextBlockObject {
	return slacklib.NewTextBlockObject(slacklib.MarkdownType, s, false, false)
}

// escapeMrkdwn escapes the characters Slack treats as control characters in mrkdwn text.
func escapeMrkdwn(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

func inventoryItemSummary(it store.InventoryItem) string {
	text := fmt.Sprintf("*%s*  `%s`", escapeMrkdwn(it.Name), store.FormatInventoryCode(it.Code))
	if it.Location != nil && *it.Location != "" {
		text += "\n_" + escapeMrkdwn(*it.Location) + "_"
	}
	return text
}

// InventoryModal builds the member-facing Inventory modal: a code box that looks an item up on Enter, the
// matched item with a "Confirm possession" button, and the items the user holds with Verify and Hand off.
func InventoryModal(data inventoryModalData) slacklib.ModalViewRequest {
	var blks []slacklib.Block

	if data.Notice != "" {
		blks = append(blks, slacklib.NewSectionBlock(invMD(data.Notice), nil, nil))
	}

	codeElem := slacklib.NewPlainTextInputBlockElement(invPlain("Enter an item code, for example K7M-4QX"), ActionInventoryCodeInput)
	codeElem.InitialValue = data.Code
	codeBlock := slacklib.NewInputBlock(inventoryCodeBlockID, invPlain("Item code"), invPlain("Press Enter to look the item up."), codeElem)
	codeBlock.Optional = true
	codeBlock.DispatchAction = true
	blks = append(blks, codeBlock)

	privateMetadata := ""
	if data.Found != nil {
		privateMetadata = data.Found.ID
		text := inventoryItemSummary(*data.Found)
		switch {
		case data.AlreadyHolder:
			text += "\n:information_source: You already have this item."
			blks = append(blks, slacklib.NewSectionBlock(invMD(text), nil, nil))
		case data.FoundHolder != "":
			text += fmt.Sprintf("\nCurrently held by *%s*", escapeMrkdwn(data.FoundHolder))
			blks = append(blks, inventoryClaimSection(text, data.Found.ID))
		default:
			text += "\nNot currently held"
			blks = append(blks, inventoryClaimSection(text, data.Found.ID))
		}
	}

	blks = append(blks, slacklib.NewDividerBlock())
	held, page, pages := inventoryHeldPage(data.Held, data.Page)
	heading := "*Items I hold*"
	if pages > 1 {
		first := (page-1)*inventoryHeldPageSize + 1
		heading += fmt.Sprintf("  _%d–%d of %d_", first, first+len(held)-1, len(data.Held))
	}
	blks = append(blks, slacklib.NewSectionBlock(invMD(heading), nil, nil))
	if len(data.Held) == 0 {
		blks = append(blks, slacklib.NewSectionBlock(invMD("_You aren't holding any items._"), nil, nil))
	}
	for _, it := range held {
		verify := slacklib.NewButtonBlockElement(ActionInventoryVerify, it.ID, invPlain("Verify"))
		handOff := slacklib.NewButtonBlockElement(ActionInventoryHandOff, it.ID, invPlain("Hand off"))
		blks = append(blks,
			slacklib.NewSectionBlock(invMD(inventoryItemSummary(it)), nil, nil),
			slacklib.NewActionBlock("inventory_held_"+it.ID, verify, handOff),
		)
	}
	if pages > 1 {
		// The page is in the block ID so the modal can be re-rendered on the same page after a verify or claim.
		blks = append(blks, slacklib.NewDividerBlock())
		var buttons []slacklib.BlockElement
		if page > 1 {
			buttons = append(buttons, slacklib.NewButtonBlockElement(ActionInventoryHeldPrev, fmt.Sprintf("page:%d", page-1), invPlain("← Previous")))
		}
		if page < pages {
			buttons = append(buttons, slacklib.NewButtonBlockElement(ActionInventoryHeldNext, fmt.Sprintf("page:%d", page+1), invPlain("Next →")))
		}
		blks = append(blks, slacklib.NewActionBlock(inventoryHeldPagePrefix+strconv.Itoa(page), buttons...))
	}

	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           invPlain("Inventory"),
		Close:           invPlain("Close"),
		CallbackID:      ModalIDInventory,
		PrivateMetadata: privateMetadata,
		Blocks:          slacklib.Blocks{BlockSet: blks},
	}
}

func inventoryClaimSection(text, itemID string) *slacklib.SectionBlock {
	btn := slacklib.NewButtonBlockElement(ActionInventoryClaim, itemID, invPlain("Confirm possession"))
	btn.Style = slacklib.StylePrimary
	return slacklib.NewSectionBlock(invMD(text), nil, slacklib.NewAccessory(btn))
}

const (
	inventoryBrowsePageSize = 10

	inventoryBrowseSearchBlockID = "inventory_browse_search_block"
	inventoryBrowseFilterBlockID = "inventory_browse_filter_block"

	browseFilterAll      = "all"
	browseFilterAwaiting = "awaiting"
	browseFilterPending  = "pending"
	browseFilterMine     = "mine"
)

var inventoryBrowseFilters = []struct{ value, label string }{
	{browseFilterAll, "All items"},
	{browseFilterAwaiting, "Awaiting verification"},
	{browseFilterPending, "Claim pending"},
	{browseFilterMine, "Held by me"},
}

func inventoryBrowseFilterLabel(filter string) string {
	for _, f := range inventoryBrowseFilters {
		if f.value == filter {
			return f.label
		}
	}
	return inventoryBrowseFilters[0].label
}

// inventoryBrowseData is everything the read-only Browse Inventory modal renders.
type inventoryBrowseData struct {
	Search string
	Filter string
	Page   int
	Pages  int
	Total  int
	Rows   []store.InventoryGridRow
}

// InventoryBrowseModal builds the read-only inventory list: a search box, a filter, a page of items with their
// holder, last verified date, and claim pending marker, and paging buttons. It changes nothing.
func InventoryBrowseModal(d inventoryBrowseData) slacklib.ModalViewRequest {
	var blks []slacklib.Block

	plural := "s"
	if d.Total == 1 {
		plural = ""
	}
	blks = append(blks, slacklib.NewSectionBlock(
		invMD(fmt.Sprintf("*%s* — %d item%s", inventoryBrowseFilterLabel(d.Filter), d.Total, plural)), nil, nil))

	searchElem := slacklib.NewPlainTextInputBlockElement(invPlain("Search by name, code, location, or holder…"), ActionInventoryBrowseSearch)
	searchElem.InitialValue = d.Search
	searchBlock := slacklib.NewInputBlock(inventoryBrowseSearchBlockID, invPlain("Search"), nil, searchElem)
	searchBlock.Optional = true
	searchBlock.DispatchAction = true
	blks = append(blks, searchBlock)

	var options []*slacklib.OptionBlockObject
	var initial *slacklib.OptionBlockObject
	for _, f := range inventoryBrowseFilters {
		opt := slacklib.NewOptionBlockObject(f.value, invPlain(f.label), nil)
		options = append(options, opt)
		if f.value == d.Filter {
			initial = opt
		}
	}
	if initial == nil {
		initial = options[0]
	}
	filter := slacklib.NewOptionsSelectBlockElement(slacklib.OptTypeStatic, invPlain("Filter"), ActionInventoryBrowseFilter, options...)
	filter.InitialOption = initial
	blks = append(blks, slacklib.NewActionBlock(inventoryBrowseFilterBlockID, filter), slacklib.NewDividerBlock())

	if len(d.Rows) == 0 {
		blks = append(blks, slacklib.NewSectionBlock(invMD("_No items found._"), nil, nil))
	}
	for _, r := range d.Rows {
		holder := "nobody"
		if r.CurrentHolderUserID != nil {
			holder = "a member"
			if r.HolderName != "" {
				holder = escapeMrkdwn(r.HolderName)
			}
		}
		verified := "never"
		if r.LastVerifiedAt != nil {
			verified = r.LastVerifiedAt.Format("Jan 2, 2006")
		}
		text := fmt.Sprintf("*%s* `%s`", escapeMrkdwn(r.Name), store.FormatInventoryCode(r.Code))
		if r.ClaimPending {
			text += "  :warning: Claim pending"
		}
		text += fmt.Sprintf("\nHolder: %s · Last verified: %s", holder, verified)
		blks = append(blks, slacklib.NewSectionBlock(invMD(text), nil, nil))
	}

	if d.Pages > 1 {
		blks = append(blks, slacklib.NewDividerBlock(),
			slacklib.NewSectionBlock(invMD(fmt.Sprintf("Page %d of %d", d.Page, d.Pages)), nil, nil))
		var buttons []slacklib.BlockElement
		if d.Page > 1 {
			buttons = append(buttons, slacklib.NewButtonBlockElement(ActionInventoryBrowsePrev, fmt.Sprintf("page:%d", d.Page-1), invPlain("← Previous")))
		}
		if d.Page < d.Pages {
			buttons = append(buttons, slacklib.NewButtonBlockElement(ActionInventoryBrowseNext, fmt.Sprintf("page:%d", d.Page+1), invPlain("Next →")))
		}
		blks = append(blks, slacklib.NewActionBlock("inventory_browse_pager", buttons...))
	}

	return slacklib.ModalViewRequest{
		Type:       slacklib.VTModal,
		Title:      invPlain("Inventory"),
		Close:      invPlain("Close"),
		CallbackID: ModalIDInventoryBrowse,
		Blocks:     slacklib.Blocks{BlockSet: blks},
	}
}

// InventoryHandOffModal asks who the holder is handing the item to.
func InventoryHandOffModal(item store.InventoryItem) slacklib.ModalViewRequest {
	picker := slacklib.NewOptionsSelectBlockElement(slacklib.OptTypeUser, invPlain("Select a person"), inventoryHandOffUserAction)
	return slacklib.ModalViewRequest{
		Type:            slacklib.VTModal,
		Title:           invPlain("Hand off"),
		Submit:          invPlain("Hand off"),
		Close:           invPlain("Cancel"),
		CallbackID:      ModalIDInventoryHandOff,
		PrivateMetadata: item.ID,
		Blocks: slacklib.Blocks{BlockSet: []slacklib.Block{
			slacklib.NewSectionBlock(invMD("Hand off "+inventoryItemSummary(item)+" to someone else. They take possession right away."), nil, nil),
			slacklib.NewInputBlock(inventoryHandOffBlockID, invPlain("Hand off to"), nil, picker),
		}},
	}
}

// inventoryHeldPage returns one page of the held items, the page actually used (1-based, clamped into range),
// and the number of pages.
func inventoryHeldPage(items []store.InventoryItem, page int) ([]store.InventoryItem, int, int) {
	pages := max((len(items)+inventoryHeldPageSize-1)/inventoryHeldPageSize, 1)
	page = min(max(page, 1), pages)
	start := (page - 1) * inventoryHeldPageSize
	return items[start:min(start+inventoryHeldPageSize, len(items))], page, pages
}

// inventoryHeldPageOf reads which page of held items a modal is showing from its pager block, 1 if it has none.
func inventoryHeldPageOf(view slacklib.View) int {
	for _, b := range view.Blocks.BlockSet {
		if ab, ok := b.(*slacklib.ActionBlock); ok {
			if n, found := strings.CutPrefix(ab.BlockID, inventoryHeldPagePrefix); found {
				if page, err := strconv.Atoi(n); err == nil {
					return page
				}
			}
		}
	}
	return 1
}
