package store

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Limits on an inventory CSV import.
const (
	MaxInventoryImportBytes = 2 << 20
	MaxInventoryImportRows  = 5000

	maxImportNameLen        = 200
	maxImportValueLen       = 2000
	importDefaultVerifyDays = 7
)

// ErrInventoryImportInvalid is returned when applying a plan that has errors.
var ErrInventoryImportInvalid = errors.New("inventory import has errors")

// importCodePattern is the alphabet NewInventoryCode draws from. Imported codes must use it so every code
// stays unambiguous on a printed label.
var importCodePattern = regexp.MustCompile(`^[A-HJ-NP-Z2-9]{6}$`)

// InventoryFieldKey derives the stable key for a custom field from its name: lowercase letters and digits
// joined by single underscores. It is empty if the name has no letters or digits.
func InventoryFieldKey(label string) string {
	var b strings.Builder
	pendingSep := false
	for _, r := range strings.ToLower(label) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if pendingSep && b.Len() > 0 {
				b.WriteByte('_')
			}
			pendingSep = false
			b.WriteRune(r)
		} else {
			pendingSep = true
		}
	}
	return b.String()
}

// InventoryImportIssue is a problem or note about the file. Line is 1-based (the header is line 1); 0 means the
// file as a whole.
type InventoryImportIssue struct {
	Line    int
	Message string
}

// InventoryImportField is a custom field the import would create.
type InventoryImportField struct {
	Key   string
	Label string
}

// InventoryImportRow is one parsed CSV row. A blank cell means "leave as it is" for an update, so the optional
// fields are pointers or omitted from Values.
type InventoryImportRow struct {
	Line            int
	Name            string
	Code            string // normalized; empty means generate one
	Location        *string
	Description     *string
	VerifyEveryDays *int
	HolderEmail     string
	Accessories     []string          // names to add to the item, in order; existing ones are kept
	Values          map[string]string // custom field key to a non-blank value
}

// InventoryImportPlanRow is a row with what the import will do with it.
type InventoryImportPlanRow struct {
	InventoryImportRow
	ItemID       string // the existing item to update; empty means create a new one
	HolderUserID string // the member who will hold it afterwards; empty means leave the holder alone
}

// InventoryImportPlan is what an import would do. It is built without writing anything.
type InventoryImportPlan struct {
	Rows      []InventoryImportPlanRow
	NewFields []InventoryImportField
	Errors    []InventoryImportIssue // any error blocks the import
	Warnings  []InventoryImportIssue
}

func (p *InventoryImportPlan) Creates() int {
	n := 0
	for _, r := range p.Rows {
		if r.ItemID == "" {
			n++
		}
	}
	return n
}

func (p *InventoryImportPlan) Updates() int { return len(p.Rows) - p.Creates() }

func (p *InventoryImportPlan) HolderChanges() int {
	n := 0
	for _, r := range p.Rows {
		if r.HolderUserID != "" {
			n++
		}
	}
	return n
}

// InventoryImportResult counts what an applied import did.
type InventoryImportResult struct {
	Created          int
	Updated          int
	HoldersSet       int
	AccessoriesAdded int
	FieldsCreated    int
	CreatedItems     []InventoryImportCreated
}

type InventoryImportCreated struct {
	Name string
	Code string
}

type importColumnKind int

const (
	colIgnore importColumnKind = iota
	colName
	colCode
	colLocation
	colDescription
	colVerify
	colHolderEmail
	colHolderAny // the export's Holder column: a display name, unless the cell holds an email address
	colAccessories
	colField
)

type importColumn struct {
	kind importColumnKind
	key  string // custom field key, for colField
}

func importHeaderKind(h string) (importColumnKind, bool) {
	switch h {
	case "name":
		return colName, true
	case "code":
		return colCode, true
	case "location":
		return colLocation, true
	case "description":
		return colDescription, true
	case "verify every (days)", "verify every days", "verify every":
		return colVerify, true
	case "holder email", "holder e-mail":
		return colHolderEmail, true
	case "holder":
		return colHolderAny, true
	case "accessories", "accessory":
		return colAccessories, true
	case "last verified", "status", "claim pending": // columns the grid export adds; read-only
		return colIgnore, true
	}
	return colIgnore, false
}

