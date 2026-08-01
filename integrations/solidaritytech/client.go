package solidaritytech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

var baseURL = "https://api.solidarity.tech/v1"

// ErrNotConfigured is returned when the SolidarityTech API key has not been set.
var ErrNotConfigured = errors.New("solidaritytech: api_key is not configured — set it on the Integrations page")

// ErrUserNotFound is returned when no SolidarityTech user matches a lookup.
var ErrUserNotFound = errors.New("solidaritytech: user not found")

// Client is a minimal client for the SolidarityTech API.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// NewClient reads the SolidarityTech API key from config_store and returns a ready Client.
func NewClient(ctx context.Context, pool *pgxpool.Pool, encKey []byte) (*Client, error) {
	apiKey, err := store.GetServiceConfig(ctx, pool, "solidaritytech", "api_key", encKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrNotConfigured
		}
		return nil, fmt.Errorf("solidaritytech: read api_key: %w", err)
	}
	if apiKey == "" {
		return nil, ErrNotConfigured
	}
	return &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// User is a SolidarityTech user profile (subset of fields relevant to this integration).
type User struct {
	ID                   int            `json:"id"`
	HashID               string         `json:"hash_id"`
	Email                string         `json:"email"`
	FirstName            string         `json:"first_name"`
	LastName             string         `json:"last_name"`
	AlternateName        string         `json:"alternate_name"`
	PhoneNumber          string         `json:"phone_number"`
	PreferredLanguage    string         `json:"preferred_language"`
	CustomUserProperties map[string]any `json:"custom_user_properties"`
}

// CustomUserProperty is a SolidarityTech custom user property definition.
type CustomUserProperty struct {
	ID        int              `json:"id"`
	Name      string           `json:"name"`
	Key       string           `json:"key"`
	FieldType string           `json:"field_type"`
	Options   []PropertyOption `json:"options"`
	ScopeID   int              `json:"scope_id"`
	ScopeType string           `json:"scope_type"`
}

// PropertyOption is a selectable option for a custom user property.
type PropertyOption struct {
	Label map[string]string `json:"label"`
	Value string            `json:"value"`
}

// GetUserByEmail looks up a single user by email address (case-insensitive on the ST side).
// Returns ErrUserNotFound when no user matches.
func (c *Client) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	q := url.Values{}
	q.Set("email", email)
	q.Set("_limit", "1")

	var wrapper struct {
		Data []User `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/users?"+q.Encode(), nil, &wrapper); err != nil {
		return nil, err
	}
	if len(wrapper.Data) == 0 {
		return nil, ErrUserNotFound
	}
	return &wrapper.Data[0], nil
}

// GetUserByID retrieves a single user by SolidarityTech user id.
func (c *Client) GetUserByID(ctx context.Context, id int) (*User, error) {
	var u User
	if err := c.do(ctx, http.MethodGet, "/users/"+strconv.Itoa(id), nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// UpdateUserCustomProperties sets custom property values on a user via PUT /users/{id}.
// props is keyed by the property's internal_name. When append is true, Multiple-Checkboxes
// values are merged with existing values; when false, they replace existing values.
func (c *Client) UpdateUserCustomProperties(ctx context.Context, id int, props map[string]any, append bool) error {
	body := struct {
		CustomUserProperties       map[string]any `json:"custom_user_properties,omitempty"`
		AppendCustomUserProperties bool           `json:"append_custom_user_properties"`
	}{
		CustomUserProperties:       props,
		AppendCustomUserProperties: append,
	}
	return c.do(ctx, http.MethodPut, "/users/"+strconv.Itoa(id), body, nil)
}

// ListCustomUserProperties returns every custom user property definition, paginating
// through all results.
func (c *Client) ListCustomUserProperties(ctx context.Context) ([]CustomUserProperty, error) {
	var all []CustomUserProperty
	offset := 0
	const limit = 100
	for {
		q := url.Values{}
		q.Set("_limit", strconv.Itoa(limit))
		q.Set("_offset", strconv.Itoa(offset))
		var wrapper struct {
			Data []CustomUserProperty `json:"data"`
			Meta struct {
				TotalCount int `json:"total_count"`
			} `json:"meta"`
		}
		if err := c.do(ctx, http.MethodGet, "/custom_user_properties?"+q.Encode(), nil, &wrapper); err != nil {
			return nil, err
		}
		all = append(all, wrapper.Data...)
		offset += len(wrapper.Data)
		if len(wrapper.Data) < limit || offset >= wrapper.Meta.TotalCount {
			break
		}
	}
	return all, nil
}

// do executes an API request. out may be nil to ignore the response body.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("solidaritytech: marshal body: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return fmt.Errorf("solidaritytech: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("solidaritytech: request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("solidaritytech: rate limited (429) on %s %s — retry later", method, path)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("solidaritytech: %s %s: HTTP %d: %s",
			method, path, resp.StatusCode, truncate(string(respBody), 500))
	}

	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("solidaritytech: decode response: %w", err)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
