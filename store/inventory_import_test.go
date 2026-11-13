package store

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"commons/internal/testhelpers"
)

func TestInventoryFieldKey(t *testing.T) {
	cases := map[string]string{
		"Serial number":      "serial_number",
		"  Serial   NUMBER":  "serial_number",
		"Purchase-date (US)": "purchase_date_us",
		"Room 12B":           "room_12b",
		"!!!":                "",
		"":                   "",
	}
	for in, want := range cases {
		assert.Equal(t, want, InventoryFieldKey(in), "%q", in)
	}
}

func importMessages(issues []InventoryImportIssue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, fmt.Sprintf("%d: %s", i.Line, i.Message))
	}
	return out
}

func hasIssue(issues []InventoryImportIssue, line int, contains string) bool {
	for _, i := range issues {
		if i.Line == line && strings.Contains(i.Message, contains) {
			return true
		}
	}
	return false
}

func TestPlanInventoryImportParsing(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	plan := func(csv string) *InventoryImportPlan {
		t.Helper()
		p, err := PlanInventoryImport(ctx, pool, []byte(csv), true)
		require.NoError(t, err)
		return p
	}

	t.Run("a bare Name column is enough", func(t *testing.T) {
		p := plan("Name\nProjector\n")
		require.Empty(t, p.Errors)
		require.Len(t, p.Rows, 1)
		assert.Equal(t, "Projector", p.Rows[0].Name)
		assert.Equal(t, "", p.Rows[0].Code)
		assert.Equal(t, 1, p.Creates())
		assert.Equal(t, 0, p.Updates())
		assert.Equal(t, 2, p.Rows[0].Line, "line numbers count the header as line 1")
	})

	t.Run("headings are matched without regard to case, spacing, or alias", func(t *testing.T) {
		p := plan("  NAME ,LOCATION,Description,Verify Every (Days)\nCamera,Studio,Mirrorless,14\n")
		require.Empty(t, p.Errors, importMessages(p.Errors))
		r := p.Rows[0]
		assert.Equal(t, "Studio", *r.Location)
		assert.Equal(t, "Mirrorless", *r.Description)
		assert.Equal(t, 14, *r.VerifyEveryDays)

		p = plan("name,verify every days\nA,3\n")
		assert.Equal(t, 3, *p.Rows[0].VerifyEveryDays)
		p = plan("name,verify every\nA,5\n")
		assert.Equal(t, 5, *p.Rows[0].VerifyEveryDays)
	})

	t.Run("a spreadsheet's byte order mark, Windows line endings, and semicolons are fine", func(t *testing.T) {
		bom := string([]byte{0xEF, 0xBB, 0xBF})
		p := plan(bom + "Name,Location\r\nProjector,Closet\r\nCamera,Studio\r\n")
		require.Empty(t, p.Errors, importMessages(p.Errors))
		assert.Len(t, p.Rows, 2)
		assert.Equal(t, "Closet", *p.Rows[0].Location)

		p = plan("Name;Location;Description\nProjector;Closet;\"Epson; 1080p\"\n")
		require.Empty(t, p.Errors, importMessages(p.Errors))
		assert.Equal(t, "Closet", *p.Rows[0].Location)
		assert.Equal(t, "Epson; 1080p", *p.Rows[0].Description)
	})

	t.Run("quoted cells keep commas and newlines", func(t *testing.T) {
		p := plan("Name,Description\n\"Cable, 10ft\",\"line one\nline two\"\n")
		require.Empty(t, p.Errors, importMessages(p.Errors))
		assert.Equal(t, "Cable, 10ft", p.Rows[0].Name)
		assert.Equal(t, "line one\nline two", *p.Rows[0].Description)
	})

	t.Run("blank rows are skipped and short or long rows are tolerated", func(t *testing.T) {
		p := plan("Name,Location,Description\n\nA\n,,\nB,Shelf,Desc,extra,cells\n  ,  ,  \n")
		require.Empty(t, p.Errors, importMessages(p.Errors))
		require.Len(t, p.Rows, 2)
		assert.Equal(t, "A", p.Rows[0].Name)
		assert.Nil(t, p.Rows[0].Location)
		assert.Equal(t, "B", p.Rows[1].Name)
		assert.Equal(t, "Shelf", *p.Rows[1].Location)
	})

	t.Run("columns the grid export adds are accepted and ignored, as are unlabeled ones", func(t *testing.T) {
		p := plan("Name,Code,Holder,Last verified,Status,Claim pending,,Location\nProjector,K7M-4QX,Alice,2026-01-02,Archived,Yes,junk,Closet\n")
		require.Empty(t, p.Errors, importMessages(p.Errors))
		assert.Empty(t, p.Warnings)
		assert.Empty(t, p.NewFields)
		assert.Equal(t, "K7M4QX", p.Rows[0].Code)
		assert.Equal(t, "Closet", *p.Rows[0].Location)
	})

	t.Run("the apostrophe the export adds before formula-like cells is removed", func(t *testing.T) {
		p := plan("Name,Description\n'=SUM(A1),'+1\n'plain,it's fine\n")
		require.Empty(t, p.Errors, importMessages(p.Errors))
		assert.Equal(t, "=SUM(A1)", p.Rows[0].Name)
		assert.Equal(t, "+1", *p.Rows[0].Description)
		assert.Equal(t, "'plain", p.Rows[1].Name, "only an apostrophe before a formula character is the export's")
		assert.Equal(t, "it's fine", *p.Rows[1].Description)
	})

	t.Run("codes are normalized", func(t *testing.T) {
		p := plan("Name,Code\nA,k7m-4qx\nB,  cam 222 \n")
		require.Empty(t, p.Errors, importMessages(p.Errors))
		assert.Equal(t, "K7M4QX", p.Rows[0].Code)
		assert.Equal(t, "CAM222", p.Rows[1].Code)
	})

	t.Run("a header with no rows is a valid, empty import", func(t *testing.T) {
		p := plan("Name,Code\n")
		assert.Empty(t, p.Errors)
		assert.Empty(t, p.Rows)
	})
}

