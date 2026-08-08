package slack

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	slacklib "github.com/slack-go/slack"

	"commons/integrations/slack/blocks"
	"commons/permissions"
	"commons/store"
)

// hasPermission reports whether the permission key is in the list.
func hasPermission(perms []string, key string) bool {
	for _, p := range perms {
		if p == key {
			return true
		}
	}
	return false
}

// Publish builds and publishes the role-aware App Home view for a Slack user.
// Called in a background goroutine from the events handler.
func Publish(ctx context.Context, pool *pgxpool.Pool, encKey []byte, client *slacklib.Client, slackUserID string) {
	title := "Commons - Slack Bot"
	if t, err := store.GetServiceConfig(ctx, pool, "bot", "title", encKey); err == nil && t != "" {
		title = t
	}
	welcomeMsg := "Access team calendars and view upcoming events, access private channels, and more."
	if m, err := store.GetServiceConfig(ctx, pool, "bot", "welcome_message", encKey); err == nil && m != "" {
		welcomeMsg = m
	}
	realName := ""
	displayName := ""
	profileEmail := ""
	if info, err := client.GetUserInfoContext(ctx, slackUserID); err != nil {
		log.Printf("slack/apphome: failed to get Slack profile for %s: %v", slackUserID, err)
	} else {
		if info.IsBot {
			return
		}
		realName = info.Profile.RealName
		displayName = info.Profile.DisplayName
		profileEmail = info.Profile.Email
		if realName == "" {
			realName = displayName // fall back to display name if no real name
		}
		if realName == "" {
			realName = slackUserID // last resort: API succeeded but no profile fields set
		}
	}

	user, err := store.GetOrCreateUserByIdentity(ctx, pool, "slack", slackUserID, realName, displayName)
	if err != nil {
		log.Printf("slack/apphome: get/create user %s: %v", slackUserID, err)
		return
	}
	if profileEmail != "" {
		if err := store.UpdateUserEmail(ctx, pool, user.ID, profileEmail); err != nil {
			log.Printf("slack/apphome: update email %s: %v", slackUserID, err)
		}
	}

	perms, err := store.GetUserPermissions(ctx, pool, user.ID)
	if err != nil {
		log.Printf("slack/apphome: get permissions for %s: %v", slackUserID, err)
		return
	}

	var blks []slacklib.Block

	// Header — always shown.
	refreshBtn := slacklib.NewButtonBlockElement(
		ActionRefreshHome, "refresh",
		slacklib.NewTextBlockObject(slacklib.PlainTextType, "↻ Refresh", false, false),
	)
	blks = append(blks,
		slacklib.NewHeaderBlock(
			slacklib.NewTextBlockObject(slacklib.PlainTextType, title, false, false),
		),
		slacklib.NewSectionBlock(
			slacklib.NewTextBlockObject(slacklib.MarkdownType, welcomeMsg, false, false),
			nil, slacklib.NewAccessory(refreshBtn),
		),
	)

	// Announcement — shown to all users if set.
	announcement, _ := store.GetServiceConfig(ctx, pool, "bot", "announcement", encKey)
	if announcement != "" {
		blks = append(blks,
			slacklib.NewDividerBlock(),
			slacklib.NewHeaderBlock(
				slacklib.NewTextBlockObject(slacklib.PlainTextType, ":mega:  "+announcement, true, false),
			),
		)
	}

	// Calendar — shown only to users with calendar.view permission.
	if hasPermission(perms, permissions.CalendarView) {
		var activeCal *store.Calendar

		// 1. User's personal selection.
		if user.SelectedCalendarID != nil && *user.SelectedCalendarID != "" {
			if c, err := store.GetCalendarByID(ctx, pool, *user.SelectedCalendarID); err == nil {
				activeCal = c
			}
		}

		// 2. Admin-configured default calendar.
		if activeCal == nil {
			if defID, err := store.GetServiceConfig(ctx, pool, "bot", "default_calendar_id", encKey); err == nil && defID != "" {
				if c, err := store.GetCalendarByID(ctx, pool, defID); err == nil {
					activeCal = c
				}
			}
		}

		// 3. First calendar alphabetically.
		if activeCal == nil {
			if cals, err := store.ListCalendars(ctx, pool); err == nil && len(cals) > 0 {
				activeCal = &cals[0]
			}
		}

		if activeCal != nil {
			// Build a calendar-switcher dropdown when there are multiple calendars.
			// It is passed into calendarBlocksForCalendar as an accessory on the
			// "Upcoming Events" title row so it doesn't occupy its own separate section.
			var calAccessory slacklib.BlockElement
			cals, _ := store.ListCalendars(ctx, pool)
			if len(cals) > 1 {
				options := make([]*slacklib.OptionBlockObject, len(cals))
				var initialOption *slacklib.OptionBlockObject
				for i, c := range cals {
					opt := slacklib.NewOptionBlockObject(
						c.ID,
						slacklib.NewTextBlockObject(slacklib.PlainTextType, c.Name, false, false),
						nil,
					)
					options[i] = opt
					if c.ID == activeCal.ID {
						initialOption = opt
					}
				}
				sel := slacklib.NewOptionsSelectBlockElement(
					slacklib.OptTypeStatic,
					slacklib.NewTextBlockObject(slacklib.PlainTextType, "Select calendar…", false, false),
					ActionSelectCalendar,
					options...,
				)
				sel.InitialOption = initialOption
				calAccessory = sel
			}
			blks = append(blks, calendarBlocksForCalendar(ctx, pool, *activeCal, calAccessory)...)

			if activeCal.IcalURL == nil && hasPermission(perms, permissions.CalendarManage) {
				addBtn := slacklib.NewButtonBlockElement(
					ActionAddCalendarEvent, activeCal.ID,
					slacklib.NewTextBlockObject(slacklib.PlainTextType, ":heavy_plus_sign: Add Event", true, false),
				)
				editBtn := slacklib.NewButtonBlockElement(
					ActionCalendarShowList, activeCal.ID,
					slacklib.NewTextBlockObject(slacklib.PlainTextType, ":pencil2: Edit Events", true, false),
				)
				blks = append(blks, slacklib.NewActionBlock("calendar_manage_actions", addBtn, editBtn))
			}
		}
	}

	// Collect action buttons for permission-gated features.
	var actionButtons []slacklib.BlockElement

	if contacts, err := store.ListContacts(ctx, pool); err == nil && len(contacts) > 0 {
		actionButtons = append(actionButtons, slacklib.NewButtonBlockElement(
			ActionOpenContacts, "open",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, ":speech_balloon: Contact", true, false),
		))
	}
	// Resources button disabled pending category redesign.
	// if hasPermission(perms, permissions.ResourcesView) {
	// 	if hasResources, _ := store.HasResources(ctx, pool); hasResources {
	// 		actionButtons = append(actionButtons, slacklib.NewButtonBlockElement(
	// 			ActionOpenResources, "open",
	// 			slacklib.NewTextBlockObject(slacklib.PlainTextType, ":books: Resources", true, false),
	// 		))
	// 	}
	// }

	if hasPermission(perms, permissions.ChannelsRequestAccess) {
		actionButtons = append(actionButtons, slacklib.NewButtonBlockElement(
			ActionRequestChannelAccess, "open",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, ":lock: Channel Request", true, false),
		))
	}

	if approverChannelIDs, err := store.GetUserChannelApprovals(ctx, pool, user.ID); err != nil {
		log.Printf("slack/apphome: list channel approvals for %s: %v", user.ID, err)
	} else if len(approverChannelIDs) > 0 {
		pending, _ := store.ListPendingRequestsForChannels(ctx, pool, approverChannelIDs)
		if len(pending) > 0 {
			label := fmt.Sprintf(":bell: Pending Requests (%d)", len(pending))
			actionButtons = append(actionButtons, slacklib.NewButtonBlockElement(
				ActionOpenPendingRequests, "open",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, label, true, false),
			))
		}
	}

	if hasPermission(perms, permissions.LegislationView) {
		if hasTrackedBills, _ := store.HasTrackedBills(ctx, pool); hasTrackedBills {
			actionButtons = append(actionButtons, slacklib.NewButtonBlockElement(
				ActionOpenLegislation, "open",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, ":bookmark_tabs: Legislation Tracking", true, false),
			))
		}
	}

	if hasPermission(perms, permissions.MeetingsSchedule) {
		actionButtons = append(actionButtons, slacklib.NewButtonBlockElement(
			ActionScheduleMeeting, "schedule_meeting",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, ":calendar: Schedule Meeting", true, false),
		))
	}
	if hasPermission(perms, permissions.MeetingsManage) {
		actionButtons = append(actionButtons, slacklib.NewButtonBlockElement(
			ActionManageMeetings, "manage_meetings",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, ":pencil: Manage Meetings", true, false),
		))
	}

	if hasPermission(perms, permissions.WorkItemsCreate) {
		actionButtons = append(actionButtons, slacklib.NewButtonBlockElement(
			ActionReportIssue, "open",
			slacklib.NewTextBlockObject(slacklib.PlainTextType, ":memo: Report Issue", true, false),
		))
	}
	isAdmin := hasPermission(perms, permissions.RecordingsConfigWrite) || hasPermission(perms, permissions.UploadsConfigWrite)
	if isAdmin {
		adminURL, _ := store.GetServiceConfig(ctx, pool, "bot", "admin_url", encKey)
		if adminURL != "" {
			btn := slacklib.NewButtonBlockElement(
				"open_admin", "",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, ":gear: Admin Panel", true, false),
			)
			btn.URL = adminURL
			actionButtons = append(actionButtons, btn)
		} else {
			actionButtons = append(actionButtons, slacklib.NewButtonBlockElement(
				"open_admin", "",
				slacklib.NewTextBlockObject(slacklib.PlainTextType, ":gear: Admin Panel", true, false),
			))
		}
	}

	if len(actionButtons) > 0 {
		blks = append(blks, slacklib.NewDividerBlock())
		// Slack allows max 5 elements per actions block.
		for i := 0; i < len(actionButtons); i += 5 {
			end := i + 5
			if end > len(actionButtons) {
				end = len(actionButtons)
			}
			blks = append(blks, slacklib.NewActionBlock(fmt.Sprintf("actions_%d", i), actionButtons[i:end]...))
		}
	}

	// Quick Links + Contacts — shown side by side only to users with quick_links.view permission.
	if hasPermission(perms, permissions.QuickLinksView) {
		quickLinks, _ := store.ListActiveQuickLinks(ctx, pool)
		contacts, _ := store.ListContacts(ctx, pool)
		if len(quickLinks) > 0 || len(contacts) > 0 {
			blks = append(blks, slacklib.NewDividerBlock())
			var fields []*slacklib.TextBlockObject
			if len(quickLinks) > 0 {
				var lines []string
				for _, l := range quickLinks {
					lines = append(lines, fmt.Sprintf("• <%s|%s>", l.URL, l.Label))
				}
				fields = append(fields, slacklib.NewTextBlockObject(slacklib.MarkdownType,
					"*Quick Links*\n"+strings.Join(lines, "\n"), false, false))
			}
			if len(contacts) > 0 {
				var lines []string
				for _, c := range contacts {
					lines = append(lines, fmt.Sprintf("*%s*\n%s", c.Username, c.Title))
				}
				fields = append(fields, slacklib.NewTextBlockObject(slacklib.MarkdownType,
					"*Comrades*\n\n"+strings.Join(lines, "\n\n"), false, false))
			}
			blks = append(blks, slacklib.NewSectionBlock(nil, fields, nil))
		}
	}

	if len(perms) == 0 {
		// New member: show welcome message.
		blks = append(blks,
			slacklib.NewDividerBlock(),
			slacklib.NewSectionBlock(
				slacklib.NewTextBlockObject(slacklib.MarkdownType,
					":wave: *Welcome!* You don't have a role assigned yet.\nContact an admin to get access to the bot's features.", false, false),
				nil, nil,
			),
		)
	}

	view := slacklib.HomeTabViewRequest{
		Type:   slacklib.VTHomeTab,
		Blocks: slacklib.Blocks{BlockSet: blks},
	}

	if _, err := client.PublishViewContext(ctx, slacklib.PublishViewContextRequest{
		UserID: slackUserID,
		View:   view,
	}); err != nil {
		log.Printf("slack/apphome: publish view for %s: %v", slackUserID, err)
	}
}

// buildRequestDisplays enriches pending requests with requester names via user lookup.
func buildRequestDisplays(ctx context.Context, pool *pgxpool.Pool, requests []store.ChannelRequest) []blocks.ChannelRequestDisplay {
	var displays []blocks.ChannelRequestDisplay
	for _, req := range requests {
		name := "Unknown"
		if req.RequesterID != nil {
			if u, err := store.GetUserByID(ctx, pool, *req.RequesterID); err == nil && u != nil {
				name = u.DisplayName
			}
		}
		displays = append(displays, blocks.ChannelRequestDisplay{
			ID:               req.ID,
			RequesterName:    name,
			SlackChannelName: req.SlackChannelName,
			RequestedAt:      req.RequestedAt,
		})
	}
	return displays
}
