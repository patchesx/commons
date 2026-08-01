package store

import "errors"

// ErrNotFound is returned when a required record is absent from the database.
//
// Convention: every single-row Get* function returns ErrNotFound when no row
// matches. Callers check with errors.Is(err, store.ErrNotFound) — never compare
// against pgx.ErrNoRows, which is an implementation detail of this package.
var ErrNotFound = errors.New("record not found")

// ErrAlreadyExists is returned when a record with the same unique key already exists.
var ErrAlreadyExists = errors.New("already exists")