func TestPlanInventoryImportErrors(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	plan := func(csv string) *InventoryImportPlan {
		t.Helper()
		p, err := PlanInventoryImport(ctx, pool, []byte(csv), true)
		require.NoError(t, err)
		return p
	}

	t.Run("file-level problems", func(t *testing.T) {
		assert.True(t, hasIssue(plan("").Errors, 0, "file is empty"))
		assert.True(t, hasIssue(plan("Code,Location\nK7M4QX,Closet\n").Errors, 0, "no Name column"))
		assert.True(t, hasIssue(plan("Name,name\nA,B\n").Errors, 0, "both name"))
		assert.True(t, hasIssue(plan("Name,Location,LOCATION\nA,B,C\n").Errors, 0, "both location"))
		assert.True(t, hasIssue(plan("Name,Name\nA,B\n").Errors, 0, "Keep one"))
		assert.True(t, hasIssue(plan("Name,Description\n\"unterminated,x\n").Errors, 0, "couldn't be read"))

		invalid := append([]byte("Name\n"), 0xff, 0xfe, '\n')
		p, err := PlanInventoryImport(ctx, pool, invalid, true)
		require.NoError(t, err)
		assert.True(t, hasIssue(p.Errors, 0, "UTF-8"))

		huge := []byte("Name\n" + strings.Repeat("x", MaxInventoryImportBytes+1))
		p, err = PlanInventoryImport(ctx, pool, huge, true)
		require.NoError(t, err)
		assert.True(t, hasIssue(p.Errors, 0, "larger than"))

		var many strings.Builder
		many.WriteString("Name\n")
		for i := 0; i <= MaxInventoryImportRows; i++ {
			many.WriteString("x\n")
		}
		p, err = PlanInventoryImport(ctx, pool, []byte(many.String()), true)
		require.NoError(t, err)
		assert.True(t, hasIssue(p.Errors, 0, "more than 5000 rows"))
	})

	t.Run("row-level problems name the line and block the import", func(t *testing.T) {
		p := plan("Name,Code,Verify every (days)\n" +
			",K7M4QX,\n" + // 2: no name
			"A,K7M0QX,\n" + // 3: code uses a 0
			"B,ABC,\n" + // 4: code too short
			"C,K7M4QX,\n" + // 5: duplicate of line 2's code
			"D,,0\n" + // 6: bad cadence
			"E,,-3\n" + // 7
			"F,,abc\n" + // 8
			"G,,1.5\n" + // 9
			strings.Repeat("N", 201) + ",,\n") // 10: name too long
		assert.True(t, hasIssue(p.Errors, 2, "Name is required"))
		assert.True(t, hasIssue(p.Errors, 3, "isn't valid"))
		assert.True(t, hasIssue(p.Errors, 4, "isn't valid"))
		assert.True(t, hasIssue(p.Errors, 5, "already used on line 2"))
		for _, line := range []int{6, 7, 8, 9} {
			assert.True(t, hasIssue(p.Errors, line, "Verify every must be a whole number"), "line %d", line)
		}
		assert.True(t, hasIssue(p.Errors, 10, "Name is longer than 200"))
	})

	t.Run("values over the length limit", func(t *testing.T) {
		long := strings.Repeat("x", maxImportValueLen+1)
		p := plan("Name,Description,Location\nA," + long + ",\nB,," + long + "\n")
		assert.True(t, hasIssue(p.Errors, 2, "at most 2000"))
		assert.True(t, hasIssue(p.Errors, 3, "at most 2000"))
	})

	t.Run("an archived item can't be updated", func(t *testing.T) {
		it, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Retired"}, "ARC222")
		require.NoError(t, err)
		require.NoError(t, ArchiveInventoryItem(ctx, pool, it.ID))
		p := plan("Name,Code\nRetired again,ARC-222\n")
		assert.True(t, hasIssue(p.Errors, 2, "is archived"))
	})
}

