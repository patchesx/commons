package librarything

import (
	"context"

	"commons/store"
)

// SyncBook attempts to add or update a book in the LT member catalog.
// Returns (false, nil) if write-back is not supported by the API.
//
// NOTE: Whether the LibraryThing API supports write-back is unverified.
// This stub always returns (false, nil) until support is confirmed and implemented.
func SyncBook(_ context.Context, _ string, _ store.Book) (synced bool, err error) {
	return false, nil
}
