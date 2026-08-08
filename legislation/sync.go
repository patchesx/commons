package legislation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"commons/platform"
	"commons/store"
)

// openStatesDelay is the pause between consecutive OpenStates API requests.
// Free-tier accounts are limited to 1 req/sec and 500 req/day; 5 seconds provides
// a comfortable buffer and keeps daily usage well within limits.
const openStatesDelay = 5 * time.Second

// ---- OpenStates API types ----

type openStatesResponse struct {
	Results    []openStatesBill `json:"results"`
	Pagination struct {
		TotalItems int `json:"total_items"`
		PerPage    int `json:"per_page"`
		Page       int `json:"page"`
		MaxPage    int `json:"max_page"`
	} `json:"pagination"`
}

type openStatesBill struct {
	ID            string    `json:"id"`
	Session       string    `json:"session"`
	Identifier    string    `json:"identifier"`
	Title         string    `json:"title"`
	Subjects      []string  `json:"subjects"`
	UpdatedAt     time.Time `json:"updated_at"`
	OpenStatesURL string    `json:"openstates_url"`
	Versions      []struct {
		URL string `json:"url"`
	} `json:"versions"`
	Actions []struct {
		Description string `json:"description"`
		Date        string `json:"date"`
		Order       int    `json:"order"`
	} `json:"actions"`
	FromOrganization struct {
		Classification string `json:"classification"` // "lower" | "upper"
	} `json:"from_organization"`
	// Summary fields returned at the top level by the API.
	LatestActionDescription string `json:"latest_action_description"`
	LatestActionDate        string `json:"latest_action_date"` // "YYYY-MM-DD"
}

func (b openStatesBill) latestAction() (desc, date string) {
	if b.LatestActionDescription != "" {
		return b.LatestActionDescription, b.LatestActionDate
	}
	// Fall back to the highest-order action in the actions array.
	type bestAction struct {
		desc  string
		date  string
		order int
	}
	var best bestAction
	for _, a := range b.Actions {
		if a.Order > best.order {
			best = bestAction{a.Description, a.Date, a.Order}
		}
	}
	return best.desc, best.date
}

func inferBillType(identifier string) string {
	id := strings.ToUpper(identifier)
	if strings.Contains(id, "CR ") || strings.Contains(id, "JR ") ||
		strings.HasPrefix(id, "HCR") || strings.HasPrefix(id, "SCR") ||
		strings.HasPrefix(id, "HJR") || strings.HasPrefix(id, "SJR") ||
		strings.HasPrefix(id, "HR ") || strings.HasPrefix(id, "SR ") {
		return "resolution"
	}
	return "bill"
}

// ---- Legistar API types ----

type legistarMatter struct {
	MatterID              int     `json:"MatterId"`
	MatterGUID            string  `json:"MatterGuid"`
	MatterFile            string  `json:"MatterFile"`
	MatterName            string  `json:"MatterName"`
	MatterTitle           string  `json:"MatterTitle"`
	MatterTypeName        string  `json:"MatterTypeName"`
	MatterStatusName      string  `json:"MatterStatusName"`
	MatterBodyName        string  `json:"MatterBodyName"`
	MatterIntroDate       *string `json:"MatterIntroDate"`
	MatterPassedDate      *string `json:"MatterPassedDate"`
	MatterLastModifiedUtc string  `json:"MatterLastModifiedUtc"`
}

// ---- Main entry point ----

// Sync runs one full sync cycle for all active legislation bodies.
// Errors for individual bodies are logged and do not abort the overall sync.
func Sync(ctx context.Context, pool *pgxpool.Pool, encKey []byte, notifier platform.Notifier) {
	log.Println("legislation/sync: starting")

	// --- OpenStates sync job ---
	apiKey, err := store.GetServiceConfig(ctx, pool, "openstates", "api_key", encKey)
	if errors.Is(err, store.ErrNotFound) || apiKey == "" {
		log.Println("legislation/sync: openstates/api_key not configured — skipping OpenStates bodies")
	} else if err != nil {
		log.Printf("legislation/sync: read openstates/api_key: %v", err)
	} else {
		runOpenStatesSyncWithJob(ctx, pool, encKey, notifier, apiKey)
	}

	// --- Legistar sync job ---
	runLegistarSyncWithJob(ctx, pool, encKey, notifier)

	log.Println("legislation/sync: complete")
}