// importUnescape undoes the apostrophe the CSV export puts in front of cells that look like spreadsheet
// formulas, so an export can be imported back unchanged.
func importUnescape(s string) string {
	if len(s) >= 2 && s[0] == '\'' && strings.ContainsRune("=+-@\t\r", rune(s[1])) {
		return s[1:]
	}
	return s
}

// importDelimiter picks a semicolon when the header line has more semicolons than commas, as spreadsheets
// do in some regions.
func importDelimiter(data []byte) rune {
	line := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		line = data[:i]
	}
	if bytes.Count(line, []byte(";")) > bytes.Count(line, []byte(",")) {
		return ';'
	}
	return ','
}

// PlanInventoryImport parses and checks a CSV and works out what importing it would do, without writing
// anything. Problems with the file or its rows are returned in the plan, not as an error. allowHolders says
// whether the Holder email column may assign holders (it is meant for inventory admins).
func PlanInventoryImport(ctx context.Context, pool *pgxpool.Pool, data []byte, allowHolders bool) (*InventoryImportPlan, error) {
	plan := &InventoryImportPlan{}
	fileErr := func(format string, a ...any) {
		plan.Errors = append(plan.Errors, InventoryImportIssue{Message: fmt.Sprintf(format, a...)})
	}

	if len(data) > MaxInventoryImportBytes {
		fileErr("The file is larger than %d MB.", MaxInventoryImportBytes>>20)
		return plan, nil
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(data) {
		fileErr("The file isn't UTF-8 text. In your spreadsheet, save or export it as \"CSV UTF-8\".")
		return plan, nil
	}

	reader := csv.NewReader(bytes.NewReader(data))
	reader.Comma = importDelimiter(data)
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true

	type record struct {
		line  int
		cells []string
	}
	var records []record
	for {
		cells, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var pe *csv.ParseError
			if errors.As(err, &pe) {
				fileErr("The file couldn't be read at line %d: %v", pe.StartLine, pe.Err)
			} else {
				fileErr("The file couldn't be read: %v", err)
			}
			return plan, nil
		}
		line, _ := reader.FieldPos(0)
		records = append(records, record{line, cells})
	}
	if len(records) == 0 {
		fileErr("The file is empty. The first line must be the column headings.")
		return plan, nil
	}
	if len(records)-1 > MaxInventoryImportRows {
		fileErr("The file has more than %d rows. Split it into smaller files.", MaxInventoryImportRows)
		return plan, nil
	}

	// Columns.
	defs, err := ListInventoryFieldDefinitions(ctx, pool, false)
	if err != nil {
		return nil, err
	}
	defByKey := map[string]InventoryFieldDefinition{}
	for _, d := range defs {
		defByKey[d.Key] = d
	}

	header := records[0].cells
	columns := make([]importColumn, len(header))
	seenKind := map[importColumnKind]string{}
	seenFieldKey := map[string]string{}
	for i, raw := range header {
		label := strings.TrimSpace(raw)
		if label == "" {
			continue // an unlabeled column is ignored
		}
		if kind, ok := importHeaderKind(strings.ToLower(label)); ok {
			if kind != colIgnore {
				if prev, dup := seenKind[kind]; dup {
					fileErr("The columns %q and %q are both %s. Keep one.", prev, label, strings.ToLower(label))
				}
				seenKind[kind] = label
			}
			columns[i] = importColumn{kind: kind}
			continue
		}

		key := InventoryFieldKey(label)
		if key == "" {
			fileErr("The column heading %q can't be used as a field name. Use letters or numbers.", label)
			continue
		}
		if prev, dup := seenFieldKey[key]; dup {
			fileErr("The columns %q and %q are the same custom field.", prev, label)
			continue
		}
		seenFieldKey[key] = label
		switch def, exists := defByKey[key]; {
		case exists && def.Active:
			columns[i] = importColumn{kind: colField, key: key}
		case exists:
			plan.Warnings = append(plan.Warnings, InventoryImportIssue{Line: 1,
				Message: fmt.Sprintf("The custom field %q is inactive, so its column was skipped. Reactivate it on the Custom Fields page to import it.", def.Label)})
		default:
			columns[i] = importColumn{kind: colField, key: key}
			plan.NewFields = append(plan.NewFields, InventoryImportField{Key: key, Label: label})
		}
	}
	if _, ok := seenKind[colName]; !ok {
		fileErr("There is no Name column. The first line must be the column headings, including Name.")
	}
	if len(plan.Errors) > 0 {
		return plan, nil
	}

	// Rows.
	cell := func(rec record, i int) string {
		if i >= len(rec.cells) {
			return ""
		}
		return importUnescape(strings.TrimSpace(rec.cells[i]))
	}
	rowErr := func(line int, format string, a ...any) {
		plan.Errors = append(plan.Errors, InventoryImportIssue{Line: line, Message: fmt.Sprintf(format, a...)})
	}
	rowWarn := func(line int, format string, a ...any) {
		plan.Warnings = append(plan.Warnings, InventoryImportIssue{Line: line, Message: fmt.Sprintf(format, a...)})
	}

	var rows []InventoryImportRow
	seenCode := map[string]int{}
	holderIgnoredWarned := false
	for _, rec := range records[1:] {
		blank := true
		for i := range rec.cells {
			if cell(rec, i) != "" {
				blank = false
				break
			}
		}
		if blank {
			continue
		}

		row := InventoryImportRow{Line: rec.line, Values: map[string]string{}}
		holderCell, holderIsEmail := "", false
		for i, col := range columns {
			v := cell(rec, i)
			switch col.kind {
			case colName:
				row.Name = v
			case colCode:
				row.Code = NormalizeInventoryCode(v)
			case colLocation:
				if v != "" {
					row.Location = &v
				}
			case colDescription:
				if v != "" {
					row.Description = &v
				}
			case colVerify:
				if v == "" {
					continue
				}
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 {
					rowErr(rec.line, "Verify every must be a whole number of days, 1 or more, or blank.")
					continue
				}
				row.VerifyEveryDays = &n
			case colAccessories:
				names, err := splitImportAccessories(v)
				if err != nil {
					rowErr(rec.line, "%v", err)
					continue
				}
				row.Accessories = names
			case colHolderEmail:
				holderCell, holderIsEmail = v, true
			case colHolderAny:
				if !holderIsEmail && strings.Contains(v, "@") {
					holderCell = v
				}
			case colField:
				if v == "" {
					continue
				}
				if utf8.RuneCountInString(v) > maxImportValueLen {
					rowErr(rec.line, "A value in the %q column is longer than %d characters.", columnLabel(header, i), maxImportValueLen)
					continue
				}
				row.Values[col.key] = v
			}
		}
		row.HolderEmail = holderCell

		switch {
		case row.Name == "":
			rowErr(rec.line, "Name is required.")
		case utf8.RuneCountInString(row.Name) > maxImportNameLen:
			rowErr(rec.line, "Name is longer than %d characters.", maxImportNameLen)
		}
		for _, p := range []*string{row.Location, row.Description} {
			if p != nil && utf8.RuneCountInString(*p) > maxImportValueLen {
				rowErr(rec.line, "Location and Description can be at most %d characters.", maxImportValueLen)
			}
		}
		if row.Code != "" {
			switch {
			case !importCodePattern.MatchString(row.Code):
				rowErr(rec.line, "The code %q isn't valid. Codes are 6 letters and digits, without 0, 1, I, or O, for example K7M-4QX.", FormatInventoryCode(row.Code))
			default:
				if first, dup := seenCode[row.Code]; dup {
					rowErr(rec.line, "The code %s is already used on line %d.", FormatInventoryCode(row.Code), first)
				} else {
					seenCode[row.Code] = rec.line
				}
			}
		}
		if row.HolderEmail != "" && !allowHolders {
			if !holderIgnoredWarned {
				rowWarn(0, "The holder column was ignored. Only inventory admins can assign holders in an import.")
				holderIgnoredWarned = true
			}
			row.HolderEmail = ""
		}
		if row.HolderEmail != "" {
			if addr, err := mail.ParseAddress(row.HolderEmail); err != nil || addr.Address != row.HolderEmail {
				rowErr(rec.line, "%q isn't a valid email address.", row.HolderEmail)
			}
		}
		rows = append(rows, row)
	}

	// Match codes to existing items.
	var codes []string
	for code := range seenCode {
		codes = append(codes, code)
	}
	type existing struct {
		id       string
		archived bool
		holder   *string
		metadata map[string]string
	}
	byCode := map[string]existing{}
	if len(codes) > 0 {
		dbRows, err := pool.Query(ctx,
			`SELECT id, code, archived_at IS NOT NULL, current_holder_user_id, metadata FROM inventory.items WHERE code = ANY($1)`, codes)
		if err != nil {
			return nil, err
		}
		for dbRows.Next() {
			var e existing
			var code string
			if err := dbRows.Scan(&e.id, &code, &e.archived, &e.holder, &e.metadata); err != nil {
				dbRows.Close()
				return nil, err
			}
			byCode[code] = e
		}
		dbRows.Close()
		if err := dbRows.Err(); err != nil {
			return nil, err
		}
	}

	for _, row := range rows {
		pr := InventoryImportPlanRow{InventoryImportRow: row}
		var currentHolder *string
		var currentMeta map[string]string
		if row.Code != "" {
			if e, ok := byCode[row.Code]; ok {
				if e.archived {
					rowErr(row.Line, "The item with code %s is archived. Restore it before importing changes to it.", FormatInventoryCode(row.Code))
				}
				pr.ItemID, currentHolder, currentMeta = e.id, e.holder, e.metadata
			}
		}
		// A dropdown field takes only its listed choices, spelled as listed. A value an item already has is left
		// alone, so an export imports back even where it holds a value that is no longer in the list.
		for key, v := range row.Values {
			def, ok := defByKey[key]
			if !ok || !def.IsSelect() || currentMeta[key] == v {
				continue
			}
			canonical, err := def.CheckValue(v)
			if err != nil {
				rowErr(row.Line, "%q isn't one of the choices for %s: %s.", v, def.Label, strings.Join(def.Options, ", "))
				continue
			}
			pr.Values[key] = canonical
		}
		if row.HolderEmail != "" {
			user, err := FindUserForClaimEmail(ctx, pool, row.HolderEmail)
			switch {
			case errors.Is(err, ErrNotFound):
				rowWarn(row.Line, "No member has the email %s, so the holder was left as it is.", row.HolderEmail)
			case err != nil:
				return nil, err
			case currentHolder == nil || *currentHolder != user.ID:
				pr.HolderUserID = user.ID
			}
		}
		plan.Rows = append(plan.Rows, pr)
	}
	return plan, nil
}

