package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInventoryInvalidValue is returned when a single-field edit names an unknown column or gives a value
// the column can't hold (an empty item name).
var ErrInventoryInvalidValue = errors.New("invalid inventory value")

// InventoryGridQuery selects, orders, and pages the rows of the inventory grid. Phase 9's Slack list reuses it.
type InventoryGridQuery struct {
	ID              string // limit to one item (used to render a single cell)
	Search          string // matches name, code, location, description, holder, and any custom field value
	IncludeArchived bool
	PendingOnly     bool   // only items with an unmatched claim waiting
	HeldBy          string // only items this user currently holds
	// AwaitingVerification limits to held items whose holder was sent a verification reminder and hasn't
	// confirmed since (a verification or a new holder clears the reminder).
	AwaitingVerification bool
	SortKey              string // name (default), code, location, holder, verified, or field:<key>
	Desc                 bool
	Limit                int // <= 0 means no limit
	Offset               int
}

// InventoryGridRow is an item with its holder's name and whether an unmatched claim is waiting on it.
type InventoryGridRow struct {
	InventoryItem
	HolderName   string
	HolderEmail  string
	ClaimPending bool
}

const inventoryGridSelect = `
	SELECT i.id, i.name, i.description, i.location, i.code, i.metadata, i.verify_every_days, i.verify_grace_days,
	       i.last_verified_at, i.last_reminded_at, i.last_escalated_at, i.current_holder_user_id, i.archived_at,
	       i.created_at, i.updated_at,
	       COALESCE(u.display_name, ''), COALESCE(u.email, ''),
	       EXISTS (SELECT 1 FROM inventory.custody_events e WHERE e.item_id = i.id AND e.status = 'pending_unmatched')
	FROM inventory.items i
	LEFT JOIN users u ON u.id = i.current_holder_user_id`

type sqlArgs struct{ vals []any }

// add records a value and returns its $n placeholder.
func (a *sqlArgs) add(v any) string {
	a.vals = append(a.vals, v)
	return "$" + strconv.Itoa(len(a.vals))
}

// likeEscape escapes LIKE wildcards so a search for "50%" finds "50%", not everything.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func inventoryGridWhere(q InventoryGridQuery, args *sqlArgs) string {
	var conds []string
	if q.ID != "" {
		conds = append(conds, "i.id = "+args.add(q.ID))
	}
	if !q.IncludeArchived {
		conds = append(conds, "i.archived_at IS NULL")
	}
	if q.PendingOnly {
		conds = append(conds, "EXISTS (SELECT 1 FROM inventory.custody_events e WHERE e.item_id = i.id AND e.status = 'pending_unmatched')")
	}
	if q.HeldBy != "" {
		conds = append(conds, "i.current_holder_user_id = "+args.add(q.HeldBy))
	}
	if q.AwaitingVerification {
		conds = append(conds, "i.current_holder_user_id IS NOT NULL AND i.last_reminded_at IS NOT NULL")
	}
	if term := strings.TrimSpace(q.Search); term != "" {
		p := args.add("%" + likeEscape(term) + "%")
		clauses := []string{
			"i.name ILIKE " + p + ` ESCAPE '\'`,
			"i.location ILIKE " + p + ` ESCAPE '\'`,
			"i.description ILIKE " + p + ` ESCAPE '\'`,
			"u.display_name ILIKE " + p + ` ESCAPE '\'`,
			"u.email ILIKE " + p + ` ESCAPE '\'`,
			"EXISTS (SELECT 1 FROM jsonb_each_text(i.metadata) m WHERE m.value ILIKE " + p + ` ESCAPE '\')`,
		}
		// Codes are stored without the dash, so "k7m-4qx" must be searched as "K7M4QX".
		if code := NormalizeInventoryCode(term); code != "" {
			clauses = append(clauses, "i.code ILIKE "+args.add("%"+likeEscape(code)+"%")+` ESCAPE '\'`)
		}
		conds = append(conds, "("+strings.Join(clauses, " OR ")+")")
	}
	if len(conds) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(conds, " AND ")
}