func TestPlanInventoryImportMatching(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)
	plan := func(csv string) *InventoryImportPlan {
		t.Helper()
		p, err := PlanInventoryImport(ctx, pool, []byte(csv), true)
		require.NoError(t, err)
		return p
	}

	existing, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Projector"}, "K7M4QX")
	require.NoError(t, err)

	t.Run("a known code updates that item, an unknown valid code is created with it, a blank code is generated", func(t *testing.T) {
		p := plan("Name,Code\nProjector 2,K7M-4QX\nBrand new,NEW222\nGenerated,\n")
		require.Empty(t, p.Errors, importMessages(p.Errors))
		require.Len(t, p.Rows, 3)
		assert.Equal(t, existing.ID, p.Rows[0].ItemID)
		assert.Equal(t, "", p.Rows[1].ItemID)
		assert.Equal(t, "NEW222", p.Rows[1].Code)
		assert.Equal(t, "", p.Rows[2].Code)
		assert.Equal(t, 2, p.Creates())
		assert.Equal(t, 1, p.Updates())
	})

	t.Run("custom field columns", func(t *testing.T) {
		_, err := CreateInventoryFieldDefinition(ctx, pool, "serial", "Serial")
		require.NoError(t, err)
		old, err := CreateInventoryFieldDefinition(ctx, pool, "color", "Color")
		require.NoError(t, err)
		require.NoError(t, UpdateInventoryFieldDefinition(ctx, pool, old.ID, "Color", false))

		p := plan("Name,serial,Warranty Expires,COLOR\nA,SN-1,2027-01-01,red\nB,,,\n")
		require.Empty(t, p.Errors, importMessages(p.Errors))

		assert.Equal(t, []InventoryImportField{{Key: "warranty_expires", Label: "Warranty Expires"}}, p.NewFields,
			"an unknown heading proposes a new field, named from the heading")
		assert.Equal(t, map[string]string{"serial": "SN-1", "warranty_expires": "2027-01-01"}, p.Rows[0].Values,
			"an existing field is matched by name regardless of case; an inactive one is skipped")
		assert.Empty(t, p.Rows[1].Values, "blank cells import nothing")
		assert.True(t, hasIssue(p.Warnings, 1, `"Color" is inactive`))
	})

	t.Run("headings that can't name a field, or name the same one twice", func(t *testing.T) {
		assert.True(t, hasIssue(plan("Name,!!!\nA,b\n").Errors, 0, "can't be used as a field name"))
		assert.True(t, hasIssue(plan("Name,Serial,SERIAL\nA,b,c\n").Errors, 0, "same custom field"))
		assert.True(t, hasIssue(plan("Name,Serial Number,serial-number\nA,b,c\n").Errors, 0, "same custom field"))
	})
}

