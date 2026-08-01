package slack

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/store"
)

// LegislationModal builds a read-only bill list modal with per-bill subscribe/unsubscribe buttons.
// subscribedBillIDs is the set of bill IDs the viewing user is currently subscribed to.
func LegislationModal(bills []store.Bill, subscribedBillIDs map[string]bool) slacklib.ModalViewRequest {
	var blks []slacklib.Block

	if len(bills) == 0 {
		blks = []slacklib.Block{
			slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, "_No legislation tracked yet. Add bills via the admin panel._", false, false),
				nil, nil,
			),
		}
	} else {
		// Group bills by body name, preserving order of first appearance.
		type group struct {
			bodyName string
			bills    []store.Bill
		}
		var groups []group
		bodyIndex := map[string]int{}
		for _, b := range bills {
			if i, ok := bodyIndex[b.BodyName]; ok {
				groups[i].bills = append(groups[i].bills, b)
			} else {
				bodyIndex[b.BodyName] = len(groups)
				groups = append(groups, group{bodyName: b.BodyName, bills: []store.Bill{b}})
			}
		}

		for gi, g := range groups {
			if gi > 0 {
				blks = append(blks, slacklib.NewDividerBlock())
			}
			// Body header.
			blks = append(blks, slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType, "*"+g.bodyName+"*", false, false),
				nil, nil,
			))

			for _, b := range g.bills {
				// Build bill text.
				var parts []string
				if b.Status != nil && *b.Status != "" {
					parts = append(parts, "Status: "+*b.Status)
				}
				if b.ChapterPosition != nil {
					parts = append(parts, positionLabel(*b.ChapterPosition))
				}
				detail := ""
				if len(parts) > 0 {
					detail = "\n" + strings.Join(parts, " · ")
				}

				billText := fmt.Sprintf("*%s — %s*%s", b.Identifier, b.Title, detail)
				if b.Link != nil && *b.Link != "" {
					billText = fmt.Sprintf("*<%s|%s — %s>*%s", *b.Link, b.Identifier, b.Title, detail)
				}

				// Subscribe/unsubscribe button.
				var btn *slacklib.ButtonBlockElement
				if subscribedBillIDs[b.ID] {
					btn = slacklib.NewButtonBlockElement(
						ActionUnsubscribeBill, b.ID,
						slacklib.NewTextBlockObject(slacklib.PlainTextType, "🔔 Unsubscribe", false, false),
					)
				} else {
					btn = slacklib.NewButtonBlockElement(
						ActionSubscribeBill, b.ID,
						slacklib.NewTextBlockObject(slacklib.PlainTextType, "🔕 Subscribe", false, false),
					)
				}

				blks = append(blks, slacklib.NewSectionBlock(
					slacklib.NewTextBlockObject(slacklib.MarkdownType, billText, false, false),
					nil, slacklib.NewAccessory(btn),
				))

				// Enforce Block Kit modal limit (100 blocks).
				if len(blks) >= 95 {
					blks = append(blks, slacklib.NewSectionBlock(
						slacklib.NewTextBlockObject(slacklib.MarkdownType, "_More bills tracked — use the admin panel to see all._", false, false),
						nil, nil,
					))
					goto done
				}
			}
		}
	}

done:
	return slacklib.ModalViewRequest{
		Type:       slacklib.VTModal,
		Title:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Legislation", false, false),
		Close:      slacklib.NewTextBlockObject(slacklib.PlainTextType, "Close", false, false),
		CallbackID: ModalIDLegislation,
		Blocks:     slacklib.Blocks{BlockSet: blks},
	}
}

func positionLabel(pos string) string {
	switch pos {
	case "support":
		return "📗 Support"
	case "oppose":
		return "📕 Oppose"
	case "monitor":
		return "📘 Monitor"
	case "neutral":
		return "📒 Neutral"
	default:
		return ""
	}
}

// openLegislationModal loads bills and subscriptions for a user and opens the legislation modal.
func openLegislationModal(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, triggerID, slackUserID string) {
	user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", slackUserID, "", "")
	if err != nil {
		log.Printf("slack/legislation: get user %s: %v", slackUserID, err)
		return
	}

	bills, err := store.ListBills(ctx, pool)
	if err != nil {
		log.Printf("slack/legislation: list bills: %v", err)
		return
	}

	subs, err := store.GetUserSubscriptions(ctx, pool, user.ID)
	if err != nil {
		log.Printf("slack/legislation: get subscriptions for %s: %v", slackUserID, err)
	}
	subscribedIDs := make(map[string]bool, len(subs))
	for _, b := range subs {
		subscribedIDs[b.ID] = true
	}

	modal := LegislationModal(bills, subscribedIDs)
	if _, err := client.OpenViewContext(ctx, triggerID, modal); err != nil {
		log.Printf("slack/legislation: open modal: %v", err)
	}
}

// refreshLegislationModal reloads and updates an already-open legislation modal.
// Called after a subscribe/unsubscribe action to reflect the new state.
func refreshLegislationModal(ctx context.Context, pool *pgxpool.Pool, client *slacklib.Client, viewID, viewHash, slackUserID string) {
	user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", slackUserID, "", "")
	if err != nil {
		log.Printf("slack/legislation: get user %s for refresh: %v", slackUserID, err)
		return
	}

	bills, err := store.ListBills(ctx, pool)
	if err != nil {
		log.Printf("slack/legislation: list bills for refresh: %v", err)
		return
	}

	subs, err := store.GetUserSubscriptions(ctx, pool, user.ID)
	if err != nil {
		log.Printf("slack/legislation: get subscriptions for refresh: %v", err)
	}
	subscribedIDs := make(map[string]bool, len(subs))
	for _, b := range subs {
		subscribedIDs[b.ID] = true
	}

	modal := LegislationModal(bills, subscribedIDs)
	if _, err := client.UpdateViewContext(ctx, modal, "", viewHash, viewID); err != nil {
		log.Printf("slack/legislation: update modal: %v", err)
	}
}
