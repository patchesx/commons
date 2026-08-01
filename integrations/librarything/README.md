# LibraryThing Integration

> **Status: Support library, not a registered plugin.** This package backs the library feature, which is currently disabled (broken — its tests carry `//go:build ignore`). It has no `plugin.go` and does not appear on the Integrations page; it is called directly by the library handlers.

ISBN metadata enrichment for the library feature. Looks up book information (title, author, cover image) by ISBN when adding books to the resource library. Falls back to Open Library if no LibraryThing API key is configured.

---

## What it does

- **ISBN lookup** — given an ISBN, returns title, primary author, cover image URL, and description excerpts
- **LibraryThing API** — used when an API key is configured; returns richer metadata
- **Open Library fallback** — used when no API key is set; free, no account required, slightly less complete metadata

---

## Setup

### Option A: Open Library only (no account required)

No configuration needed. Open Library lookups work out of the box with no API key. Metadata quality is generally good for widely-published books.

### Option B: LibraryThing API

1. Create an account at [librarything.com](https://www.librarything.com)
2. Apply for a developer API key at [librarything.com/services/keys](https://www.librarything.com/services/keys)
3. Go to **Integrations** in the admin panel and set:

| Field | Value |
|---|---|
| LibraryThing API Key | Your developer key |

---

## Usage

The integration is called automatically by the library admin interface when an admin enters an ISBN while adding or editing a book. There is no manual trigger — metadata is fetched inline during book creation.

---

## Current Limitations

- **Write-back is not implemented.** The `SyncBook()` function exists as a stub but always returns no-op. Library records are not pushed back to LibraryThing — the integration is read-only.
- **No bulk import.** There is no way to import an existing LibraryThing library in bulk; books must be added one at a time.
- **Cover images are URLs.** Cover images are stored as external URLs (from LibraryThing or Open Library CDN), not downloaded locally.

---

## Config Keys Reference

| Service | Key | Sensitive | Description |
|---|---|---|---|
| `librarything` | `api_key` | No | LibraryThing developer API key (optional — falls back to Open Library if not set) |

---

## Architecture Notes

- The lookup functions are called synchronously from the library admin handlers during book creation/edit.
- Both lookup paths return the same `BookMetadata` struct so callers don't need to care which source was used.
- Open Library is queried via `openlibrary.org/isbn/{isbn}.json` and returns a subset of fields compared to LibraryThing.