func TestPlanInventoryImportHolders(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	alice := invUser(t, pool, "Alice")
	invExec(t, pool, `UPDATE users SET email = 'alice@example.com' WHERE id = $1`, alice)
	bob := invUser(t, pool, "Bob")
	invExec(t, pool, `INSERT INTO user_identities (user_id, provider, external_id, external_email)
		VALUES ($1, 'slack', 'UB', 'bob.slack@example.com')`, bob)

	held, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Held"}, "HLD222")
	require.NoError(t, err)
	invClaim(t, pool, held.ID, alice)

	plan := func(csv string, allow bool) *InventoryImportPlan {
		t.Helper()
		p, err := PlanInventoryImport(ctx, pool, []byte(csv), allow)
		require.NoError(t, err)
		return p
	}

	t.Run("without permission the holder column is ignored, with one warning", func(t *testing.T) {
		p := plan("Name,Holder email\nA,alice@example.com\nB,bob.slack@example.com\n", false)
		require.Empty(t, p.Errors)
		assert.Len(t, p.Warnings, 1)
		assert.True(t, hasIssue(p.Warnings, 0, "Only inventory admins"))
		assert.Equal(t, 0, p.HolderChanges())
		assert.Equal(t, "", p.Rows[0].HolderEmail)
	})

	t.Run("an email matches a member by their primary or Slack profile email", func(t *testing.T) {
		p := plan("Name,Holder email\nA,ALICE@example.com\nB,bob.slack@example.com\nC,\n", true)
		require.Empty(t, p.Errors, importMessages(p.Errors))
		assert.Equal(t, alice, p.Rows[0].HolderUserID)
		assert.Equal(t, bob, p.Rows[1].HolderUserID)
		assert.Equal(t, "", p.Rows[2].HolderUserID, "a blank cell leaves the holder alone")
		assert.Equal(t, 2, p.HolderChanges())
	})

	t.Run("an email nobody has is a warning, and the row is still imported", func(t *testing.T) {
		p := plan("Name,Holder email\nA,nobody@elsewhere.example\n", true)
		require.Empty(t, p.Errors)
		assert.True(t, hasIssue(p.Warnings, 2, "No member has the email nobody@elsewhere.example"))
		assert.Len(t, p.Rows, 1)
		assert.Equal(t, "", p.Rows[0].HolderUserID)
	})

	t.Run("something that isn't an email address is an error", func(t *testing.T) {
		p := plan("Name,Holder email\nA,not an email\nB,Alice <alice@example.com>\n", true)
		assert.True(t, hasIssue(p.Errors, 2, "isn't a valid email address"))
		assert.True(t, hasIssue(p.Errors, 3, "isn't a valid email address"))
	})

	t.Run("the current holder is not a change", func(t *testing.T) {
		p := plan("Name,Code,Holder email\nHeld,HLD222,alice@example.com\nHeld,,\n", true)
		require.Empty(t, p.Errors, importMessages(p.Errors))
		assert.Equal(t, held.ID, p.Rows[0].ItemID)
		assert.Equal(t, "", p.Rows[0].HolderUserID)
		assert.Equal(t, 0, p.HolderChanges())

		p = plan("Name,Code,Holder email\nHeld,HLD222,bob.slack@example.com\n", true)
		assert.Equal(t, bob, p.Rows[0].HolderUserID, "a different member takes over")
	})

	t.Run("the export's Holder column holds a name, so it is ignored unless a cell is an email", func(t *testing.T) {
		p := plan("Name,Holder\nA,Alice\nB,alice@example.com\n", true)
		require.Empty(t, p.Errors, importMessages(p.Errors))
		assert.Empty(t, p.Warnings, "a name in the Holder column is not a problem")
		assert.Equal(t, "", p.Rows[0].HolderUserID)
		assert.Equal(t, alice, p.Rows[1].HolderUserID)

		p = plan("Name,Holder,Holder email\nA,bob.slack@example.com,alice@example.com\n", true)
		assert.Equal(t, alice, p.Rows[0].HolderUserID, "Holder email wins when both columns exist")
	})
}

