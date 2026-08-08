package legislation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// BrowseParams holds filter/pagination options for browsing available bills.
type BrowseParams struct {
	Page    int    // 1-based
	PerPage int    // default 20
	Session string // optional
	Chamber string // optional (OpenStates only)
	Subject string // optional (OpenStates only)
	Query   string // optional full-text search (OpenStates only)
	Sort    string // optional (OpenStates: updated_desc, etc.)
}

// BrowseBillResult is a single bill from the source API, enriched with tracked status.
type BrowseBillResult struct {
	ExternalID       string   `json:"external_id"`
	BillID           string   `json:"bill_id,omitempty"` // populated when Tracked=true
	Identifier       string   `json:"identifier"`
	Title            string   `json:"title"`
	LatestAction     string   `json:"latest_action,omitempty"`
	LatestActionDate *time.Time `json:"latest_action_date,omitempty"`
	Session          string   `json:"session,omitempty"`
	Chamber          string   `json:"chamber,omitempty"`
	Link             string   `json:"link,omitempty"`
	Subjects         []string `json:"subjects,omitempty"` // OpenStates only
	MatterType       string   `json:"matter_type,omitempty"` // Legistar only
	Tracked          bool     `json:"tracked"`
}

// BrowseResult is the paginated response.
type BrowseResult struct {
	Bills      []BrowseBillResult `json:"bills"`
	Page       int                `json:"page"`
	MaxPage    int                `json:"max_page"`
	TotalItems int                `json:"total_items"`
}

// BrowseBills queries the source API for a body and returns available bills.
func BrowseBills(ctx context.Context, pool *pgxpool.Pool, encKey []byte, body store.LegislativeBody, params BrowseParams) (BrowseResult, error) {
	if params.Page < 1 {
		params.Page = 1
	}
	if params.PerPage < 1 || params.PerPage > 200 {
		params.PerPage = 20
	}

	tracked, err := store.TrackedExternalIDs(ctx, pool, body.ID)
	if err != nil {
		return BrowseResult{}, fmt.Errorf("get tracked bills: %w", err)
	}

	switch body.DataSource {
	case "openstates":
		return browseOpenStates(ctx, pool, encKey, body, params, tracked)
	case "legistar":
		return browseLegistar(ctx, body, params, tracked)
	default:
		return BrowseResult{}, fmt.Errorf("browsing not supported for data source %q", body.DataSource)
	}
}

// ---- OpenStates ----

func browseOpenStates(ctx context.Context, pool *pgxpool.Pool, encKey []byte, body store.LegislativeBody, params BrowseParams, tracked map[string]string) (BrowseResult, error) {
	if body.OpenStatesJurisdiction == nil || *body.OpenStatesJurisdiction == "" {
		return BrowseResult{}, fmt.Errorf("body has no openstates_jurisdiction")
	}

	apiKey, err := store.GetServiceConfig(ctx, pool, "openstates", "api_key", encKey)
	if err != nil {
		return BrowseResult{}, fmt.Errorf("read openstates api key: %w", err)
	}

	p := url.Values{
		"jurisdiction": {*body.OpenStatesJurisdiction},
		"per_page":     {strconv.Itoa(params.PerPage)},
		"page":         {strconv.Itoa(params.Page)},
	}
	if body.OpenStatesChamber != nil && *body.OpenStatesChamber != "" {
		p.Set("chamber", *body.OpenStatesChamber)
	} else if params.Chamber != "" {
		p.Set("chamber", params.Chamber)
	}
	if params.Session != "" {
		p.Set("session", params.Session)
	}
	if params.Subject != "" {
		p.Set("subject", params.Subject)
	}
	if params.Query != "" {
		p.Set("q", params.Query)
	}
	if params.Sort != "" {
		p.Set("sort", params.Sort)
	}

	resp, err := fetchOpenStatesBillsPage(ctx, apiKey, p)
	if err != nil {
		return BrowseResult{}, fmt.Errorf("fetch bills: %w", err)
	}

	bills := make([]BrowseBillResult, 0, len(resp.Results))
	for _, ab := range resp.Results {
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
		billID, isTracked := tracked[ab.ID]
		bills = append(bills, BrowseBillResult{
			ExternalID:       ab.ID,
			BillID:           billID,
			Identifier:       ab.Identifier,
			Title:            ab.Title,
			LatestAction:     actionDesc,
			LatestActionDate: actionDate,
			Session:          ab.Session,
			Chamber:          ab.FromOrganization.Classification,
			Link:             link,
			Subjects:         ab.Subjects,
			Tracked:          isTracked,
		})
	}

	maxPage := resp.Pagination.MaxPage
	if maxPage == 0 {
		maxPage = 1
	}

	return BrowseResult{
		Bills:      bills,
		Page:       resp.Pagination.Page,
		MaxPage:    maxPage,
		TotalItems: resp.Pagination.TotalItems,
	}, nil
}