// runOpenStatesSyncWithJob wraps the OpenStates sync in a job record.
func runOpenStatesSyncWithJob(ctx context.Context, pool *pgxpool.Pool, encKey []byte, notifier platform.Notifier, apiKey string) {
	integration, err := store.GetIntegrationByType(ctx, pool, "openstates")
	if errors.Is(err, store.ErrNotFound) {
		log.Printf("legislation/sync: no enabled OpenStates integration found")
		return
	}
	if err != nil {
		log.Printf("legislation/sync: get openstates integration: %v", err)
		return
	}

	job := &store.Job{
		Type:    store.JobTypeOpenStatesSync,
		Feature: store.JobFeatureLegislationSync,
		Trigger: store.JobTriggerScheduled,
		Status:  store.JobStatusRunning,
	}
	if err := store.CreateJob(ctx, pool, job); err != nil {
		log.Printf("legislation/sync: create openstates job: %v", err)
		return
	}

	syncData := &OpenStatesSyncData{
		JobID:         job.ID,
		IntegrationID: integration.ID,
		BillsImported: 0,
		BillsUpdated:  0,
		BillsSkipped:  0,
	}
	if err := CreateOpenStatesSyncData(ctx, pool, syncData); err != nil {
		log.Printf("legislation/sync: create openstates sync data: %v", err)
		_ = store.FailJob(ctx, pool, job.ID, fmt.Sprintf("create sync data: %v", err))
		return
	}

	log.Printf("legislation/sync: starting OpenStates job %s", job.ID)

	// Run the actual sync.
	bodies, err := store.ListActiveBodiesBySource(ctx, pool, "openstates")
	if err != nil {
		log.Printf("legislation/sync: list openstates bodies: %v", err)
		_ = store.FailJob(ctx, pool, job.ID, fmt.Sprintf("list bodies: %v", err))
		return
	}

	totalImported, totalUpdated, totalSkipped := 0, 0, 0
	for _, body := range bodies {
		imported, updated, skipped, err := syncOpenStatesWithCounts(ctx, pool, encKey, notifier, body, apiKey)
		if err != nil {
			log.Printf("legislation/sync: openstates body %q: %v", body.Name, err)
			continue
		}
		totalImported += imported
		totalUpdated += updated
		totalSkipped += skipped
	}

	if err := UpdateOpenStatesSyncCounts(ctx, pool, job.ID, totalImported, totalUpdated, totalSkipped); err != nil {
		log.Printf("legislation/sync: update openstates counts: %v", err)
		_ = store.FailJob(ctx, pool, job.ID, fmt.Sprintf("update counts: %v", err))
		return
	}

	if err := store.CompleteJob(ctx, pool, job.ID); err != nil {
		log.Printf("legislation/sync: complete openstates job: %v", err)
		return
	}

	log.Printf("legislation/sync: OpenStates job %s complete, imported=%d updated=%d skipped=%d",
		job.ID, totalImported, totalUpdated, totalSkipped)
}

// parseLegistarClients splits a newline-separated config value into individual
// client subdomains, trimming whitespace and dropping empty lines.
func parseLegistarClients(val string) []string {
	var clients []string
	for _, line := range strings.Split(val, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			clients = append(clients, line)
		}
	}
	return clients
}

