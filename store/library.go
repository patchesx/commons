package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoAvailableCopies is returned when all copies of a book are checked out.
var ErrNoAvailableCopies = errors.New("no available copies")

// ErrMaxCheckoutsReached is returned when a user is at their checkout limit.
var ErrMaxCheckoutsReached = errors.New("max active checkouts reached")

// ErrAlreadyHasCheckout is returned when a user already has an active or pending checkout for a book.
var ErrAlreadyHasCheckout = errors.New("user already has an active checkout for this book")

// ErrAlreadyHasHold is returned when a user already has a hold on a book.
var ErrAlreadyHasHold = errors.New("user already has a hold on this book")

// Book represents a bibliographic record.
type Book struct {
	ID          string
	Title       string
	Author      string
	ISBN        *string
	CoverURL    *string
	Description *string
	LTWorkID    *string
	AddedBy     *string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Copy represents a physical copy of a book.
type Copy struct {
	ID             string
	BookID         string
	Barcode        *string
	Condition      string
	Notes          *string
	LoanPeriodDays *int
	IsActive       bool
	IsAvailable    bool // computed: active + not in pending/active checkout
	CreatedAt      time.Time
}

// Checkout is a flat read struct with joined book title, user name, and copy barcode.
type Checkout struct {
	ID           string
	CopyID       string
	CopyBarcode  *string
	BookID       string
	BookTitle    string
	UserID       string
	UserName     string
	Status       string
	DueDate      *time.Time
	CheckedOutAt *time.Time
	ReturnedAt   *time.Time
	ApprovedBy   *string
	ReturnedBy   *string
	Notes        *string
	RequestedAt  time.Time
}

// Hold is a flat read struct with joined book title and user name.
type Hold struct {
	ID          string
	BookID      string
	BookTitle   string
	UserID      string
	UserName    string
	Position    int
	RequestedAt time.Time
	NotifiedAt  *time.Time
	FulfilledAt *time.Time
	CancelledAt *time.Time
}

// BookDetail contains a book record, its copies, and availability counts.
type BookDetail struct {
	Book
	Copies          []Copy
	TotalCopies     int
	AvailableCopies int
}

// ---- Books ----

// ListBooks returns a paginated list of books with optional full-text search on title/author.
// Returns the slice, total count, and error.
func ListBooks(ctx context.Context, pool *pgxpool.Pool, search string, page, perPage int) ([]Book, int, error) {
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * perPage

	args := []any{}
	where := "WHERE 1=1"
	if search != "" {
		args = append(args, "%"+search+"%")
		idx := fmt.Sprintf("$%d", len(args))
		where += " AND (title ILIKE " + idx + " OR author ILIKE " + idx + ")"
	}

	var total int
	if err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM library.books "+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, perPage, offset)
	limitIdx := fmt.Sprintf("$%d", len(args)-1)
	offsetIdx := fmt.Sprintf("$%d", len(args))

	rows, err := pool.Query(ctx, `
		SELECT id, title, author, isbn, cover_url, description, lt_work_id,
		       added_by, created_at, updated_at
		FROM library.books
		`+where+`
		ORDER BY title ASC
		LIMIT `+limitIdx+` OFFSET `+offsetIdx,
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	books := make([]Book, 0)
	for rows.Next() {
		var b Book
		if err := rows.Scan(
			&b.ID, &b.Title, &b.Author, &b.ISBN, &b.CoverURL, &b.Description,
			&b.LTWorkID, &b.AddedBy, &b.CreatedAt, &b.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}
		books = append(books, b)
	}
	return books, total, rows.Err()
}

// GetBook returns a book with its copies and availability counts.
// Returns ErrNotFound if absent.
func GetBook(ctx context.Context, pool *pgxpool.Pool, id string) (*BookDetail, error) {
	var d BookDetail
	err := pool.QueryRow(ctx, `
		SELECT id, title, author, isbn, cover_url, description, lt_work_id,
		       added_by, created_at, updated_at
		FROM library.books WHERE id = $1
	`, id).Scan(
		&d.ID, &d.Title, &d.Author, &d.ISBN, &d.CoverURL, &d.Description,
		&d.LTWorkID, &d.AddedBy, &d.CreatedAt, &d.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	copies, err := ListCopiesForBook(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	d.Copies = copies
	d.TotalCopies = len(copies)
	for _, c := range copies {
		if c.IsAvailable {
			d.AvailableCopies++
		}
	}
	return &d, nil
}

// CreateBook inserts a new book and returns the created record.
func CreateBook(ctx context.Context, pool *pgxpool.Pool, b Book, adminID string) (*Book, error) {
	out := &Book{}
	err := pool.QueryRow(ctx, `
		INSERT INTO library.books (title, author, isbn, cover_url, description, lt_work_id, added_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, title, author, isbn, cover_url, description, lt_work_id,
		          added_by, created_at, updated_at
	`, b.Title, b.Author, b.ISBN, b.CoverURL, b.Description, b.LTWorkID, adminID).Scan(
		&out.ID, &out.Title, &out.Author, &out.ISBN, &out.CoverURL, &out.Description,
		&out.LTWorkID, &out.AddedBy, &out.CreatedAt, &out.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateBook updates a book's editable fields.
func UpdateBook(ctx context.Context, pool *pgxpool.Pool, id string, b Book) error {
	_, err := pool.Exec(ctx, `
		UPDATE library.books
		SET title = $1, author = $2, isbn = $3, cover_url = $4,
		    description = $5, lt_work_id = $6, updated_at = NOW()
		WHERE id = $7
	`, b.Title, b.Author, b.ISBN, b.CoverURL, b.Description, b.LTWorkID, id)
	return err
}

// DeleteBook deletes a book only if it has no active or pending checkouts.
func DeleteBook(ctx context.Context, pool *pgxpool.Pool, id string) error {
	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM library.checkouts ch
		JOIN library.copies cp ON cp.id = ch.copy_id
		WHERE cp.book_id = $1 AND ch.status IN ('pending', 'active')
	`, id).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return errors.New("cannot delete book with active or pending checkouts")
	}
	_, err = pool.Exec(ctx, `DELETE FROM library.books WHERE id = $1`, id)
	return err
}

// ---- Copies ----

// ListCopiesForBook returns all copies for a book, with IsAvailable computed.
func ListCopiesForBook(ctx context.Context, pool *pgxpool.Pool, bookID string) ([]Copy, error) {
	rows, err := pool.Query(ctx, `
		SELECT c.id, c.book_id, c.barcode, c.condition, c.notes, c.loan_period_days,
		       c.is_active, c.created_at,
		       NOT EXISTS (
		           SELECT 1 FROM library.checkouts ch
		           WHERE ch.copy_id = c.id AND ch.status IN ('pending', 'active')
		       ) AND c.is_active AS is_available
		FROM library.copies c
		WHERE c.book_id = $1
		ORDER BY c.created_at ASC
	`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	copies := make([]Copy, 0)
	for rows.Next() {
		var c Copy
		if err := rows.Scan(
			&c.ID, &c.BookID, &c.Barcode, &c.Condition, &c.Notes,
			&c.LoanPeriodDays, &c.IsActive, &c.CreatedAt, &c.IsAvailable,
		); err != nil {
			return nil, err
		}
		copies = append(copies, c)
	}
	return copies, rows.Err()
}

// CreateCopy inserts a new copy and returns the created record.
func CreateCopy(ctx context.Context, pool *pgxpool.Pool, c Copy) (*Copy, error) {
	out := &Copy{}
	err := pool.QueryRow(ctx, `
		INSERT INTO library.copies (book_id, barcode, condition, notes, loan_period_days)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, book_id, barcode, condition, notes, loan_period_days,
		          is_active, created_at
	`, c.BookID, c.Barcode, c.Condition, c.Notes, c.LoanPeriodDays).Scan(
		&out.ID, &out.BookID, &out.Barcode, &out.Condition, &out.Notes,
		&out.LoanPeriodDays, &out.IsActive, &out.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	out.IsAvailable = true // newly created copy has no checkouts
	return out, nil
}

// UpdateCopy updates a copy's editable fields.
func UpdateCopy(ctx context.Context, pool *pgxpool.Pool, id string, c Copy) error {
	_, err := pool.Exec(ctx, `
		UPDATE library.copies
		SET barcode = $1, condition = $2, notes = $3, loan_period_days = $4
		WHERE id = $5
	`, c.Barcode, c.Condition, c.Notes, c.LoanPeriodDays, id)
	return err
}

// DeactivateCopy sets is_active = false on a copy.
// Returns an error if the copy has a pending or active checkout.
func DeactivateCopy(ctx context.Context, pool *pgxpool.Pool, id string) error {
	var count int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM library.checkouts
		WHERE copy_id = $1 AND status IN ('pending', 'active')
	`, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return errors.New("cannot deactivate copy with active or pending checkout")
	}
	_, err := pool.Exec(ctx, `
		UPDATE library.copies SET is_active = FALSE WHERE id = $1
	`, id)
	return err
}

// AvailableCopiesForBook returns active copies of a book not in a pending/active checkout.
func AvailableCopiesForBook(ctx context.Context, pool *pgxpool.Pool, bookID string) ([]Copy, error) {
	rows, err := pool.Query(ctx, `
		SELECT c.id, c.book_id, c.barcode, c.condition, c.notes, c.loan_period_days,
		       c.is_active, c.created_at
		FROM library.copies c
		WHERE c.book_id = $1 AND c.is_active = TRUE
		  AND NOT EXISTS (
		      SELECT 1 FROM library.checkouts ch
		      WHERE ch.copy_id = c.id AND ch.status IN ('pending', 'active')
		  )
		ORDER BY c.created_at ASC
	`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	copies := make([]Copy, 0)
	for rows.Next() {
		var c Copy
		if err := rows.Scan(
			&c.ID, &c.BookID, &c.Barcode, &c.Condition, &c.Notes,
			&c.LoanPeriodDays, &c.IsActive, &c.CreatedAt,
		); err != nil {
			return nil, err
		}
		c.IsAvailable = true
		copies = append(copies, c)
	}
	return copies, rows.Err()
}

// ---- Checkouts ----

// RequestCheckout picks an available copy of a book and creates a pending checkout.
// Returns ErrNoAvailableCopies if all copies are checked out, ErrMaxCheckoutsReached
// if the user is at their limit, or ErrAlreadyHasCheckout if they already have one.
func RequestCheckout(ctx context.Context, pool *pgxpool.Pool, bookID, userID string) (*Checkout, error) {
	// Check user's current active checkout count against configured max.
	maxStr, err := GetServiceConfig(ctx, pool, "library", "max_checkouts", nil)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, fmt.Errorf("read max_checkouts: %w", err)
	}
	maxCheckouts, _ := strconv.Atoi(maxStr)
	if maxCheckouts > 0 {
		count, err := UserActiveCheckoutCount(ctx, pool, userID)
		if err != nil {
			return nil, err
		}
		if count >= maxCheckouts {
			return nil, ErrMaxCheckoutsReached
		}
	}

	// Guard: user must not already have a pending/active checkout for this book.
	has, err := UserHasActiveCheckoutForBook(ctx, pool, userID, bookID)
	if err != nil {
		return nil, err
	}
	if has {
		return nil, ErrAlreadyHasCheckout
	}

	// Pick the first available copy.
	available, err := AvailableCopiesForBook(ctx, pool, bookID)
	if err != nil {
		return nil, err
	}
	if len(available) == 0 {
		return nil, ErrNoAvailableCopies
	}
	copyID := available[0].ID

	out := &Checkout{}
	err = pool.QueryRow(ctx, `
		INSERT INTO library.checkouts (copy_id, user_id)
		VALUES ($1, $2)
		RETURNING id, copy_id, user_id, status, due_date, checked_out_at,
		          returned_at, approved_by, returned_by, notes, requested_at
	`, copyID, userID).Scan(
		&out.ID, &out.CopyID, &out.UserID, &out.Status, &out.DueDate,
		&out.CheckedOutAt, &out.ReturnedAt, &out.ApprovedBy, &out.ReturnedBy,
		&out.Notes, &out.RequestedAt,
	)
	if err != nil {
		return nil, err
	}
	out.BookID = bookID
	return out, nil
}

// ApproveCheckout sets a checkout to active, records the approver, and sets the due date.
// adminID must be a web_admins.id UUID; pass "" to leave approved_by NULL (Slack flow).
func ApproveCheckout(ctx context.Context, pool *pgxpool.Pool, id, adminID string, dueDate time.Time) error {
	var dueDatePtr *time.Time
	if !dueDate.IsZero() {
		dueDatePtr = &dueDate
	}
	var approvedBy *string
	if adminID != "" {
		approvedBy = &adminID
	}
	_, err := pool.Exec(ctx, `
		UPDATE library.checkouts
		SET status = 'active', approved_by = $1, checked_out_at = NOW(), due_date = $2
		WHERE id = $3
	`, approvedBy, dueDatePtr, id)
	return err
}

// DenyCheckout sets a checkout to denied and records the admin and reason.
// adminID must be a web_admins.id UUID; pass "" to leave approved_by NULL (Slack flow).
func DenyCheckout(ctx context.Context, pool *pgxpool.Pool, id, adminID, notes string) error {
	var approvedBy *string
	if adminID != "" {
		approvedBy = &adminID
	}
	_, err := pool.Exec(ctx, `
		UPDATE library.checkouts
		SET status = 'denied', approved_by = $1, notes = $2
		WHERE id = $3
	`, approvedBy, notes, id)
	return err
}

// MarkReturned marks a checkout as returned.
// adminID must be a web_admins.id UUID; pass "" to leave returned_by NULL (Slack flow).
func MarkReturned(ctx context.Context, pool *pgxpool.Pool, id, adminID string) error {
	var returnedBy *string
	if adminID != "" {
		returnedBy = &adminID
	}
	_, err := pool.Exec(ctx, `
		UPDATE library.checkouts
		SET status = 'returned', returned_by = $1, returned_at = NOW()
		WHERE id = $2
	`, returnedBy, id)
	return err
}

// ExtendDueDate updates the due date on an active checkout.
func ExtendDueDate(ctx context.Context, pool *pgxpool.Pool, id string, newDate time.Time) error {
	_, err := pool.Exec(ctx, `
		UPDATE library.checkouts SET due_date = $1 WHERE id = $2
	`, newDate, id)
	return err
}

// ListCheckouts returns a paginated list of checkouts, optionally filtered by status.
func ListCheckouts(ctx context.Context, pool *pgxpool.Pool, status string, page, perPage int) ([]Checkout, int, error) {
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * perPage

	args := []any{}
	where := "WHERE 1=1"
	if status != "" {
		args = append(args, status)
		where += " AND ch.status = $" + fmt.Sprintf("%d", len(args))
	}

	var total int
	if err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM library.checkouts ch "+where, args...,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, perPage, offset)
	limitIdx := fmt.Sprintf("$%d", len(args)-1)
	offsetIdx := fmt.Sprintf("$%d", len(args))

	rows, err := pool.Query(ctx, `
		SELECT ch.id, ch.copy_id, cp.barcode, b.id, b.title,
		       ch.user_id, COALESCE(u.display_name, u.email, '') AS user_name,
		       ch.status, ch.due_date, ch.checked_out_at,
		       ch.returned_at, ch.approved_by, ch.returned_by,
		       ch.notes, ch.requested_at
		FROM library.checkouts ch
		JOIN library.copies cp ON cp.id = ch.copy_id
		JOIN library.books b ON b.id = cp.book_id
		JOIN users u ON u.id = ch.user_id
		`+where+`
		ORDER BY ch.requested_at DESC
		LIMIT `+limitIdx+` OFFSET `+offsetIdx,
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	checkouts := make([]Checkout, 0)
	for rows.Next() {
		var c Checkout
		if err := rows.Scan(
			&c.ID, &c.CopyID, &c.CopyBarcode, &c.BookID, &c.BookTitle,
			&c.UserID, &c.UserName, &c.Status, &c.DueDate, &c.CheckedOutAt,
			&c.ReturnedAt, &c.ApprovedBy, &c.ReturnedBy, &c.Notes, &c.RequestedAt,
		); err != nil {
			return nil, 0, err
		}
		checkouts = append(checkouts, c)
	}
	return checkouts, total, rows.Err()
}

// ListCheckoutsForUser returns active and pending checkouts for a user.
func ListCheckoutsForUser(ctx context.Context, pool *pgxpool.Pool, userID string) ([]Checkout, error) {
	rows, err := pool.Query(ctx, `
		SELECT ch.id, ch.copy_id, cp.barcode, b.id, b.title,
		       ch.user_id, COALESCE(u.display_name, u.email, '') AS user_name,
		       ch.status, ch.due_date, ch.checked_out_at,
		       ch.returned_at, ch.approved_by, ch.returned_by,
		       ch.notes, ch.requested_at
		FROM library.checkouts ch
		JOIN library.copies cp ON cp.id = ch.copy_id
		JOIN library.books b ON b.id = cp.book_id
		JOIN users u ON u.id = ch.user_id
		WHERE ch.user_id = $1 AND ch.status IN ('pending', 'active')
		ORDER BY ch.requested_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	checkouts := make([]Checkout, 0)
	for rows.Next() {
		var c Checkout
		if err := rows.Scan(
			&c.ID, &c.CopyID, &c.CopyBarcode, &c.BookID, &c.BookTitle,
			&c.UserID, &c.UserName, &c.Status, &c.DueDate, &c.CheckedOutAt,
			&c.ReturnedAt, &c.ApprovedBy, &c.ReturnedBy, &c.Notes, &c.RequestedAt,
		); err != nil {
			return nil, err
		}
		checkouts = append(checkouts, c)
	}
	return checkouts, rows.Err()
}

// ListOverdueCheckouts returns all active checkouts with a past due_date.
func ListOverdueCheckouts(ctx context.Context, pool *pgxpool.Pool) ([]Checkout, error) {
	rows, err := pool.Query(ctx, `
		SELECT ch.id, ch.copy_id, cp.barcode, b.id, b.title,
		       ch.user_id, COALESCE(u.display_name, u.email, '') AS user_name,
		       ch.status, ch.due_date, ch.checked_out_at,
		       ch.returned_at, ch.approved_by, ch.returned_by,
		       ch.notes, ch.requested_at
		FROM library.checkouts ch
		JOIN library.copies cp ON cp.id = ch.copy_id
		JOIN library.books b ON b.id = cp.book_id
		JOIN users u ON u.id = ch.user_id
		WHERE ch.status = 'active' AND ch.due_date IS NOT NULL AND ch.due_date < CURRENT_DATE
		ORDER BY ch.due_date ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	checkouts := make([]Checkout, 0)
	for rows.Next() {
		var c Checkout
		if err := rows.Scan(
			&c.ID, &c.CopyID, &c.CopyBarcode, &c.BookID, &c.BookTitle,
			&c.UserID, &c.UserName, &c.Status, &c.DueDate, &c.CheckedOutAt,
			&c.ReturnedAt, &c.ApprovedBy, &c.ReturnedBy, &c.Notes, &c.RequestedAt,
		); err != nil {
			return nil, err
		}
		checkouts = append(checkouts, c)
	}
	return checkouts, rows.Err()
}

// ListOverdueUnnotifiedCheckouts returns active checkouts past due_date that have not been
// notified in the last 23 hours (de-dup window for the daily overdue reminder job).
func ListOverdueUnnotifiedCheckouts(ctx context.Context, pool *pgxpool.Pool) ([]Checkout, error) {
	rows, err := pool.Query(ctx, `
		SELECT ch.id, ch.copy_id, cp.barcode, b.id, b.title,
		       ch.user_id, COALESCE(u.display_name, u.email, '') AS user_name,
		       ch.status, ch.due_date, ch.checked_out_at,
		       ch.returned_at, ch.approved_by, ch.returned_by,
		       ch.notes, ch.requested_at
		FROM library.checkouts ch
		JOIN library.copies cp ON cp.id = ch.copy_id
		JOIN library.books b ON b.id = cp.book_id
		JOIN users u ON u.id = ch.user_id
		WHERE ch.status = 'active'
		  AND ch.due_date IS NOT NULL AND ch.due_date < CURRENT_DATE
		  AND (ch.overdue_notified_at IS NULL OR ch.overdue_notified_at < NOW() - INTERVAL '23 hours')
		ORDER BY ch.due_date ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	checkouts := make([]Checkout, 0)
	for rows.Next() {
		var c Checkout
		if err := rows.Scan(
			&c.ID, &c.CopyID, &c.CopyBarcode, &c.BookID, &c.BookTitle,
			&c.UserID, &c.UserName, &c.Status, &c.DueDate, &c.CheckedOutAt,
			&c.ReturnedAt, &c.ApprovedBy, &c.ReturnedBy, &c.Notes, &c.RequestedAt,
		); err != nil {
			return nil, err
		}
		checkouts = append(checkouts, c)
	}
	return checkouts, rows.Err()
}

// MarkCheckoutOverdueNotified stamps overdue_notified_at = NOW() on a checkout.
func MarkCheckoutOverdueNotified(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `
		UPDATE library.checkouts SET overdue_notified_at = NOW() WHERE id = $1
	`, id)
	return err
}

// ---- Holds ----

// PlaceHold appends a hold for a user on a book.
// Returns ErrAlreadyHasHold if the user already has a hold, or ErrAlreadyHasCheckout
// if they have an active/pending checkout for this book.
func PlaceHold(ctx context.Context, pool *pgxpool.Pool, bookID, userID string) (*Hold, error) {
	has, err := UserHasActiveCheckoutForBook(ctx, pool, userID, bookID)
	if err != nil {
		return nil, err
	}
	if has {
		return nil, ErrAlreadyHasCheckout
	}

	out := &Hold{}
	err = pool.QueryRow(ctx, `
		INSERT INTO library.holds (book_id, user_id, position)
		SELECT $1, $2, COALESCE(MAX(position), 0) + 1
		FROM library.holds
		WHERE book_id = $1
		  AND fulfilled_at IS NULL
		  AND cancelled_at IS NULL
		RETURNING id, book_id, user_id, position, requested_at,
		          notified_at, fulfilled_at, cancelled_at
	`, bookID, userID).Scan(
		&out.ID, &out.BookID, &out.UserID, &out.Position, &out.RequestedAt,
		&out.NotifiedAt, &out.FulfilledAt, &out.CancelledAt,
	)
	if err != nil {
		// UNIQUE (book_id, user_id) violation
		if isUniqueViolation(err) {
			return nil, ErrAlreadyHasHold
		}
		return nil, err
	}
	out.BookTitle = "" // not populated at insert time; callers use ListHoldsForBook if needed
	return out, nil
}

// isUniqueViolation returns true for PostgreSQL error code 23505.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	type pgErr interface{ SQLState() string }
	var pe pgErr
	if errors.As(err, &pe) {
		return pe.SQLState() == "23505"
	}
	return false
}

// CancelHold marks a hold as cancelled.
func CancelHold(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `
		UPDATE library.holds SET cancelled_at = NOW() WHERE id = $1
	`, id)
	return err
}

// ListHoldsForBook returns all active (unfulfilled, uncancelled) holds for a book,
// ordered by position.
func ListHoldsForBook(ctx context.Context, pool *pgxpool.Pool, bookID string) ([]Hold, error) {
	rows, err := pool.Query(ctx, `
		SELECT h.id, h.book_id, b.title, h.user_id,
		       COALESCE(u.display_name, u.email, '') AS user_name,
		       h.position, h.requested_at, h.notified_at, h.fulfilled_at, h.cancelled_at
		FROM library.holds h
		JOIN library.books b ON b.id = h.book_id
		JOIN users u ON u.id = h.user_id
		WHERE h.book_id = $1
		  AND h.fulfilled_at IS NULL AND h.cancelled_at IS NULL
		ORDER BY h.position ASC
	`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHolds(rows)
}

// ListHoldsForUser returns active holds for a user.
func ListHoldsForUser(ctx context.Context, pool *pgxpool.Pool, userID string) ([]Hold, error) {
	rows, err := pool.Query(ctx, `
		SELECT h.id, h.book_id, b.title, h.user_id,
		       COALESCE(u.display_name, u.email, '') AS user_name,
		       h.position, h.requested_at, h.notified_at, h.fulfilled_at, h.cancelled_at
		FROM library.holds h
		JOIN library.books b ON b.id = h.book_id
		JOIN users u ON u.id = h.user_id
		WHERE h.user_id = $1
		  AND h.fulfilled_at IS NULL AND h.cancelled_at IS NULL
		ORDER BY h.requested_at ASC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHolds(rows)
}

// NextHoldForBook returns the first unfulfilled, unnotified hold for a book.
// Returns nil if there are no waiting holds.
func NextHoldForBook(ctx context.Context, pool *pgxpool.Pool, bookID string) (*Hold, error) {
	h := &Hold{}
	err := pool.QueryRow(ctx, `
		SELECT h.id, h.book_id, b.title, h.user_id,
		       COALESCE(u.display_name, u.email, '') AS user_name,
		       h.position, h.requested_at, h.notified_at, h.fulfilled_at, h.cancelled_at
		FROM library.holds h
		JOIN library.books b ON b.id = h.book_id
		JOIN users u ON u.id = h.user_id
		WHERE h.book_id = $1
		  AND h.fulfilled_at IS NULL AND h.cancelled_at IS NULL AND h.notified_at IS NULL
		ORDER BY h.position ASC
		LIMIT 1
	`, bookID).Scan(
		&h.ID, &h.BookID, &h.BookTitle, &h.UserID, &h.UserName,
		&h.Position, &h.RequestedAt, &h.NotifiedAt, &h.FulfilledAt, &h.CancelledAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return h, nil
}

// MarkHoldNotified sets notified_at on a hold.
func MarkHoldNotified(ctx context.Context, pool *pgxpool.Pool, id string) error {
	_, err := pool.Exec(ctx, `
		UPDATE library.holds SET notified_at = NOW() WHERE id = $1
	`, id)
	return err
}

// FulfillHold marks a hold as fulfilled and links it to the resulting checkout.
func FulfillHold(ctx context.Context, pool *pgxpool.Pool, holdID, _ string) error {
	_, err := pool.Exec(ctx, `
		UPDATE library.holds SET fulfilled_at = NOW() WHERE id = $1
	`, holdID)
	return err
}

// scanHolds is a shared helper to scan hold rows.
func scanHolds(rows pgx.Rows) ([]Hold, error) {
	defer rows.Close()
	holds := make([]Hold, 0)
	for rows.Next() {
		var h Hold
		if err := rows.Scan(
			&h.ID, &h.BookID, &h.BookTitle, &h.UserID, &h.UserName,
			&h.Position, &h.RequestedAt, &h.NotifiedAt, &h.FulfilledAt, &h.CancelledAt,
		); err != nil {
			return nil, err
		}
		holds = append(holds, h)
	}
	return holds, rows.Err()
}

// BookSummary is a lightweight book record with per-user availability context,
// used for the Slack browse modal.
type BookSummary struct {
	Book
	AvailableCopies int
	TotalCopies     int
	HoldsCount      int
	UserHasCheckout bool // user has a pending/active checkout for this book
	UserHasHold     bool // user has an active hold on this book
}

// HasLibraryBooks returns true if there is at least one book in the library.
func HasLibraryBooks(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM library.books LIMIT 1)`).Scan(&exists)
	return exists, err
}

// ListBooksWithAvailabilityForUser returns a paginated list of books with per-user availability
// context computed in a single query. userID may be empty for anonymous requests.
func ListBooksWithAvailabilityForUser(ctx context.Context, pool *pgxpool.Pool, userID, search string, page, perPage int) ([]BookSummary, int, error) {
	if page < 1 {
		page = 1
	}
	offset := (page - 1) * perPage

	// Count query: only needs search filter (no userID).
	var countArgs []any
	countWhere := "WHERE 1=1"
	if search != "" {
		countArgs = append(countArgs, "%"+search+"%")
		idx := fmt.Sprintf("$%d", len(countArgs))
		countWhere += " AND (b.title ILIKE " + idx + " OR b.author ILIKE " + idx + ")"
	}

	var total int
	if err := pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM library.books b "+countWhere, countArgs...,
	).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Main query: $1 = userID for per-user CTEs, then search/limit/offset.
	args := []any{userID}
	where := "WHERE 1=1"
	if search != "" {
		args = append(args, "%"+search+"%")
		idx := fmt.Sprintf("$%d", len(args))
		where += " AND (b.title ILIKE " + idx + " OR b.author ILIKE " + idx + ")"
	}
	args = append(args, perPage, offset)
	limitIdx := fmt.Sprintf("$%d", len(args)-1)
	offsetIdx := fmt.Sprintf("$%d", len(args))

	rows, err := pool.Query(ctx, `
		WITH avail AS (
		    SELECT cp.book_id,
		           COUNT(cp.id) FILTER (WHERE cp.is_active) AS total_copies,
		           COUNT(cp.id) FILTER (
		               WHERE cp.is_active AND NOT EXISTS (
		                   SELECT 1 FROM library.checkouts ch
		                   WHERE ch.copy_id = cp.id AND ch.status IN ('pending', 'active')
		               )
		           ) AS available_copies
		    FROM library.copies cp
		    GROUP BY cp.book_id
		),
		holds_cnt AS (
		    SELECT book_id, COUNT(*) AS holds_count
		    FROM library.holds
		    WHERE fulfilled_at IS NULL AND cancelled_at IS NULL
		    GROUP BY book_id
		),
		user_co AS (
		    SELECT cp.book_id, TRUE AS has_checkout
		    FROM library.checkouts ch
		    JOIN library.copies cp ON cp.id = ch.copy_id
		    WHERE ch.user_id = $1 AND ch.status IN ('pending', 'active')
		),
		user_hold AS (
		    SELECT book_id, TRUE AS has_hold
		    FROM library.holds
		    WHERE user_id = $1 AND fulfilled_at IS NULL AND cancelled_at IS NULL
		)
		SELECT b.id, b.title, b.author, b.isbn, b.cover_url, b.description,
		       b.lt_work_id, b.added_by, b.created_at, b.updated_at,
		       COALESCE(a.available_copies, 0),
		       COALESCE(a.total_copies, 0),
		       COALESCE(hc.holds_count, 0),
		       COALESCE(uc.has_checkout, FALSE),
		       COALESCE(uh.has_hold, FALSE)
		FROM library.books b
		LEFT JOIN avail a ON a.book_id = b.id
		LEFT JOIN holds_cnt hc ON hc.book_id = b.id
		LEFT JOIN user_co uc ON uc.book_id = b.id
		LEFT JOIN user_hold uh ON uh.book_id = b.id
		`+where+`
		ORDER BY b.title ASC
		LIMIT `+limitIdx+` OFFSET `+offsetIdx,
		args...,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	summaries := make([]BookSummary, 0)
	for rows.Next() {
		var s BookSummary
		if err := rows.Scan(
			&s.ID, &s.Title, &s.Author, &s.ISBN, &s.CoverURL, &s.Description,
			&s.LTWorkID, &s.AddedBy, &s.CreatedAt, &s.UpdatedAt,
			&s.AvailableCopies, &s.TotalCopies, &s.HoldsCount,
			&s.UserHasCheckout, &s.UserHasHold,
		); err != nil {
			return nil, 0, err
		}
		summaries = append(summaries, s)
	}
	return summaries, total, rows.Err()
}

// GetCheckoutByID returns a single checkout by ID with joined book title, user name, and copy barcode.
// Returns ErrNotFound if absent.
func GetCheckoutByID(ctx context.Context, pool *pgxpool.Pool, id string) (*Checkout, error) {
	co := &Checkout{}
	err := pool.QueryRow(ctx, `
		SELECT ch.id, ch.copy_id, cp.barcode, b.id, b.title,
		       ch.user_id, COALESCE(u.display_name, u.email, '') AS user_name,
		       ch.status, ch.due_date, ch.checked_out_at,
		       ch.returned_at, ch.approved_by, ch.returned_by,
		       ch.notes, ch.requested_at
		FROM library.checkouts ch
		JOIN library.copies cp ON cp.id = ch.copy_id
		JOIN library.books b ON b.id = cp.book_id
		JOIN users u ON u.id = ch.user_id
		WHERE ch.id = $1
	`, id).Scan(
		&co.ID, &co.CopyID, &co.CopyBarcode, &co.BookID, &co.BookTitle,
		&co.UserID, &co.UserName, &co.Status, &co.DueDate, &co.CheckedOutAt,
		&co.ReturnedAt, &co.ApprovedBy, &co.ReturnedBy, &co.Notes, &co.RequestedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return co, nil
}

// GetHoldByID returns a single hold by ID with joined book title and user name.
// Returns ErrNotFound if absent.
func GetHoldByID(ctx context.Context, pool *pgxpool.Pool, id string) (*Hold, error) {
	h := &Hold{}
	err := pool.QueryRow(ctx, `
		SELECT h.id, h.book_id, b.title, h.user_id,
		       COALESCE(u.display_name, u.email, '') AS user_name,
		       h.position, h.requested_at, h.notified_at, h.fulfilled_at, h.cancelled_at
		FROM library.holds h
		JOIN library.books b ON b.id = h.book_id
		JOIN users u ON u.id = h.user_id
		WHERE h.id = $1
	`, id).Scan(
		&h.ID, &h.BookID, &h.BookTitle, &h.UserID, &h.UserName,
		&h.Position, &h.RequestedAt, &h.NotifiedAt, &h.FulfilledAt, &h.CancelledAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return h, nil
}

// ListAllActiveHolds returns all unfulfilled, uncancelled holds across all books,
// ordered by book title then queue position.
func ListAllActiveHolds(ctx context.Context, pool *pgxpool.Pool) ([]Hold, error) {
	rows, err := pool.Query(ctx, `
		SELECT h.id, h.book_id, b.title, h.user_id,
		       COALESCE(u.display_name, u.email, '') AS user_name,
		       h.position, h.requested_at, h.notified_at, h.fulfilled_at, h.cancelled_at
		FROM library.holds h
		JOIN library.books b ON b.id = h.book_id
		JOIN users u ON u.id = h.user_id
		WHERE h.fulfilled_at IS NULL AND h.cancelled_at IS NULL
		ORDER BY b.title ASC, h.position ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanHolds(rows)
}

// ---- Helpers ----

// UserActiveCheckoutCount returns the number of active or pending checkouts for a user.
func UserActiveCheckoutCount(ctx context.Context, pool *pgxpool.Pool, userID string) (int, error) {
	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM library.checkouts
		WHERE user_id = $1 AND status IN ('pending', 'active')
	`, userID).Scan(&count)
	return count, err
}

// UserHasActiveCheckoutForBook returns true if the user has a pending or active checkout
// for any copy of the given book.
func UserHasActiveCheckoutForBook(ctx context.Context, pool *pgxpool.Pool, userID, bookID string) (bool, error) {
	var has bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM library.checkouts ch
		    JOIN library.copies cp ON cp.id = ch.copy_id
		    WHERE ch.user_id = $1 AND cp.book_id = $2
		      AND ch.status IN ('pending', 'active')
		)
	`, userID, bookID).Scan(&has)
	return has, err
}