// fetchOpenStatesBillsPage fetches a single page from the OpenStates v3 API.
// Unlike fetchOpenStatesBillsWithParams (which fetches up to 10 pages), this
// returns after one page — no inter-page delay needed for interactive browse.
func fetchOpenStatesBillsPage(ctx context.Context, apiKey string, params url.Values) (openStatesResponse, error) {
	rawURL := "https://v3.openstates.org/bills?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return openStatesResponse{}, err
	}
	req.Header.Set("X-API-KEY", apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return openStatesResponse{}, fmt.Errorf("HTTP GET: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return openStatesResponse{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return openStatesResponse{}, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var osr openStatesResponse
	if err := json.Unmarshal(body, &osr); err != nil {
		return openStatesResponse{}, fmt.Errorf("unmarshal: %w", err)
	}
	return osr, nil
}

// ---- Legistar ----

func browseLegistar(ctx context.Context, body store.LegislativeBody, params BrowseParams, tracked map[string]string) (BrowseResult, error) {
	if body.LegistarClient == nil || *body.LegistarClient == "" {
		return BrowseResult{}, fmt.Errorf("body has no legistar_client")
	}
	legistarClient := *body.LegistarClient

	// Broader date window than sync (90 days vs 30).
	since := time.Now().AddDate(0, 0, -90)
	sinceStr := since.UTC().Format("2006-01-02T15:04:05")
	filterParts := []string{fmt.Sprintf("MatterLastModifiedUtc gt datetime'%s'", sinceStr)}
	if body.LegistarBodyID != nil {
		filterParts = append(filterParts, fmt.Sprintf("MatterBodyId eq %d", *body.LegistarBodyID))
	}
	filterStr := strings.Join(filterParts, " and ")

	top := params.PerPage
	skip := (params.Page - 1) * params.PerPage
	rawURL := fmt.Sprintf("https://webapi.legistar.com/v1/%s/matters?$filter=%s&$top=%d&$skip=%d&$orderby=%s",
		legistarClient, url.QueryEscape(filterStr), top, skip, url.QueryEscape("MatterLastModifiedUtc desc"))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return BrowseResult{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return BrowseResult{}, fmt.Errorf("HTTP GET: %w", err)
	}
	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return BrowseResult{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return BrowseResult{}, fmt.Errorf("status %d: %s", resp.StatusCode, string(respBody))
	}

	var matters []legistarMatter
	if err := json.Unmarshal(respBody, &matters); err != nil {
		return BrowseResult{}, fmt.Errorf("unmarshal: %w", err)
	}

	bills := make([]BrowseBillResult, 0, len(matters))
	for _, m := range matters {
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
			identifier = extID
		}
		legistarURL := fmt.Sprintf("https://%s.legistar.com/gateway.aspx?m=l&id=/matter.aspx?key=%d",
			legistarClient, m.MatterID)

		var actionDate *time.Time
		if m.MatterPassedDate != nil {
			if t := parseLegistarDate(*m.MatterPassedDate); t != nil {
				actionDate = t
			}
		}

		billID, isTracked := tracked[extID]
		bills = append(bills, BrowseBillResult{
			ExternalID:       extID,
			BillID:           billID,
			Identifier:       identifier,
			Title:            title,
			LatestAction:     m.MatterStatusName,
			LatestActionDate: actionDate,
			MatterType:       m.MatterTypeName,
			Link:             legistarURL,
			Tracked:          isTracked,
		})
	}

	// Legistar v1 doesn't return pagination metadata — estimate max page.
	maxPage := params.Page
	if len(bills) == params.PerPage {
		maxPage = params.Page + 1
	}

	return BrowseResult{
		Bills:   bills,
		Page:    params.Page,
		MaxPage: maxPage,
	}, nil
}
