package legislation

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"commons/store"
)

// ---- inferBillType ----

func TestInferBillTypeResolutions(t *testing.T) {
	cases := []struct {
		identifier string
	}{
		{"HR 1234"},
		{"SR 5678"},
		{"HCR 10"},
		{"SCR 20"},
		{"HJR 30"},
		{"SJR 40"},
		{"hr 1"},   // lowercase — function uppercases internally
		{"scr 5"},  // lowercase
		{"HJR123"}, // HasPrefix("HJR") matches even without space
	}
	for _, tc := range cases {
		t.Run(tc.identifier, func(t *testing.T) {
			assert.Equal(t, "resolution", inferBillType(tc.identifier))
		})
	}
}

func TestInferBillTypeBills(t *testing.T) {
	cases := []string{"HB 100", "SB 200", "AB 300", "HF 400", "SF 500", "1234"}
	for _, id := range cases {
		t.Run(id, func(t *testing.T) {
			assert.Equal(t, "bill", inferBillType(id))
		})
	}
}

// ---- latestAction ----

func TestLatestActionTopLevelFields(t *testing.T) {
	b := openStatesBill{
		LatestActionDescription: "Passed committee",
		LatestActionDate:        "2024-03-01",
	}
	desc, date := b.latestAction()
	assert.Equal(t, "Passed committee", desc)
	assert.Equal(t, "2024-03-01", date)
}

func TestLatestActionFallsBackToHighestOrder(t *testing.T) {
	b := openStatesBill{
		Actions: []struct {
			Description string `json:"description"`
			Date        string `json:"date"`
			Order       int    `json:"order"`
		}{
			{Description: "Introduced", Date: "2024-01-01", Order: 1},
			{Description: "Passed House", Date: "2024-02-01", Order: 3},
			{Description: "Referred to Senate", Date: "2024-01-15", Order: 2},
		},
	}
	desc, date := b.latestAction()
	assert.Equal(t, "Passed House", desc)
	assert.Equal(t, "2024-02-01", date)
}

func TestLatestActionEmptyBill(t *testing.T) {
	b := openStatesBill{}
	desc, date := b.latestAction()
	assert.Equal(t, "", desc)
	assert.Equal(t, "", date)
}

// ---- collectSubjectFilters ----

func TestCollectSubjectFilters(t *testing.T) {
	filters := []store.ImportFilter{
		{FilterType: "subject", Value: "Housing"},
		{FilterType: "matter_type", Value: "Ordinance"},
		{FilterType: "subject", Value: "Environment"},
	}
	got := collectSubjectFilters(filters)
	assert.Equal(t, []string{"Housing", "Environment"}, got)
}

func TestCollectSubjectFiltersEmpty(t *testing.T) {
	assert.Nil(t, collectSubjectFilters([]store.ImportFilter{}))
}

func TestCollectSubjectFiltersNoneOfType(t *testing.T) {
	filters := []store.ImportFilter{
		{FilterType: "matter_type", Value: "Ordinance"},
	}
	assert.Nil(t, collectSubjectFilters(filters))
}

// ---- openStatesBillMatchesFilters ----

func TestOpenStatesBillMatchesFiltersCaseInsensitive(t *testing.T) {
	bill := openStatesBill{Subjects: []string{"Housing", "Environment"}}
	filters := []store.ImportFilter{
		{FilterType: "subject", Value: "housing"},
	}
	assert.True(t, openStatesBillMatchesFilters(bill, filters))
}

func TestOpenStatesBillMatchesFiltersNoMatch(t *testing.T) {
	bill := openStatesBill{Subjects: []string{"Housing"}}
	filters := []store.ImportFilter{
		{FilterType: "subject", Value: "Transportation"},
	}
	assert.False(t, openStatesBillMatchesFilters(bill, filters))
}

func TestOpenStatesBillMatchesFiltersNoFilters(t *testing.T) {
	bill := openStatesBill{Subjects: []string{"Housing"}}
	assert.False(t, openStatesBillMatchesFilters(bill, nil))
}

func TestOpenStatesBillMatchesFiltersSkipsWrongType(t *testing.T) {
	// A matter_type filter should not match against bill subjects.
	bill := openStatesBill{Subjects: []string{"Housing"}}
	filters := []store.ImportFilter{
		{FilterType: "matter_type", Value: "Housing"},
	}
	assert.False(t, openStatesBillMatchesFilters(bill, filters))
}

func TestOpenStatesBillMatchesFiltersNoSubjects(t *testing.T) {
	bill := openStatesBill{Subjects: []string{}}
	filters := []store.ImportFilter{
		{FilterType: "subject", Value: "Housing"},
	}
	assert.False(t, openStatesBillMatchesFilters(bill, filters))
}

// ---- legistarMatterMatchesFilters ----

func TestLegistarMatterMatchesFiltersCaseInsensitive(t *testing.T) {
	m := legistarMatter{MatterTypeName: "Ordinance"}
	filters := []store.ImportFilter{
		{FilterType: "matter_type", Value: "ordinance"},
	}
	assert.True(t, legistarMatterMatchesFilters(m, filters))
}

func TestLegistarMatterMatchesFiltersNoMatch(t *testing.T) {
	m := legistarMatter{MatterTypeName: "Resolution"}
	filters := []store.ImportFilter{
		{FilterType: "matter_type", Value: "Ordinance"},
	}
	assert.False(t, legistarMatterMatchesFilters(m, filters))
}

func TestLegistarMatterMatchesFiltersSkipsWrongType(t *testing.T) {
	m := legistarMatter{MatterTypeName: "Ordinance"}
	filters := []store.ImportFilter{
		{FilterType: "subject", Value: "Ordinance"},
	}
	assert.False(t, legistarMatterMatchesFilters(m, filters))
}

func TestLegistarMatterMatchesFiltersNoFilters(t *testing.T) {
	m := legistarMatter{MatterTypeName: "Ordinance"}
	assert.False(t, legistarMatterMatchesFilters(m, nil))
}

// ---- parseLegistarDate ----

func TestParseLegistarDateRFC3339(t *testing.T) {
	got := parseLegistarDate("2024-03-15T10:30:00Z")
	assert.NotNil(t, got)
	assert.Equal(t, 2024, got.Year())
	assert.Equal(t, 3, int(got.Month()))
	assert.Equal(t, 15, got.Day())
}

func TestParseLegistarDateDateTimeNoTZ(t *testing.T) {
	got := parseLegistarDate("2024-03-15T10:30:00")
	assert.NotNil(t, got)
	assert.Equal(t, 2024, got.Year())
}

func TestParseLegistarDateDateOnly(t *testing.T) {
	got := parseLegistarDate("2024-03-15")
	assert.NotNil(t, got)
	assert.Equal(t, 2024, got.Year())
	assert.Equal(t, 3, int(got.Month()))
	assert.Equal(t, 15, got.Day())
}

func TestParseLegistarDateInvalid(t *testing.T) {
	assert.Nil(t, parseLegistarDate("not-a-date"))
}

func TestParseLegistarDateEmpty(t *testing.T) {
	assert.Nil(t, parseLegistarDate(""))
}