// runLegistarSyncWithJob wraps the Legistar sync in a job record.
func runLegistarSyncWithJob(ctx context.Context, pool *pgxpool.Pool, encKey []byte, notifier platform.Notifier) {
	integrations, err := store.ListIntegrationsByType(ctx, pool, "legistar")
	if err != nil {
		log.Printf("legislation/sync: list legistar integrations: %v", err)
		return
	}
	if len(integrations) == 0 {
		log.Printf("legislation/sync: no legistar integrations found")
		return
	}
	integration := integrations[0]

	clientsVal, err := store.GetServiceConfig(ctx, pool, "legistar", "clients", encKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("legislation/sync: read legistar/clients: %v", err)
		return
	}
	clients := parseLegistarClients(clientsVal)
	if len(clients) == 0 {
		log.Printf("legislation/sync: no legistar clients configured")
		return
	}

	job := &store.Job{
		Type:    store.JobTypeLegistarSync,
		Feature: store.JobFeatureLegislationSync,
		Trigger: store.JobTriggerScheduled,
		Status:  store.JobStatusRunning,
	}
	if err := store.CreateJob(ctx, pool, job); err != nil {
		log.Printf("legislation/sync: create legistar job: %v", err)
		return
	}

	syncData := &LegistarSyncData{
		JobID:         job.ID,
		IntegrationID: integration.ID,
		BillsImported: 0,
		BillsUpdated:  0,
	}
	if err := CreateLegistarSyncData(ctx, pool, syncData); err != nil {
		log.Printf("legislation/sync: create legistar sync data: %v", err)
		_ = store.FailJob(ctx, pool, job.ID, fmt.Sprintf("create sync data: %v", err))
		return
	}

	log.Printf("legislation/sync: starting Legistar job %s", job.ID)

	totalImported, totalUpdated := 0, 0
	for _, client := range clients {
		body, err := store.GetLegislativeBodyByClient(ctx, pool, client)
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				log.Printf("legislation/sync: lookup body for client %q: %v", client, err)
				continue
			}
			body = &store.LegislativeBody{
				Name:           client,
				Level:          "local",
				DataSource:     "legistar",
				LegistarClient: &client,
				Active:         true,
			}
			if err := store.CreateLegislativeBody(ctx, pool, body); err != nil {
				log.Printf("legislation/sync: create body for client %q: %v", client, err)
				continue
			}
		}
		imported, updated, err := syncLegistarWithCounts(ctx, pool, encKey, notifier, *body)
		if err != nil {
			log.Printf("legislation/sync: legistar body %q: %v", body.Name, err)
			continue
		}
		totalImported += imported
		totalUpdated += updated
	}

	if err := UpdateLegistarSyncCounts(ctx, pool, job.ID, totalImported, totalUpdated); err != nil {
		log.Printf("legislation/sync: update legistar counts: %v", err)
		_ = store.FailJob(ctx, pool, job.ID, fmt.Sprintf("update counts: %v", err))
		return
	}

	if err := store.CompleteJob(ctx, pool, job.ID); err != nil {
		log.Printf("legislation/sync: complete legistar job: %v", err)
		return
	}

	log.Printf("legislation/sync: Legistar job %s complete, imported=%d updated=%d",
		job.ID, totalImported, totalUpdated)
}

// ---- OpenStates sync ----

func syncOpenStates(ctx context.Context, pool *pgxpool.Pool, encKey []byte, notifier platform.Notifier, body store.LegislativeBody, apiKey string) error {
	if body.OpenStatesJurisdiction == nil {
		return fmt.Errorf("body has no openstates_jurisdiction")
	}

	// Pre-load existing following=true bills for change detection.
	existingBills, err := store.ListBillsByBody(ctx, pool, body.ID)
	if err != nil {
		return fmt.Errorf("load existing bills: %w", err)
	}
	existingMap := make(map[string]*store.Bill, len(existingBills))
	for i := range existingBills {
		if existingBills[i].ExternalID != nil {
			existingMap[*existingBills[i].ExternalID] = &existingBills[i]
		}
	}

	// Load import filters.
	filters, err := store.ListImportFilters(ctx, pool, body.ID)
	if err != nil {
		return fmt.Errorf("load import filters: %w", err)
	}

	processed := make(map[string]bool)

	// Phase 1: Refresh each currently tracked bill by its OpenStates ID.
	first := true
	for extID, existing := range existingMap {
		if extID == "" {
			continue
		}
		if !first {
			time.Sleep(openStatesDelay)
		}
		first = false
		apiBills, err := fetchOpenStatesBillsWithParams(ctx, apiKey, url.Values{"id": {extID}})
		if err != nil {
			log.Printf("legislation/sync: refresh OpenStates bill %q: %v", extID, err)
			continue
		}
		for _, ab := range apiBills {
			processOpenStatesBill(ctx, pool, encKey, notifier, body, ab, existing, filters, processed)
		}
	}

	// Phase 2: Auto-import bills matching subject filters (if any).
	subjects := collectSubjectFilters(filters)
	if len(subjects) == 0 {
		return nil
	}

	params := url.Values{
		"jurisdiction": {*body.OpenStatesJurisdiction},
		"per_page":     {"200"},
	}
	if body.OpenStatesChamber != nil && *body.OpenStatesChamber != "" {
		params.Set("chamber", *body.OpenStatesChamber)
	}
	for _, s := range subjects {
		params.Add("subject", s)
	}

	time.Sleep(openStatesDelay)
	apiBills, err := fetchOpenStatesBillsWithParams(ctx, apiKey, params)
	if err != nil {
		return fmt.Errorf("fetch OpenStates bills by subject: %w", err)
	}
	for _, ab := range apiBills {
		if processed[ab.ID] {
			continue
		}
		processOpenStatesBill(ctx, pool, encKey, notifier, body, ab, existingMap[ab.ID], filters, processed)
	}

	return nil
}

