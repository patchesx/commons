package legislation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/store"
)

// RefreshSubjects queries the OpenStates API for a jurisdiction, collects unique
// subject values from bill records, and upserts them to the cache table. Returns
// the number of unique subjects cached.
func RefreshSubjects(ctx context.Context, pool *pgxpool.Pool, encKey []byte, body store.LegislativeBody) (int, error) {
	if body.DataSource != "openstates" {
		return 0, fmt.Errorf("RefreshSubjects: body %q is not an OpenStates body", body.Name)
	}
	if body.OpenStatesJurisdiction == nil || *body.OpenStatesJurisdiction == "" {
		return 0, fmt.Errorf("RefreshSubjects: body %q has no openstates_jurisdiction", body.Name)
	}

	apiKey, err := store.GetServiceConfig(ctx, pool, "openstates", "api_key", encKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return 0, fmt.Errorf("RefreshSubjects: openstates api_key not configured")
		}
		return 0, fmt.Errorf("RefreshSubjects: read api key: %w", err)
	}

	params := url.Values{
		"jurisdiction": {*body.OpenStatesJurisdiction},
		"per_page":     {"200"},
	}
	if body.OpenStatesChamber != nil && *body.OpenStatesChamber != "" {
		params.Set("chamber", *body.OpenStatesChamber)
	}

	log.Printf("legislation/subjects: refreshing subjects for body %q", body.Name)

	bills, err := fetchOpenStatesBillsWithParams(ctx, apiKey, params)
	if err != nil {
		return 0, fmt.Errorf("RefreshSubjects: fetch bills: %w", err)
	}

	seen := make(map[string]bool)
	var subjects []string
	for _, b := range bills {
		for _, s := range b.Subjects {
			if s == "" || seen[s] {
				continue
			}
			seen[s] = true
			subjects = append(subjects, s)
		}
	}

	if len(subjects) == 0 {
		log.Printf("legislation/subjects: no subjects found for body %q across %d bills", body.Name, len(bills))
		return 0, nil
	}

	if err := store.UpsertBodySubjects(ctx, pool, body.ID, subjects); err != nil {
		return 0, fmt.Errorf("RefreshSubjects: upsert subjects: %w", err)
	}

	log.Printf("legislation/subjects: cached %d subjects for body %q", len(subjects), body.Name)
	return len(subjects), nil
}
