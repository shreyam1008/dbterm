package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func TestIsSelectableTableLabelIgnoresDecorativeRows(t *testing.T) {
	cases := []struct {
		name  string
		label string
		want  bool
	}{
		{name: "plain table", label: "public.users", want: true},
		{name: "section header", label: "[#6c7086]── Views (2) ──[-]", want: false},
		{name: "indented styled object", label: "  [#a6adc8]◈[-] reporting_view", want: false},
		{name: "empty decorative", label: "   [gray]No tables found[-]", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSelectableTableLabel(tc.label); got != tc.want {
				t.Fatalf("isSelectableTableLabel(%q) = %v, want %v", tc.label, got, tc.want)
			}
		})
	}
}

func TestTableSearchMatchRangeIsCaseInsensitive(t *testing.T) {
	start, end, matched := tableSearchMatchRange("audit.UserProfiles", "user")
	if !matched {
		t.Fatal("expected a match")
	}
	if got := string([]rune("audit.UserProfiles")[start:end]); got != "User" {
		t.Fatalf("matched text = %q, want %q", got, "User")
	}
}

func TestTableTypeAheadSelectsFirstMatchAndClearsOnEnter(t *testing.T) {
	list := tview.NewList().ShowSecondaryText(false)
	list.AddItem("[#6c7086]── Schema (public) ──[-]", "", 0, nil)
	list.AddItem("audit_users", "", 0, nil)
	list.AddItem("user_profiles", "", 0, nil)

	app := &App{
		tables: list,
		tableIdentifiers: map[int]string{
			1: "audit_users",
			2: "user_profiles",
		},
		tableCount: 2,
	}

	for _, r := range "USER" {
		if got := app.handleTableListInput(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone)); got != nil {
			t.Fatalf("type-ahead event %q was not consumed", r)
		}
	}

	if got := list.GetCurrentItem(); got != 1 {
		t.Fatalf("selected index = %d, want first matching index 1", got)
	}
	label, _ := list.GetItemText(1)
	if !strings.Contains(label, "[black:#f9e2af:b]user[-:-:-]") {
		t.Fatalf("matching letters are not highlighted: %q", label)
	}
	if !strings.Contains(list.GetTitle(), "USER") {
		t.Fatalf("title does not show active search: %q", list.GetTitle())
	}

	enter := tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
	if got := app.handleTableListInput(enter); got != enter {
		t.Fatal("Enter should continue to the list after clearing a successful search")
	}
	if app.tableSearch != "" {
		t.Fatalf("search was not cleared: %q", app.tableSearch)
	}
	label, _ = list.GetItemText(1)
	if label != "audit_users" {
		t.Fatalf("highlight was not cleared: %q", label)
	}
}

func TestTableTypeAheadEnterDoesNotOpenWhenNothingMatches(t *testing.T) {
	list := tview.NewList().ShowSecondaryText(false)
	list.AddItem("users", "", 0, nil)
	app := &App{
		tables:           list,
		tableIdentifiers: map[int]string{0: "users"},
		tableCount:       1,
	}

	for _, r := range "missing" {
		app.handleTableListInput(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone))
	}
	if got := app.handleTableListInput(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)); got != nil {
		t.Fatal("Enter should be consumed when the search has no match")
	}
	if app.tableSearch != "" {
		t.Fatalf("search was not cleared: %q", app.tableSearch)
	}
}