func processOpenStatesBill(
	ctx context.Context,
	pool *pgxpool.Pool,
	encKey []byte,
	notifier platform.Notifier,
	body store.LegislativeBody,
	ab openStatesBill,
	existing *store.Bill,
	filters []store.ImportFilter,
	processed map[string]bool,
) {
	if processed[ab.ID] {
		return
	}
	processed[ab.ID] = true

	isTracked := existing != nil
	matchesFilter := openStatesBillMatchesFilters(ab, filters)
	if !isTracked && !matchesFilter {
		return
	}

	actionDesc, actionDateStr := ab.latestAction()
	var actionDate *time.Time
	if actionDateStr != "" {
		if t, err := time.Parse("2006-01-02", actionDateStr); err == nil {
			actionDate = &t
		}
	}

	link := ab.OpenStatesURL
	if link == "" && len(ab.Versions) > 0 {
		link = ab.Versions[0].URL
	}

	b := store.Bill{
		BodyID:           body.ID,
		ExternalID:       ptrStr(ab.ID),
		Identifier:       ab.Identifier,
		Title:            ab.Title,
		Type:             ptrStrIfNE(inferBillType(ab.Identifier)),
		Chamber:          ptrStrIfNE(ab.FromOrganization.Classification),
		Session:          ptrStrIfNE(ab.Session),
		Status:           ptrStrIfNE(actionDesc),
		LatestAction:     ptrStrIfNE(actionDesc),
		LatestActionDate: actionDate,
		Link:             ptrStrIfNE(link),
		Subjects:         ab.Subjects,
		AutoImported:     matchesFilter,
		Following:        true,
	}

	var oldStatus, oldLatestAction *string
	if existing != nil {
		oldStatus = existing.Status
		oldLatestAction = existing.LatestAction
	}

	_, err := store.UpsertBill(ctx, pool, &b)
	if errors.Is(err, pgx.ErrNoRows) {
		return // Bill is dismissed; skip.
	}
	if err != nil {
		log.Printf("legislation/sync: upsert OpenStates bill %q: %v", ab.Identifier, err)
		return
	}

	if len(ab.Subjects) > 0 {
		if err := store.UpsertBodySubjects(ctx, pool, body.ID, ab.Subjects); err != nil {
			log.Printf("legislation/sync: upsert subjects for body %s: %v", body.ID, err)
		}
	}

	if existing != nil {
		notifyIfChanged(ctx, pool, encKey, notifier, b.ID, "status", oldStatus, b.Status, "openstates")
		notifyIfChanged(ctx, pool, encKey, notifier, b.ID, "latest_action", oldLatestAction, b.LatestAction, "openstates")
	}
}

func collectSubjectFilters(filters []store.ImportFilter) []string {
	var subjects []string
	for _, f := range filters {
		if f.FilterType == "subject" {
			subjects = append(subjects, f.Value)
		}
	}
	return subjects
}

func openStatesBillMatchesFilters(bill openStatesBill, filters []store.ImportFilter) bool {
	for _, f := range filters {
		if f.FilterType != "subject" {
			continue
		}
		for _, subj := range bill.Subjects {
			if strings.EqualFold(subj, f.Value) {
				return true
			}
		}
	}
	return false
}

// fetchOpenStatesBillsWithParams fetches all pages of bills from the OpenStates v3 API
// using the provided query parameters. A maximum of 10 pages (2000 bills) are fetched.
func fetchOpenStatesBillsWithParams(ctx context.Context, apiKey string, params url.Values) ([]openStatesBill, error) {
	const maxPages = 10

	var all []openStatesBill
	for page := 1; page <= maxPages; page++ {
		if page > 1 {
			time.Sleep(openStatesDelay)
		}
		params.Set("page", strconv.Itoa(page))
		rawURL := "https://v3.openstates.org/bills?" + params.Encode()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-API-KEY", apiKey)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("HTTP GET: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
		}

		var osr openStatesResponse
		if err := json.Unmarshal(body, &osr); err != nil {
			return nil, fmt.Errorf("unmarshal: %w", err)
		}

		all = append(all, osr.Results...)

		maxPage := osr.Pagination.MaxPage
		if maxPage == 0 {
			maxPage = 1
		}
		if page >= maxPage || len(osr.Results) == 0 {
			break
		}
	}
	return all, nil
}

// ---- Legistar sync ----

