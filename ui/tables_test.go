package ui

import "testing"

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
