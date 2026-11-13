package store

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Custom field kinds.
const (
	InventoryFieldText   = "text"
	InventoryFieldSelect = "select"
)

// Limits on a dropdown field's list.
const (
	MaxInventoryFieldOptions   = 200
	MaxInventoryFieldOptionLen = 200
)

// ErrInventoryNotAnOption is returned when a value isn't in a dropdown field's list.
var ErrInventoryNotAnOption = errors.New("value is not one of the field's options")

// ErrInventoryFieldOptionsInvalid is returned for an empty list, or too many or too long options.
var ErrInventoryFieldOptionsInvalid = errors.New("inventory field options are invalid")

// IsSelect reports whether the field is a dropdown.
func (d InventoryFieldDefinition) IsSelect() bool { return d.Kind == InventoryFieldSelect }

// CheckValue validates a non-blank value for the field. A text field accepts anything. A dropdown field
// accepts only one of its options, matched without regard to case, and returns the option as written in the
// list so every item spells it the same way. Otherwise ErrInventoryNotAnOption.
func (d InventoryFieldDefinition) CheckValue(v string) (string, error) {
	if !d.IsSelect() {
		return v, nil
	}
	for _, o := range d.Options {
		if strings.EqualFold(o, v) {
			return o, nil
		}
	}
	return "", ErrInventoryNotAnOption
}

// NormalizeInventoryFieldOptions trims the options, drops blanks and repeats (any case), and checks the
// limits. The first spelling of a repeat wins. An empty result is ErrInventoryFieldOptionsInvalid.
func NormalizeInventoryFieldOptions(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, o := range in {
		o = strings.TrimSpace(o)
		if o == "" || seen[strings.ToLower(o)] {
			continue
		}
		if utf8.RuneCountInString(o) > MaxInventoryFieldOptionLen {
			return nil, ErrInventoryFieldOptionsInvalid
		}
		seen[strings.ToLower(o)] = true
		out = append(out, o)
	}
	if len(out) == 0 || len(out) > MaxInventoryFieldOptions {
		return nil, ErrInventoryFieldOptionsInvalid
	}
	return out, nil
}

// SetInventoryFieldKind makes a field a dropdown with the given options, or plain text again. It works on a
// field that already has values: nothing stored on items is changed, so a value that isn't in the new list
// stays on its item (shown as it is) until someone picks another. Going back to text keeps the list out of
// the way but the values stay. Returns ErrNotFound for an unknown field.
func SetInventoryFieldKind(ctx context.Context, pool *pgxpool.Pool, id, kind string, options []string) error {
	switch kind {
	case InventoryFieldText:
		options = []string{}
	case InventoryFieldSelect:
		var err error
		if options, err = NormalizeInventoryFieldOptions(options); err != nil {
			return err
		}
	default:
		return ErrInventoryFieldOptionsInvalid
	}
	tag, err := pool.Exec(ctx,
		`UPDATE inventory.field_definitions SET kind = $2, options = $3 WHERE id = $1`, id, kind, options)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListInventoryFieldValues returns the distinct non-blank values stored for each of the given field keys,
// sorted, at most MaxInventoryFieldOptions per key. It is what a text field becoming a dropdown starts from.
func ListInventoryFieldValues(ctx context.Context, pool *pgxpool.Pool, keys []string) (map[string][]string, error) {
	out := map[string][]string{}
	if len(keys) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT e.key, e.value
		FROM inventory.items i, jsonb_each_text(i.metadata) e
		WHERE e.key = ANY($1) AND e.value <> ''`, keys)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = append(out[k], v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for k, vs := range out {
		sort.Slice(vs, func(i, j int) bool { return strings.ToLower(vs[i]) < strings.ToLower(vs[j]) })
		if len(vs) > MaxInventoryFieldOptions {
			vs = vs[:MaxInventoryFieldOptions]
		}
		out[k] = vs
	}
	return out, nil
}