func syncLegistar(ctx context.Context, pool *pgxpool.Pool, encKey []byte, notifier platform.Notifier, body store.LegislativeBody) error {
	if body.LegistarClient == nil || *body.LegistarClient == "" {
		return fmt.Errorf("body has no legistar_client")
	}
	legistarClient := *body.LegistarClient

	existingBills, err := store.ListBillsByBody(ctx, pool, body.ID)
	if err != nil {
		return fmt.Errorf("load existing bills: %w", err)
	}
	existingMap := make(map[string]*store.Bill, len(existingBills))
	for i := range existingBills {
		if existingBills[i].ExternalID != nil {
			existingMap[*existingBills[i].ExternalID] = &existingBills[i]
		}
	}

	filters, err := store.ListImportFilters(ctx, pool, body.ID)
	if err != nil {
		return fmt.Errorf("load import filters: %w", err)
	}

	// Fetch matters modified in the last 30 days.
	since := time.Now().AddDate(0, 0, -30)
	matters, err := fetchLegistarMatters(ctx, legistarClient, body.LegistarBodyID, since)
	if err != nil {
		return fmt.Errorf("fetch legistar matters: %w", err)
	}

	for _, m := range matters {
		extID := strconv.Itoa(m.MatterID)
		existing := existingMap[extID]
		matchesFilter := legistarMatterMatchesFilters(m, filters)
		if existing == nil && !matchesFilter {
			continue
		}
		processLegistarMatter(ctx, pool, encKey, notifier, body, legistarClient, m, existing, matchesFilter)
	}
	return nil
}

func processLegistarMatter(
	ctx context.Context,
	pool *pgxpool.Pool,
	encKey []byte,
	notifier platform.Notifier,
	body store.LegislativeBody,
	legistarClient string,
	m legistarMatter,
	existing *store.Bill,
	autoImported bool,
) {
	extID := strconv.Itoa(m.MatterID)

	title := m.MatterTitle
	if title == "" {
		title = m.MatterName
	}
	if title == "" {
		title = m.MatterFile
	}

	identifier := m.MatterFile
	if identifier == "" {
		identifier = strconv.Itoa(m.MatterID)
	}

	legistarURL := fmt.Sprintf("https://%s.legistar.com/gateway.aspx?m=l&id=/matter.aspx?key=%d",
		legistarClient, m.MatterID)

	var introDate *time.Time
	if m.MatterIntroDate != nil {
		if t := parseLegistarDate(*m.MatterIntroDate); t != nil {
			introDate = t
		}
	}

	latestAction := m.MatterStatusName
	var latestActionDate *time.Time
	if m.MatterPassedDate != nil {
		if t := parseLegistarDate(*m.MatterPassedDate); t != nil {
			latestActionDate = t
			latestAction = "Passed"
		}
	}

	b := store.Bill{
		BodyID:           body.ID,
		ExternalID:       ptrStr(extID),
		Identifier:       identifier,
		Title:            title,
		Type:             ptrStrIfNE(strings.ToLower(m.MatterTypeName)),
		Status:           ptrStrIfNE(m.MatterStatusName),
		LatestAction:     ptrStrIfNE(latestAction),
		LatestActionDate: latestActionDate,
		IntroducedAt:     introDate,
		Link:             ptrStr(legistarURL),
		AutoImported:     autoImported,
		Following:        true,
	}

	var oldStatus, oldLatestAction *string
	if existing != nil {
		oldStatus = existing.Status
		oldLatestAction = existing.LatestAction
	}

	_, err := store.UpsertBill(ctx, pool, &b)
	if errors.Is(err, pgx.ErrNoRows) {
		return // Dismissed; skip.
	}
	if err != nil {
		log.Printf("legislation/sync: upsert legistar matter %q: %v", identifier, err)
		return
	}

	if existing != nil {
		notifyIfChanged(ctx, pool, encKey, notifier, b.ID, "status", oldStatus, b.Status, "legistar")
		notifyIfChanged(ctx, pool, encKey, notifier, b.ID, "latest_action", oldLatestAction, b.LatestAction, "legistar")
	}
}