// inventoryGridOrder builds an ORDER BY from a whitelist of sort keys; custom field keys go in as a parameter.
func inventoryGridOrder(sortKey string, desc bool, args *sqlArgs) string {
	dir := "ASC"
	if desc {
		dir = "DESC"
	}
	var expr string
	switch {
	case sortKey == "code":
		expr = "i.code"
	case sortKey == "location":
		expr = "lower(COALESCE(i.location, ''))"
	case sortKey == "holder":
		expr = "lower(COALESCE(u.display_name, ''))"
	case sortKey == "verified":
		return fmt.Sprintf("i.last_verified_at %s NULLS LAST, lower(i.name), i.id", dir)
	case strings.HasPrefix(sortKey, "field:"):
		expr = "lower(COALESCE(i.metadata->>" + args.add(strings.TrimPrefix(sortKey, "field:")) + ", ''))"
	default:
		expr = "lower(i.name)"
	}
	return fmt.Sprintf("%s %s, lower(i.name), i.id", expr, dir)
}

// ListInventoryGrid returns one page of grid rows and the total number of rows matching the filters.
func ListInventoryGrid(ctx context.Context, pool *pgxpool.Pool, q InventoryGridQuery) ([]InventoryGridRow, int, error) {
	where := &sqlArgs{}
	cond := inventoryGridWhere(q, where)

	var total int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM inventory.items i LEFT JOIN users u ON u.id = i.current_holder_user_id`+cond,
		where.vals...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args := &sqlArgs{vals: append([]any(nil), where.vals...)}
	sql := inventoryGridSelect + cond + " ORDER BY " + inventoryGridOrder(q.SortKey, q.Desc, args)
	if q.Limit > 0 {
		sql += " LIMIT " + args.add(q.Limit) + " OFFSET " + args.add(max(q.Offset, 0))
	}
	rows, err := pool.Query(ctx, sql, args.vals...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]InventoryGridRow, 0)
	for rows.Next() {
		var r InventoryGridRow
		it := &r.InventoryItem
		if err := rows.Scan(&it.ID, &it.Name, &it.Description, &it.Location, &it.Code, &it.Metadata,
			&it.VerifyEveryDays, &it.VerifyGraceDays, &it.LastVerifiedAt, &it.LastRemindedAt, &it.LastEscalatedAt,
			&it.CurrentHolderUserID, &it.ArchivedAt, &it.CreatedAt, &it.UpdatedAt,
			&r.HolderName, &r.HolderEmail, &r.ClaimPending); err != nil {
			return nil, 0, err
		}
		if it.Metadata == nil {
			it.Metadata = map[string]string{}
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// SetInventoryItemBuiltinField updates one built-in column of an active item for inline editing: "name"
// (cannot be empty) or "location" (empty clears it). Returns ErrInventoryInvalidValue for any other column or
// an empty name, ErrInventoryItemArchived for an archived item, and ErrNotFound if the item is absent.
func SetInventoryItemBuiltinField(ctx context.Context, pool *pgxpool.Pool, id, column, value string) error {
	value = strings.TrimSpace(value)
	var sql string
	switch column {
	case "name":
		if value == "" {
			return ErrInventoryInvalidValue
		}
		sql = `UPDATE inventory.items SET name = $2, updated_at = NOW() WHERE id = $1 AND archived_at IS NULL`
	case "location":
		sql = `UPDATE inventory.items SET location = NULLIF($2, ''), updated_at = NOW() WHERE id = $1 AND archived_at IS NULL`
	default:
		return ErrInventoryInvalidValue
	}
	tag, err := pool.Exec(ctx, sql, id, value)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	item, err := GetInventoryItem(ctx, pool, id)
	if err != nil {
		return err
	}
	if item.ArchivedAt != nil {
		return ErrInventoryItemArchived
	}
	return ErrNotFound
}
