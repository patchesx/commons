package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"commons/integrations/librarything"
	"commons/internal/httpx"
	"commons/store"
	"commons/web"
)

// ---- response types ----

type bookSummary struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Author          string `json:"author"`
	ISBN            string `json:"isbn,omitempty"`
	CoverURL        string `json:"coverUrl,omitempty"`
	TotalCopies     int    `json:"copiesTotal"`
	AvailableCopies int    `json:"copiesAvailable"`
	HoldsCount      int    `json:"holdsCount"`
}

type bookDetail struct {
	ID          string       `json:"id"`
	Title       string       `json:"title"`
	Author      string       `json:"author"`
	ISBN        string       `json:"isbn,omitempty"`
	CoverURL    string       `json:"coverUrl,omitempty"`
	Description string       `json:"description,omitempty"`
	LTWorkID    string       `json:"ltWorkId,omitempty"`
	Copies      []copyDetail `json:"copies"`
	HoldsCount  int          `json:"holdsCount"`
}

type copyDetail struct {
	ID             string `json:"id"`
	Barcode        string `json:"barcode,omitempty"`
	Condition      string `json:"condition"`
	LoanPeriodDays *int   `json:"loanPeriodDays"`
	IsActive       bool   `json:"isActive"`
	IsAvailable    bool   `json:"isAvailable"`
}

type checkoutResponse struct {
	ID           string `json:"id"`
	CopyID       string `json:"copyId"`
	CopyBarcode  string `json:"copyBarcode,omitempty"`
	BookID       string `json:"bookId"`
	BookTitle    string `json:"bookTitle"`
	UserID       string `json:"userId"`
	UserName     string `json:"userName"`
	Status       string `json:"status"`
	DueDate      string `json:"dueDate,omitempty"`
	CheckedOutAt string `json:"checkedOutAt,omitempty"`
	ReturnedAt   string `json:"returnedAt,omitempty"`
	Notes        string `json:"notes,omitempty"`
	RequestedAt  string `json:"requestedAt"`
}

type holdResponse struct {
	ID          string `json:"id"`
	BookID      string `json:"bookId"`
	BookTitle   string `json:"bookTitle"`
	UserID      string `json:"userId"`
	UserName    string `json:"userName"`
	Position    int    `json:"position"`
	RequestedAt string `json:"requestedAt"`
	NotifiedAt  string `json:"notifiedAt,omitempty"`
}

func toCheckoutResponse(c store.Checkout) checkoutResponse {
	r := checkoutResponse{
		ID:          c.ID,
		CopyID:      c.CopyID,
		BookID:      c.BookID,
		BookTitle:   c.BookTitle,
		UserID:      c.UserID,
		UserName:    c.UserName,
		Status:      c.Status,
		RequestedAt: c.RequestedAt.Format(time.RFC3339),
	}
	if c.CopyBarcode != nil {
		r.CopyBarcode = *c.CopyBarcode
	}
	if c.DueDate != nil {
		r.DueDate = c.DueDate.Format("2006-01-02")
	}
	if c.CheckedOutAt != nil {
		r.CheckedOutAt = c.CheckedOutAt.Format(time.RFC3339)
	}
	if c.ReturnedAt != nil {
		r.ReturnedAt = c.ReturnedAt.Format(time.RFC3339)
	}
	if c.Notes != nil {
		r.Notes = *c.Notes
	}
	return r
}

func toHoldResponse(h store.Hold) holdResponse {
	r := holdResponse{
		ID:          h.ID,
		BookID:      h.BookID,
		BookTitle:   h.BookTitle,
		UserID:      h.UserID,
		UserName:    h.UserName,
		Position:    h.Position,
		RequestedAt: h.RequestedAt.Format(time.RFC3339),
	}
	if h.NotifiedAt != nil {
		r.NotifiedAt = h.NotifiedAt.Format(time.RFC3339)
	}
	return r
}

// ---- Books ----