func fetchLegistarMatters(ctx context.Context, legistarClient string, bodyID *int, since time.Time) ([]legistarMatter, error) {
	const top = 200

	sinceStr := since.UTC().Format("2006-01-02T15:04:05")
	filterParts := []string{fmt.Sprintf("MatterLastModifiedUtc gt datetime'%s'", sinceStr)}
	if bodyID != nil {
		filterParts = append(filterParts, fmt.Sprintf("MatterBodyId eq %d", *bodyID))
	}
	filterStr := strings.Join(filterParts, " and ")

	var all []legistarMatter
	for skip := 0; ; skip += top {
		rawURL := fmt.Sprintf("https://webapi.legistar.com/v1/%s/matters?$filter=%s&$top=%d&$skip=%d",
			legistarClient, url.QueryEscape(filterStr), top, skip)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("HTTP GET: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read response: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
		}

		var page []legistarMatter
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("unmarshal: %w", err)
		}

		all = append(all, page...)
		if len(page) < top {
			break
		}
	}
	return all, nil
}

func legistarMatterMatchesFilters(m legistarMatter, filters []store.ImportFilter) bool {
	for _, f := range filters {
		if f.FilterType != "matter_type" {
			continue
		}
		if strings.EqualFold(m.MatterTypeName, f.Value) {
			return true
		}
	}
	return false
}

func parseLegistarDate(s string) *time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// ---- Change detection and notification ----

// notifyIfChanged records a bill_updates row and notifies subscribers if a field changed.
// Only status changes trigger notifications; all changes are logged to bill_updates.
func notifyIfChanged(
	ctx context.Context,
	pool *pgxpool.Pool,
	encKey []byte,
	notifier platform.Notifier,
	billID, field string,
	oldVal, newVal *string,
	source string,
) {
	oldStr := ""
	if oldVal != nil {
		oldStr = *oldVal
	}
	newStr := ""
	if newVal != nil {
		newStr = *newVal
	}
	if oldStr == newStr {
		return
	}

	u := &store.BillUpdate{
		BillID:   billID,
		Field:    field,
		OldValue: oldVal,
		NewValue: newVal,
		Source:   source,
	}
	if err := store.RecordBillUpdate(ctx, pool, u); err != nil {
		log.Printf("legislation/sync: record bill update %s/%s: %v", billID, field, err)
	}

	// Notifications are only sent for status changes.
	if field != "status" {
		return
	}

	bill, err := store.GetBill(ctx, pool, billID)
	if err != nil || bill == nil {
		return
	}

	subscribers, err := store.GetBillSubscribers(ctx, pool, billID)
	if err != nil {
		log.Printf("legislation/sync: get subscribers for %s: %v", billID, err)
		return
	}
	if len(subscribers) == 0 {
		return
	}

	text := fmt.Sprintf(":bookmark: *%s: %s*\nStatus updated: %s → %s",
		bill.Identifier, bill.Title, oldStr, newStr)
	if bill.LatestAction != nil && *bill.LatestAction != "" {
		text += fmt.Sprintf("\nLatest action: %s", *bill.LatestAction)
	}
	if bill.Link != nil && *bill.Link != "" {
		text += fmt.Sprintf("\n%s", *bill.Link)
	}

	title := fmt.Sprintf("%s: %s", bill.Identifier, bill.Title)

	for _, sub := range subscribers {
		if err := store.NotifyUser(ctx, pool, encKey, sub.ID, "bill_update", title, text, "", notifier); err != nil {
			log.Printf("legislation/sync: notify subscriber %s for bill %s: %v", sub.ID, bill.Identifier, err)
		}
	}
}

// ---- Counting versions for job tracking ----