func columnLabel(header []string, i int) string {
	if i < len(header) {
		return strings.TrimSpace(header[i])
	}
	return ""
}

// ApplyInventoryImport carries out a plan in one transaction: either everything is saved or nothing is. New
// custom fields are created, rows with an existing code update that item (blank cells leave values alone; the
// import never clears anything), rows without one create items with generated codes, and a matched holder
// takes possession, recorded as an admin assignment. Nobody is messaged. A plan with errors is refused.
func ApplyInventoryImport(ctx context.Context, pool *pgxpool.Pool, plan *InventoryImportPlan) (*InventoryImportResult, error) {
	if len(plan.Errors) > 0 {
		return nil, ErrInventoryImportInvalid
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	result := &InventoryImportResult{}
	for _, f := range plan.NewFields {
		tag, err := tx.Exec(ctx, `
			INSERT INTO inventory.field_definitions (key, label, sort_order)
			SELECT $1, $2, COALESCE(MAX(sort_order), 0) + 1 FROM inventory.field_definitions
			ON CONFLICT (key) DO NOTHING`, f.Key, f.Label)
		if err != nil {
			return nil, err
		}
		result.FieldsCreated += int(tag.RowsAffected())
	}

	// Generated codes for the rows that need one, none of which may collide with an existing code or each other.
	generated, err := generateImportCodes(ctx, pool, plan)
	if err != nil {
		return nil, err
	}

	note := "Imported from CSV"
	for i, row := range plan.Rows {
		values, err := json.Marshal(row.Values)
		if err != nil {
			return nil, err
		}
		itemID := row.ItemID
		if itemID == "" {
			code := row.Code
			if code == "" {
				code = generated[i]
			}
			cadence := row.VerifyEveryDays
			if cadence == nil {
				d := importDefaultVerifyDays
				cadence = &d
			}
			if err := tx.QueryRow(ctx, `
				INSERT INTO inventory.items (name, description, location, code, verify_every_days, metadata)
				VALUES ($1, $2, $3, $4, $5, $6::jsonb) RETURNING id`,
				row.Name, row.Description, row.Location, code, cadence, string(values)).Scan(&itemID); err != nil {
				return nil, fmt.Errorf("line %d: create item: %w", row.Line, err)
			}
			result.Created++
			result.CreatedItems = append(result.CreatedItems, InventoryImportCreated{Name: row.Name, Code: code})
		} else {
			tag, err := tx.Exec(ctx, `
				UPDATE inventory.items
				SET name = $2,
				    description = COALESCE($3::text, description),
				    location = COALESCE($4::text, location),
				    verify_every_days = COALESCE($5::int, verify_every_days),
				    metadata = metadata || $6::jsonb,
				    updated_at = NOW()
				WHERE id = $1 AND archived_at IS NULL`,
				itemID, row.Name, row.Description, row.Location, row.VerifyEveryDays, string(values))
			if err != nil {
				return nil, fmt.Errorf("line %d: update item: %w", row.Line, err)
			}
			if tag.RowsAffected() == 0 {
				return nil, fmt.Errorf("line %d: the item changed or was archived since the preview", row.Line)
			}
			result.Updated++
		}

		for _, name := range row.Accessories {
			tag, err := tx.Exec(ctx, `
				INSERT INTO inventory.accessories (item_id, name) VALUES ($1, $2)
				ON CONFLICT DO NOTHING`, itemID, name)
			if err != nil {
				return nil, fmt.Errorf("line %d: add accessory: %w", row.Line, err)
			}
			result.AccessoriesAdded += int(tag.RowsAffected())
		}

		if row.HolderUserID == "" {
			continue
		}
		holder, archived, err := lockInventoryItem(ctx, tx, itemID)
		if err != nil {
			return nil, err
		}
		if archived || (holder != nil && *holder == row.HolderUserID) {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO inventory.custody_events
			    (item_id, user_id, previous_holder_user_id, source, status, note, applied_at)
			VALUES ($1, $2, $3, 'admin', 'applied', $4, NOW())`, itemID, row.HolderUserID, holder, note); err != nil {
			return nil, err
		}
		if err := setInventoryHolder(ctx, tx, itemID, &row.HolderUserID); err != nil {
			return nil, err
		}
		result.HoldersSet++
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

// generateImportCodes returns a fresh code for every row that creates an item without one, keyed by row index.
func generateImportCodes(ctx context.Context, pool *pgxpool.Pool, plan *InventoryImportPlan) (map[int]string, error) {
	generated := map[int]string{}
	used := map[string]bool{}
	for _, row := range plan.Rows {
		if row.Code != "" {
			used[row.Code] = true
		}
	}
	for i, row := range plan.Rows {
		if row.ItemID != "" || row.Code != "" {
			continue
		}
		for attempt := 0; ; attempt++ {
			if attempt >= 20 {
				return nil, ErrInventoryCodeTaken
			}
			code := NewInventoryCode()
			if used[code] {
				continue
			}
			var taken bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory.items WHERE code = $1)`, code).Scan(&taken); err != nil {
				return nil, err
			}
			if taken {
				continue
			}
			used[code] = true
			generated[i] = code
			break
		}
	}
	return generated, nil
}

// splitImportAccessories turns an Accessories cell, a comma-separated list, into names: trimmed, blanks
// dropped, repeats (any case) dropped.
func splitImportAccessories(cell string) ([]string, error) {
	var names []string
	seen := map[string]bool{}
	for _, part := range strings.Split(cell, ",") {
		name := strings.TrimSpace(part)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		if utf8.RuneCountInString(name) > MaxInventoryAccessoryNameLen {
			return nil, fmt.Errorf("An accessory name is longer than %d characters.", MaxInventoryAccessoryNameLen)
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	if len(names) > MaxInventoryAccessories {
		return nil, fmt.Errorf("An item can have at most %d accessories.", MaxInventoryAccessories)
	}
	return names, nil
}