// ListLibraryBooks handles GET /api/library/books.
// Accepts ?search=, ?page=, ?per_page= query params.
func ListLibraryBooks(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		search := r.URL.Query().Get("search")
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}
		perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		if perPage < 1 || perPage > 100 {
			perPage = 50
		}

		books, total, err := store.ListBooks(ctx, pool, search, page, perPage)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list books")
			return
		}

		summaries := make([]bookSummary, len(books))
		for i, b := range books {
			s := bookSummary{
				ID:     b.ID,
				Title:  b.Title,
				Author: b.Author,
			}
			if b.ISBN != nil {
				s.ISBN = *b.ISBN
			}
			if b.CoverURL != nil {
				s.CoverURL = *b.CoverURL
			}
			// Fetch per-book availability counts.
			detail, err := store.GetBook(ctx, pool, b.ID)
			if err == nil && detail != nil {
				s.TotalCopies = detail.TotalCopies
				s.AvailableCopies = detail.AvailableCopies
			}
			holds, err := store.ListHoldsForBook(ctx, pool, b.ID)
			if err == nil {
				s.HoldsCount = len(holds)
			}
			summaries[i] = s
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"books": summaries,
			"total": total,
			"page":  page,
		})
	}
}

// GetLibraryBook handles GET /api/library/books/{id}.
func GetLibraryBook(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		d, err := store.GetBook(r.Context(), pool, id)
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "book not found")
			return
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to get book")
			return
		}

		out := bookDetail{
			ID:     d.ID,
			Title:  d.Title,
			Author: d.Author,
		}
		if d.ISBN != nil {
			out.ISBN = *d.ISBN
		}
		if d.CoverURL != nil {
			out.CoverURL = *d.CoverURL
		}
		if d.Description != nil {
			out.Description = *d.Description
		}
		if d.LTWorkID != nil {
			out.LTWorkID = *d.LTWorkID
		}

		out.Copies = make([]copyDetail, len(d.Copies))
		for i, c := range d.Copies {
			cd := copyDetail{
				ID:             c.ID,
				Condition:      c.Condition,
				LoanPeriodDays: c.LoanPeriodDays,
				IsActive:       c.IsActive,
				IsAvailable:    c.IsAvailable,
			}
			if c.Barcode != nil {
				cd.Barcode = *c.Barcode
			}
			out.Copies[i] = cd
		}

		holds, _ := store.ListHoldsForBook(r.Context(), pool, id)
		out.HoldsCount = len(holds)

		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