// syncOpenStatesWithCounts runs an OpenStates sync for a single body and returns counts.
func syncOpenStatesWithCounts(ctx context.Context, pool *pgxpool.Pool, encKey []byte, notifier platform.Notifier, body store.LegislativeBody, apiKey string) (imported, updated, skipped int, err error) {
	if body.OpenStatesJurisdiction == nil {
		return 0, 0, 0, fmt.Errorf("body has no openstates_jurisdiction")
	}

	// Pre-load existing following=true bills for change detection.
	existingBills, err := store.ListBillsByBody(ctx, pool, body.ID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("load existing bills: %w", err)
	}
	existingMap := make(map[string]*store.Bill, len(existingBills))
	for i := range existingBills {
		if existingBills[i].ExternalID != nil {
			existingMap[*existingBills[i].ExternalID] = &existingBills[i]
		}
	}

	// Load import filters.
	filters, err := store.ListImportFilters(ctx, pool, body.ID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("load import filters: %w", err)
	}

	processed := make(map[string]bool)

	// Phase 1: Refresh each currently tracked bill by its OpenStates ID.
	first := true
	for extID, existing := range existingMap {
		if extID == "" {
			continue
		}
		if !first {
			time.Sleep(openStatesDelay)
		}
		first = false
		apiBills, err := fetchOpenStatesBillsWithParams(ctx, apiKey, url.Values{"id": {extID}})
		if err != nil {
			log.Printf("legislation/sync: refresh OpenStates bill %q: %v", extID, err)
			continue
		}
		for _, ab := range apiBills {
			processOpenStatesBillWithCounts(ctx, pool, encKey, notifier, body, ab, existing, filters, processed, &imported, &updated, &skipped)
		}
	}

	// Phase 2: Auto-import bills matching subject filters (if any).
	subjects := collectSubjectFilters(filters)
	if len(subjects) == 0 {
		return imported, updated, skipped, nil
	}

	params := url.Values{
		"jurisdiction": {*body.OpenStatesJurisdiction},
		"per_page":     {"200"},
	}
	if body.OpenStatesChamber != nil && *body.OpenStatesChamber != "" {
		params.Set("chamber", *body.OpenStatesChamber)
	}
	for _, s := range subjects {
		params.Add("subject", s)
	}

	time.Sleep(openStatesDelay)
	apiBills, err := fetchOpenStatesBillsWithParams(ctx, apiKey, params)
	if err != nil {
		return imported, updated, skipped, fmt.Errorf("fetch OpenStates bills by subject: %w", err)
	}
	for _, ab := range apiBills {
		if processed[ab.ID] {
			continue
		}
		processOpenStatesBillWithCounts(ctx, pool, encKey, notifier, body, ab, existingMap[ab.ID], filters, processed, &imported, &updated, &skipped)
	}

	return imported, updated, skipped, nil
}

// processOpenStatesBillWithCounts updates counters based on what happens.
func processOpenStatesBillWithCounts(
	ctx context.Context,
	pool *pgxpool.Pool,
	encKey []byte,
	notifier platform.Notifier,
	body store.LegislativeBody,
	ab openStatesBill,
	existing *store.Bill,
	filters []store.ImportFilter,
	processed map[string]bool,
	imported, updated, skipped *int,
) {
	if processed[ab.ID] {
		return
	}
	processed[ab.ID] = true

	isTracked := existing != nil
	matchesFilter := openStatesBillMatchesFilters(ab, filters)
	if !isTracked && !matchesFilter {
		return
	}

	actionDesc, actionDateStr := ab.latestAction()
	var actionDate *time.Time
	if actionDateStr != "" {
		if t, err := time.Parse("2006-01-02", actionDateStr); err == nil {
			actionDate = &t
		}
	}

	link := ab.OpenStatesURL
	if link == "" && len(ab.Versions) > 0 {
		link = ab.Versions[0].URL
	}

	b := store.Bill{
		BodyID:           body.ID,
		ExternalID:       ptrStr(ab.ID),
		Identifier:       ab.Identifier,
		Title:            ab.Title,
		Type:             ptrStrIfNE(inferBillType(ab.Identifier)),
		Chamber:          ptrStrIfNE(ab.FromOrganization.Classification),
		Session:          ptrStrIfNE(ab.Session),
		Status:           ptrStrIfNE(actionDesc),
		LatestAction:     ptrStrIfNE(actionDesc),
		LatestActionDate: actionDate,
		Link:             ptrStrIfNE(link),
		Subjects:         ab.Subjects,
		AutoImported:     matchesFilter,
		Following:        true,
	}

	var oldStatus, oldLatestAction *string
	if existing != nil {
		oldStatus = existing.Status
		oldLatestAction = existing.LatestAction
	}

	_, err := store.UpsertBill(ctx, pool, &b)
	if errors.Is(err, pgx.ErrNoRows) {
		// Bill is dismissed; count as skipped.
		*skipped++
		return
	}
	if err != nil {
		log.Printf("legislation/sync: upsert OpenStates bill %q: %v", ab.Identifier, err)
		return
	}

	if len(ab.Subjects) > 0 {
		if err := store.UpsertBodySubjects(ctx, pool, body.ID, ab.Subjects); err != nil {
			log.Printf("legislation/sync: upsert subjects for body %s: %v", body.ID, err)
		}
	}

	if existing == nil {
		// New bill imported.
		*imported++
	} else {
		// Existing bill updated.
		*updated++
		notifyIfChanged(ctx, pool, encKey, notifier, b.ID, "status", oldStatus, b.Status, "openstates")
		notifyIfChanged(ctx, pool, encKey, notifier, b.ID, "latest_action", oldLatestAction, b.LatestAction, "openstates")
	}
}