func TestApplyInventoryImport(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	alice := invUser(t, pool, "Alice")
	invExec(t, pool, `UPDATE users SET email = 'alice@example.com' WHERE id = $1`, alice)
	bob := invUser(t, pool, "Bob")
	invExec(t, pool, `UPDATE users SET email = 'bob@example.com' WHERE id = $1`, bob)

	loc, desc := "Closet", "Old description"
	days := 30
	existing, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Projector", Location: &loc, Description: &desc, VerifyEveryDays: &days}, "K7M4QX")
	require.NoError(t, err)
	_, err = CreateInventoryFieldDefinition(ctx, pool, "serial", "Serial")
	require.NoError(t, err)
	_, err = CreateInventoryFieldDefinition(ctx, pool, "color", "Color")
	require.NoError(t, err)
	require.NoError(t, SetInventoryItemMetadata(ctx, pool, existing.ID, map[string]string{"serial": "SN-OLD", "color": "black"}))
	invClaim(t, pool, existing.ID, alice)

	run := func(csv string) (*InventoryImportResult, error) {
		t.Helper()
		p, err := PlanInventoryImport(ctx, pool, []byte(csv), true)
		require.NoError(t, err)
		require.Empty(t, p.Errors, importMessages(p.Errors))
		return ApplyInventoryImport(ctx, pool, p)
	}

	res, err := run("Name,Code,Location,Description,Verify every (days),Holder email,Serial,Warranty,Notes\n" +
		"Projector 4K,K7M-4QX,,New description,,bob@example.com,SN-NEW,,\n" +
		"Camera,,Studio,,14,alice@example.com,SN-2,2027-01-01,\n" +
		"Tripod,TRP222,Shelf,,,,,,\"two\nlines\"\n")
	require.NoError(t, err)

	assert.Equal(t, 2, res.Created)
	assert.Equal(t, 1, res.Updated)
	assert.Equal(t, 2, res.HoldersSet)
	assert.Equal(t, 2, res.FieldsCreated, "Warranty and Notes are new")
	require.Len(t, res.CreatedItems, 2)

	t.Run("an existing item is updated, and blank cells change nothing", func(t *testing.T) {
		got, err := GetInventoryItem(ctx, pool, existing.ID)
		require.NoError(t, err)
		assert.Equal(t, "Projector 4K", got.Name)
		assert.Equal(t, "Closet", *got.Location, "a blank location is left alone")
		assert.Equal(t, "New description", *got.Description)
		assert.Equal(t, 30, *got.VerifyEveryDays, "a blank cadence is left alone")
		assert.Equal(t, "SN-NEW", got.Metadata["serial"])
		assert.Equal(t, "black", got.Metadata["color"], "a field with no column is untouched")
		assert.Equal(t, "K7M4QX", got.Code)
	})

	t.Run("the holder changes and is recorded as an admin assignment from the import", func(t *testing.T) {
		got, _ := GetInventoryItem(ctx, pool, existing.ID)
		assert.Equal(t, bob, *got.CurrentHolderUserID)
		evs, err := ListInventoryCustodyEvents(ctx, pool, existing.ID)
		require.NoError(t, err)
		assert.Equal(t, "admin", evs[0].Source)
		assert.Equal(t, "applied", evs[0].Status)
		assert.Equal(t, alice, *evs[0].PreviousHolderUserID)
		assert.Equal(t, "Imported from CSV", *evs[0].Note)
		assert.NotNil(t, got.LastVerifiedAt, "a new holder starts a verification cycle")
	})

	t.Run("new items get a generated or given code, a default cadence, and their values", func(t *testing.T) {
		byName := map[string]string{}
		for _, c := range res.CreatedItems {
			byName[c.Name] = c.Code
		}
		assert.Regexp(t, importCodePattern, byName["Camera"], "generated codes use the label alphabet")
		assert.Equal(t, "TRP222", byName["Tripod"])

		camera, err := GetInventoryItemByCode(ctx, pool, byName["Camera"])
		require.NoError(t, err)
		assert.Equal(t, "Studio", *camera.Location)
		assert.Equal(t, 14, *camera.VerifyEveryDays)
		assert.Equal(t, map[string]string{"serial": "SN-2", "warranty": "2027-01-01"}, camera.Metadata)
		assert.Equal(t, alice, *camera.CurrentHolderUserID)

		tripod, err := GetInventoryItemByCode(ctx, pool, "TRP222")
		require.NoError(t, err)
		assert.Equal(t, importDefaultVerifyDays, *tripod.VerifyEveryDays, "the form's default of 7 days")
		assert.Nil(t, tripod.CurrentHolderUserID)
		assert.Equal(t, "two\nlines", tripod.Metadata["notes"])
	})

	t.Run("new fields are created in column order", func(t *testing.T) {
		defs, err := ListInventoryFieldDefinitions(ctx, pool, true)
		require.NoError(t, err)
		var keys []string
		for _, d := range defs {
			keys = append(keys, d.Key)
		}
		assert.Equal(t, []string{"serial", "color", "warranty", "notes"}, keys)
		assert.Equal(t, "Warranty", defs[2].Label)
	})

	t.Run("importing the same file again updates instead of duplicating", func(t *testing.T) {
		before, err := ListInventoryItems(ctx, pool, true)
		require.NoError(t, err)
		res, err := run("Name,Code\nTripod,TRP222\nProjector 4K,K7M4QX\n")
		require.NoError(t, err)
		assert.Equal(t, 0, res.Created)
		assert.Equal(t, 2, res.Updated)
		after, _ := ListInventoryItems(ctx, pool, true)
		assert.Len(t, after, len(before))
	})

	t.Run("a holder who already holds the item records nothing", func(t *testing.T) {
		before, _ := ListInventoryCustodyEvents(ctx, pool, existing.ID)
		res, err := run("Name,Code,Holder email\nProjector 4K,K7M4QX,bob@example.com\n")
		require.NoError(t, err)
		assert.Equal(t, 0, res.HoldersSet)
		after, _ := ListInventoryCustodyEvents(ctx, pool, existing.ID)
		assert.Len(t, after, len(before))
	})

	t.Run("the import never clears a value", func(t *testing.T) {
		_, err := run("Name,Code,Location,Serial\nProjector 4K,K7M4QX,,\n")
		require.NoError(t, err)
		got, _ := GetInventoryItem(ctx, pool, existing.ID)
		assert.Equal(t, "Closet", *got.Location)
		assert.Equal(t, "SN-NEW", got.Metadata["serial"])
	})
}

func TestApplyInventoryImportIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	existing, err := CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Projector"}, "K7M4QX")
	require.NoError(t, err)
	itemsBefore, _ := ListInventoryItems(ctx, pool, true)

	plan, err := PlanInventoryImport(ctx, pool, []byte("Name,Code,Notes\nRenamed,K7M4QX,n1\nFresh,FRS222,n2\n"), true)
	require.NoError(t, err)
	require.Empty(t, plan.Errors)
	require.Len(t, plan.NewFields, 1)

	// Someone else takes the code between the preview and the confirmation.
	_, err = CreateInventoryItem(ctx, pool, InventoryItemInput{Name: "Sneaky"}, "FRS222")
	require.NoError(t, err)

	_, err = ApplyInventoryImport(ctx, pool, plan)
	require.Error(t, err)

	got, _ := GetInventoryItem(ctx, pool, existing.ID)
	assert.Equal(t, "Projector", got.Name, "the earlier row's update was rolled back")
	assert.Empty(t, got.Metadata)
	defs, _ := ListInventoryFieldDefinitions(ctx, pool, false)
	assert.Empty(t, defs, "so was the new field")
	itemsAfter, _ := ListInventoryItems(ctx, pool, true)
	assert.Len(t, itemsAfter, len(itemsBefore)+1, "only the sneaky item exists")
}

func TestApplyInventoryImportRefusesAPlanWithErrors(t *testing.T) {
	ctx := context.Background()
	pool := testhelpers.SetupTestDB(t)

	// The second row has a code but no name, so the whole file is refused, including the valid first row.
	plan, err := PlanInventoryImport(ctx, pool, []byte("Name,Code\nGood,\n,K7M4QX\n"), true)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Errors)

	_, err = ApplyInventoryImport(ctx, pool, plan)
	assert.ErrorIs(t, err, ErrInventoryImportInvalid)
	items, _ := ListInventoryItems(ctx, pool, true)
	assert.Empty(t, items, "nothing is saved, not even the valid row")
}