// CreateLibraryBook handles POST /api/library/books.
// If isbn is provided and title/author are empty, performs ISBN lookup first.
// Creates `copies` copies (default 1) with default condition.
func CreateLibraryBook(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		adminID := web.UserIDFromContext(ctx)

		var body struct {
			Title       string `json:"title"`
			Author      string `json:"author"`
			ISBN        string `json:"isbn"`
			CoverURL    string `json:"coverUrl"`
			Description string `json:"description"`
			LTWorkID    string `json:"ltWorkId"`
			Copies      int    `json:"copies"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.Copies < 1 {
			body.Copies = 1
		}

		// Auto ISBN lookup when title/author not provided.
		if body.ISBN != "" && (body.Title == "" || body.Author == "") {
			apiKey, _ := store.GetServiceConfig(ctx, pool, "librarything", "api_key", encKey)
			meta, err := librarything.LookupISBN(ctx, body.ISBN, apiKey)
			if err != nil {
				log.Printf("api: ISBN lookup failed: %v", err)
			} else if meta != nil {
				if body.Title == "" {
					body.Title = meta.Title
				}
				if body.Author == "" {
					body.Author = meta.Author
				}
				if body.CoverURL == "" {
					body.CoverURL = meta.CoverURL
				}
				if body.Description == "" {
					body.Description = meta.Description
				}
				if body.LTWorkID == "" {
					body.LTWorkID = meta.LTWorkID
				}
			}
		}

		if body.Title == "" || body.Author == "" {
			httpx.WriteError(w, http.StatusBadRequest, "title and author are required")
			return
		}

		b := store.Book{
			Title:  body.Title,
			Author: body.Author,
		}
		if body.ISBN != "" {
			b.ISBN = &body.ISBN
		}
		if body.CoverURL != "" {
			b.CoverURL = &body.CoverURL
		}
		if body.Description != "" {
			b.Description = &body.Description
		}
		if body.LTWorkID != "" {
			b.LTWorkID = &body.LTWorkID
		}

		created, err := store.CreateBook(ctx, pool, b, adminID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create book")
			return
		}

		for i := 0; i < body.Copies; i++ {
			if _, err := store.CreateCopy(ctx, pool, store.Copy{BookID: created.ID, Condition: "good"}); err != nil {
				log.Printf("api: warning: failed to create copy %d for book %s: %v", i+1, created.ID, err)
			}
		}

		httpx.WriteJSON(w, http.StatusCreated, map[string]string{"id": created.ID})
	}
}

// UpdateLibraryBook handles PUT /api/library/books/{id}.
func UpdateLibraryBook(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		var body struct {
			Title       string `json:"title"`
			Author      string `json:"author"`
			ISBN        string `json:"isbn"`
			CoverURL    string `json:"coverUrl"`
			Description string `json:"description"`
			LTWorkID    string `json:"ltWorkId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.Title == "" || body.Author == "" {
			httpx.WriteError(w, http.StatusBadRequest, "title and author are required")
			return
		}

		b := store.Book{Title: body.Title, Author: body.Author}
		if body.ISBN != "" {
			b.ISBN = &body.ISBN
		}
		if body.CoverURL != "" {
			b.CoverURL = &body.CoverURL
		}
		if body.Description != "" {
			b.Description = &body.Description
		}
		if body.LTWorkID != "" {
			b.LTWorkID = &body.LTWorkID
		}

		if err := store.UpdateBook(r.Context(), pool, id, b); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update book")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// DeleteLibraryBook handles DELETE /api/library/books/{id}.
func DeleteLibraryBook(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := store.DeleteBook(r.Context(), pool, id); err != nil {
			if strings.Contains(err.Error(), "cannot delete") {
				httpx.WriteError(w, http.StatusConflict, err.Error())
				return
			}
			httpx.WriteError(w, http.StatusInternalServerError, "failed to delete book")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// LookupISBN handles GET /api/library/books/isbn/{isbn}.
// Returns metadata from LibraryThing or Open Library without creating a record.
func LookupISBN(pool *pgxpool.Pool, encKey []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		isbn := r.PathValue("isbn")
		ctx := r.Context()

		apiKey, _ := store.GetServiceConfig(ctx, pool, "librarything", "api_key", encKey)
		meta, err := librarything.LookupISBN(ctx, isbn, apiKey)
		if err != nil {
			httpx.WriteError(w, http.StatusBadGateway, "ISBN lookup failed: "+err.Error())
			return
		}
		if meta == nil {
			httpx.WriteError(w, http.StatusNotFound, "ISBN not found")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, meta)
	}
}

// ImportLibraryBooks handles POST /api/library/books/import.
// Expects multipart/form-data with a `file` field containing a LibraryThing CSV export.
func ImportLibraryBooks(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		adminID := web.UserIDFromContext(ctx)

		if err := r.ParseMultipartForm(10 << 20); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "failed to parse form")
			return
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "file field is required")
			return
		}
		defer file.Close()

		imported, skipped, importErrors, err := importLibraryCSV(ctx, pool, file, adminID)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "CSV parse error: "+err.Error())
			return
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"imported": imported,
			"skipped":  skipped,
			"errors":   importErrors,
		})
	}
}