// syncLegistarWithCounts runs a Legistar sync for a single body and returns counts.
func syncLegistarWithCounts(ctx context.Context, pool *pgxpool.Pool, encKey []byte, notifier platform.Notifier, body store.LegislativeBody) (imported, updated int, err error) {
	if body.LegistarClient == nil || *body.LegistarClient == "" {
		return 0, 0, fmt.Errorf("body has no legistar_client")
	}
	legistarClient := *body.LegistarClient

	existingBills, err := store.ListBillsByBody(ctx, pool, body.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("load existing bills: %w", err)
	}
	existingMap := make(map[string]*store.Bill, len(existingBills))
	for i := range existingBills {
		if existingBills[i].ExternalID != nil {
			existingMap[*existingBills[i].ExternalID] = &existingBills[i]
		}
	}

	filters, err := store.ListImportFilters(ctx, pool, body.ID)
	if err != nil {
		return 0, 0, fmt.Errorf("load import filters: %w", err)
	}

	// Fetch matters modified in the last 30 days.
	since := time.Now().AddDate(0, 0, -30)
	matters, err := fetchLegistarMatters(ctx, legistarClient, body.LegistarBodyID, since)
	if err != nil {
		return 0, 0, fmt.Errorf("fetch legistar matters: %w", err)
	}

	for _, m := range matters {
		extID := strconv.Itoa(m.MatterID)
		existing := existingMap[extID]
		matchesFilter := legistarMatterMatchesFilters(m, filters)
		if existing == nil && !matchesFilter {
			continue
		}
		processLegistarMatterWithCounts(ctx, pool, encKey, notifier, body, legistarClient, m, existing, matchesFilter, &imported, &updated)
	}
	return imported, updated, nil
}

// processLegistarMatterWithCounts updates counters.
func processLegistarMatterWithCounts(
	ctx context.Context,
	pool *pgxpool.Pool,
	encKey []byte,
	notifier platform.Notifier,
	body store.LegislativeBody,
	legistarClient string,
	m legistarMatter,
	existing *store.Bill,
	autoImported bool,
	imported, updated *int,
) {
	extID := strconv.Itoa(m.MatterID)

	title := m.MatterTitle
	if title == "" {
		title = m.MatterName
	}
	if title == "" {
		title = m.MatterFile
	}

	identifier := m.MatterFile
	if identifier == "" {
		identifier = strconv.Itoa(m.MatterID)
	}

	legistarURL := fmt.Sprintf("https://%s.legistar.com/gateway.aspx?m=l&id=/matter.aspx?key=%d",
		legistarClient, m.MatterID)

	var introDate *time.Time
	if m.MatterIntroDate != nil {
		if t := parseLegistarDate(*m.MatterIntroDate); t != nil {
			introDate = t
		}
	}

	latestAction := m.MatterStatusName
	var latestActionDate *time.Time
	if m.MatterPassedDate != nil {
		if t := parseLegistarDate(*m.MatterPassedDate); t != nil {
			latestActionDate = t
			latestAction = "Passed"
		}
	}

	b := store.Bill{
		BodyID:           body.ID,
		ExternalID:       ptrStr(extID),
		Identifier:       identifier,
		Title:            title,
		Type:             ptrStrIfNE(strings.ToLower(m.MatterTypeName)),
		Status:           ptrStrIfNE(m.MatterStatusName),
		LatestAction:     ptrStrIfNE(latestAction),
		LatestActionDate: latestActionDate,
		IntroducedAt:     introDate,
		Link:             ptrStr(legistarURL),
		AutoImported:     autoImported,
		Following:        true,
	}

	var oldStatus, oldLatestAction *string
	if existing != nil {
		oldStatus = existing.Status
		oldLatestAction = existing.LatestAction
	}

	_, err := store.UpsertBill(ctx, pool, &b)
	if errors.Is(err, pgx.ErrNoRows) {
		// Dismissed; not counted as imported or updated.
		return
	}
	if err != nil {
		log.Printf("legislation/sync: upsert legistar matter %q: %v", identifier, err)
		return
	}

	if existing == nil {
		*imported++
	} else {
		*updated++
		notifyIfChanged(ctx, pool, encKey, notifier, b.ID, "status", oldStatus, b.Status, "legistar")
		notifyIfChanged(ctx, pool, encKey, notifier, b.ID, "latest_action", oldLatestAction, b.LatestAction, "legistar")
	}
}

// ---- Helpers ----

func ptrStr(s string) *string { return &s }

func ptrStrIfNE(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
