package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"commons/plugin"
)

// HTTPRequestAction implements plugin.ActionType for "core.http_request".
// It makes a generic HTTP request and returns the status code and response body.
// All params support {{key}} references for dynamic URLs, headers, and bodies.
type HTTPRequestAction struct{}

func (a *HTTPRequestAction) ID() string                     { return "core.http_request" }
func (a *HTTPRequestAction) Label() string                  { return "HTTP Request" }
func (a *HTTPRequestAction) RequiredCapabilities() []string { return nil }
func (a *HTTPRequestAction) OutputSchema() []plugin.DataFieldDef {
	return []plugin.DataFieldDef{
		{Key: "http_status", Label: "HTTP Status", Type: "number"},
		{Key: "http_response", Label: "Response Body", Type: "string"},
	}
}
func (a *HTTPRequestAction) ParamSchema() []plugin.ParamDef {
	return []plugin.ParamDef{
		{Key: "method", Label: "Method", Type: "select", Required: true,
			Options: []plugin.SelectOption{
				{Value: "GET", Label: "GET"},
				{Value: "POST", Label: "POST"},
				{Value: "PUT", Label: "PUT"},
				{Value: "PATCH", Label: "PATCH"},
				{Value: "DELETE", Label: "DELETE"},
			}},
		{Key: "url", Label: "URL", Type: "text", Required: true, Dynamic: true,
			Description: "Request URL. Supports {{key}} references."},
		{Key: "headers", Label: "Headers (JSON)", Type: "text", Dynamic: true,
			Description: "JSON object of HTTP headers, e.g. {\"Authorization\": \"Bearer {{token}}\"}."},
		{Key: "body", Label: "Body (JSON)", Type: "text", Dynamic: true,
			Description: "JSON request body for POST/PUT/PATCH. Supports {{key}} references."},
	}
}

func (a *HTTPRequestAction) Execute(ctx context.Context, params map[string]any, _ plugin.ActionContext) (map[string]any, error) {
	method := getString(params, "method")
	url := getString(params, "url")
	headersStr := getString(params, "headers")
	bodyStr := getString(params, "body")

	if method == "" {
		return nil, fmt.Errorf("core.http_request: method is required")
	}
	if url == "" {
		return nil, fmt.Errorf("core.http_request: url is required")
	}

	var bodyReader io.Reader
	if bodyStr != "" {
		bodyReader = bytes.NewReader([]byte(bodyStr))
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("core.http_request: build request: %w", err)
	}

	if headersStr != "" {
		var headers map[string]string
		if err := json.Unmarshal([]byte(headersStr), &headers); err != nil {
			return nil, fmt.Errorf("core.http_request: invalid headers JSON: %w", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
	}
	if bodyStr != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("core.http_request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	return map[string]any{
		"http_status":   resp.StatusCode,
		"http_response": string(respBody),
	}, nil
}