// importLibraryCSV parses a LibraryThing CSV export and imports books + one copy each.
func importLibraryCSV(ctx context.Context, pool *pgxpool.Pool, r io.Reader, adminID string) (imported, skipped int, errs []string, err error) {
	reader := csv.NewReader(r)
	reader.LazyQuotes = true

	headers, err := reader.Read()
	if err != nil {
		return 0, 0, nil, fmt.Errorf("read header row: %w", err)
	}
	idx := make(map[string]int)
	for i, h := range headers {
		idx[strings.TrimSpace(h)] = i
	}

	col := func(row []string, name string) string {
		i, ok := idx[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	for {
		row, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			errs = append(errs, fmt.Sprintf("row read error: %v", err))
			skipped++
			continue
		}

		title := col(row, "Title")
		author := col(row, "Primary Author")
		if title == "" || author == "" {
			errs = append(errs, fmt.Sprintf("skipped row: missing title or author"))
			skipped++
			continue
		}

		isbn := col(row, "ISBN")
		ltWorkID := col(row, "Book Id")
		description := col(row, "Comments")

		b := store.Book{Title: title, Author: author}
		if isbn != "" {
			b.ISBN = &isbn
		}
		if ltWorkID != "" {
			b.LTWorkID = &ltWorkID
		}
		if description != "" {
			b.Description = &description
		}

		created, err := store.CreateBook(ctx, pool, b, adminID)
		if err != nil {
			// Unique constraint on ISBN or lt_work_id = duplicate; skip.
			errs = append(errs, fmt.Sprintf("skipped %q: %v", title, err))
			skipped++
			continue
		}

		if _, err := store.CreateCopy(ctx, pool, store.Copy{BookID: created.ID, Condition: "good"}); err != nil {
			log.Printf("api: warning: failed to create copy for imported book %s: %v", created.ID, err)
		}
		imported++
	}
	return imported, skipped, errs, nil
}

// ---- Copies ----

// ListLibraryCopies handles GET /api/library/books/{id}/copies.
func ListLibraryCopies(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bookID := r.PathValue("id")
		copies, err := store.ListCopiesForBook(r.Context(), pool, bookID)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list copies")
			return
		}
		out := make([]copyDetail, len(copies))
		for i, c := range copies {
			cd := copyDetail{
				ID:             c.ID,
				Condition:      c.Condition,
				LoanPeriodDays: c.LoanPeriodDays,
				IsActive:       c.IsActive,
				IsAvailable:    c.IsAvailable,
			}
			if c.Barcode != nil {
				cd.Barcode = *c.Barcode
			}
			out[i] = cd
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

// CreateLibraryCopy handles POST /api/library/books/{id}/copies.
func CreateLibraryCopy(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bookID := r.PathValue("id")

		var body struct {
			Barcode        string `json:"barcode"`
			Condition      string `json:"condition"`
			Notes          string `json:"notes"`
			LoanPeriodDays *int   `json:"loanPeriodDays"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.Condition == "" {
			body.Condition = "good"
		}

		c := store.Copy{BookID: bookID, Condition: body.Condition, LoanPeriodDays: body.LoanPeriodDays}
		if body.Barcode != "" {
			c.Barcode = &body.Barcode
		}
		if body.Notes != "" {
			c.Notes = &body.Notes
		}

		created, err := store.CreateCopy(r.Context(), pool, c)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to create copy")
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, map[string]string{"id": created.ID})
	}
}

// UpdateLibraryCopy handles PUT /api/library/copies/{id}.
func UpdateLibraryCopy(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		var body struct {
			Barcode        string `json:"barcode"`
			Condition      string `json:"condition"`
			Notes          string `json:"notes"`
			LoanPeriodDays *int   `json:"loanPeriodDays"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		c := store.Copy{Condition: body.Condition, LoanPeriodDays: body.LoanPeriodDays}
		if body.Barcode != "" {
			c.Barcode = &body.Barcode
		}
		if body.Notes != "" {
			c.Notes = &body.Notes
		}

		if err := store.UpdateCopy(r.Context(), pool, id, c); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to update copy")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// DeactivateLibraryCopy handles DELETE /api/library/copies/{id}.
func DeactivateLibraryCopy(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := store.DeactivateCopy(r.Context(), pool, id); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to deactivate copy")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ---- Checkouts ----

// ListLibraryCheckouts handles GET /api/library/checkouts.
// Accepts ?status=, ?page=, and ?userId=me query params.
// When ?userId=me is set, returns the current user's active/pending checkouts.
func ListLibraryCheckouts(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userIDParam := r.URL.Query().Get("userId")

		// Member-scoped view: return only the current user's checkouts.
		if userIDParam == "me" {
			userID := web.UserIDFromContext(ctx)
			checkouts, err := store.ListCheckoutsForUser(ctx, pool, userID)
			if err != nil {
				httpx.WriteError(w, http.StatusInternalServerError, "failed to list checkouts")
				return
			}
			out := make([]checkoutResponse, len(checkouts))
			for i, c := range checkouts {
				out[i] = toCheckoutResponse(c)
			}
			httpx.WriteJSON(w, http.StatusOK, out)
			return
		}

		// Admin view: paginated, filterable by status.
		status := r.URL.Query().Get("status")
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if page < 1 {
			page = 1
		}

		checkouts, total, err := store.ListCheckouts(ctx, pool, status, page, 50)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list checkouts")
			return
		}

		out := make([]checkoutResponse, len(checkouts))
		for i, c := range checkouts {
			out[i] = toCheckoutResponse(c)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"checkouts": out,
			"total":     total,
			"page":      page,
		})
	}
}

// RequestLibraryCheckout handles POST /api/library/checkouts.
// Creates a checkout request for the current user. Body: {"book_id": "..."}
// Returns 409 if no copies are available, the user has reached their limit,
// or the user already has an active checkout for this book.
func RequestLibraryCheckout(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		userID := web.UserIDFromContext(ctx)

		var body struct {
			BookID string `json:"bookId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if body.BookID == "" {
			httpx.WriteError(w, http.StatusBadRequest, "book_id is required")
			return
		}

		checkout, err := store.RequestCheckout(ctx, pool, body.BookID, userID)
		if err != nil {
			switch err {
			case store.ErrNoAvailableCopies:
				httpx.WriteError(w, http.StatusConflict, "no available copies — consider placing a hold")
			case store.ErrMaxCheckoutsReached:
				httpx.WriteError(w, http.StatusConflict, "you have reached your maximum checkout limit")
			case store.ErrAlreadyHasCheckout:
				httpx.WriteError(w, http.StatusConflict, "you already have an active checkout for this book")
			default:
				httpx.WriteError(w, http.StatusInternalServerError, "failed to request checkout")
			}
			return
		}
		httpx.WriteJSON(w, http.StatusCreated, toCheckoutResponse(*checkout))
	}
}

// ApproveLibraryCheckout handles PUT /api/library/checkouts/{id}/approve.
// Body: {"due_date": "2026-04-01"} (optional — empty means no due date)
func ApproveLibraryCheckout(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		adminID := web.UserIDFromContext(ctx)

		_, err := store.GetCheckoutByID(ctx, pool, id)
		if errors.Is(err, store.ErrNotFound) {
			httpx.WriteError(w, http.StatusNotFound, "checkout not found")
			return
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to get checkout")
			return
		}

		var body struct {
			DueDate string `json:"dueDate"`
		}
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck

		var dueDate time.Time
		if body.DueDate != "" {
			var parseErr error
			dueDate, parseErr = time.Parse("2006-01-02", body.DueDate)
			if parseErr != nil {
				httpx.WriteError(w, http.StatusBadRequest, "due_date must be YYYY-MM-DD")
				return
			}
		}

		if err := store.ApproveCheckout(ctx, pool, id, adminID, dueDate); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to approve checkout")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// DenyLibraryCheckout handles PUT /api/library/checkouts/{id}/deny.
func DenyLibraryCheckout(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		adminID := web.UserIDFromContext(ctx)

		var body struct {
			Notes string `json:"notes"`
		}
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck

		if err := store.DenyCheckout(ctx, pool, id, adminID, body.Notes); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to deny checkout")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ReturnLibraryCheckout handles PUT /api/library/checkouts/{id}/return.
func ReturnLibraryCheckout(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := r.PathValue("id")
		adminID := web.UserIDFromContext(ctx)

		if err := store.MarkReturned(ctx, pool, id, adminID); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to mark returned")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ExtendLibraryCheckout handles PUT /api/library/checkouts/{id}/extend.
// Body: {"due_date": "2026-04-15"}
func ExtendLibraryCheckout(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")

		var body struct {
			DueDate string `json:"dueDate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.DueDate == "" {
			httpx.WriteError(w, http.StatusBadRequest, "due_date is required")
			return
		}
		newDate, err := time.Parse("2006-01-02", body.DueDate)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "due_date must be YYYY-MM-DD")
			return
		}

		if err := store.ExtendDueDate(r.Context(), pool, id, newDate); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to extend due date")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// ---- Holds ----

// ListLibraryHolds handles GET /api/library/holds.
// Accepts ?book_id= or ?user_id= query params.
func ListLibraryHolds(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		bookID := r.URL.Query().Get("book_id")
		userID := r.URL.Query().Get("user_id")

		var holds []store.Hold
		var err error
		switch {
		case bookID != "":
			holds, err = store.ListHoldsForBook(ctx, pool, bookID)
		case userID != "":
			holds, err = store.ListHoldsForUser(ctx, pool, userID)
		default:
			holds, err = store.ListAllActiveHolds(ctx, pool)
		}
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to list holds")
			return
		}

		out := make([]holdResponse, len(holds))
		for i, h := range holds {
			out[i] = toHoldResponse(h)
		}
		httpx.WriteJSON(w, http.StatusOK, out)
	}
}

// CancelLibraryHold handles PUT /api/library/holds/{id}/cancel.
func CancelLibraryHold(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := store.CancelHold(r.Context(), pool, id); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to cancel hold")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// NotifyLibraryHold handles PUT /api/library/holds/{id}/notify.
// Marks the hold as notified. (Actual DM is handled by the notification layer in Phase 6.)
func NotifyLibraryHold(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := store.MarkHoldNotified(r.Context(), pool, id); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "failed to mark hold notified")
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
