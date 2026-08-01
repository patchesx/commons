// Package librarything provides ISBN metadata lookup via the LibraryThing API,
// with fallback to the Open Library API when no API key is configured.
package librarything

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// BookMetadata holds enriched book data returned from an ISBN lookup.
type BookMetadata struct {
	Title       string
	Author      string
	CoverURL    string
	Description string
	LTWorkID    string
}

// LookupISBN fetches book metadata for the given ISBN.
// When apiKey is non-empty, uses the LibraryThing API.
// When apiKey is empty, falls back to the Open Library API (no key required).
// Returns nil with no error if the ISBN is not found.
func LookupISBN(ctx context.Context, isbn, apiKey string) (*BookMetadata, error) {
	if apiKey != "" {
		return lookupLT(ctx, isbn, apiKey)
	}
	return lookupOpenLibrary(ctx, isbn)
}

// lookupLT queries the LibraryThing REST API for a work by ISBN.
func lookupLT(ctx context.Context, isbn, apiKey string) (*BookMetadata, error) {
	endpoint := fmt.Sprintf(
		"http://www.librarything.com/services/rest/1.1/?method=librarything.ck.getwork&isbn=%s&apikey=%s&response=json",
		url.QueryEscape(isbn), url.QueryEscape(apiKey),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build LT request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LT request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LT API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read LT response: %w", err)
	}

	// LT response shape: {"stat":"ok","result":{"works":[{...}]}}
	var ltResp struct {
		Stat   string `json:"stat"`
		Result struct {
			Works []struct {
				WorkID      json.Number `json:"work_id"`
				Title       string      `json:"title"`
				AuthorFL    string      `json:"author_fl"`
				Img         string      `json:"img"`
				Description string      `json:"description"`
			} `json:"works"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &ltResp); err != nil {
		return nil, fmt.Errorf("parse LT response: %w", err)
	}

	if ltResp.Stat != "ok" || len(ltResp.Result.Works) == 0 {
		return nil, nil
	}

	w := ltResp.Result.Works[0]
	return &BookMetadata{
		Title:       w.Title,
		Author:      w.AuthorFL,
		CoverURL:    w.Img,
		Description: w.Description,
		LTWorkID:    w.WorkID.String(),
	}, nil
}

// lookupOpenLibrary queries the Open Library Books API for an ISBN.
func lookupOpenLibrary(ctx context.Context, isbn string) (*BookMetadata, error) {
	endpoint := fmt.Sprintf(
		"https://openlibrary.org/api/books?bibkeys=ISBN:%s&format=json&jscmd=data",
		url.QueryEscape(isbn),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build OL request: %w", err)
	}
	req.Header.Set("User-Agent", "commons/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OL request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Open Library API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read OL response: %w", err)
	}

	// OL response shape: {"ISBN:XXXXXXXXX": {"title": "...", "authors": [...], "cover": {...}, ...}}
	var olResp map[string]struct {
		Title   string `json:"title"`
		Authors []struct {
			Name string `json:"name"`
		} `json:"authors"`
		Cover struct {
			Medium string `json:"medium"`
		} `json:"cover"`
		Excerpts []struct {
			Text string `json:"text"`
		} `json:"excerpts"`
	}
	if err := json.Unmarshal(body, &olResp); err != nil {
		return nil, fmt.Errorf("parse OL response: %w", err)
	}

	key := "ISBN:" + isbn
	entry, ok := olResp[key]
	if !ok {
		return nil, nil
	}

	m := &BookMetadata{
		Title: entry.Title,
	}
	if len(entry.Authors) > 0 {
		m.Author = entry.Authors[0].Name
	}
	m.CoverURL = entry.Cover.Medium
	if len(entry.Excerpts) > 0 {
		m.Description = entry.Excerpts[0].Text
	}
	return m, nil
}
