package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/rivo/uniseg"
)

func TestBinaryResultPreviewUsesHexWithoutLosingRawBytes(t *testing.T) {
	value := []byte{0, 1, 2, '\n', '\x1b', 255, 'A'}
	preview, _ := formatCellValueForDatabaseType(value, "BLOB")
	if want := "BLOB 7 B: 0x0001020a1bff41"; preview != want {
		t.Fatalf("binary preview = %q, want %q", preview, want)
	}
	reference := newResultCellReferenceForDatabaseType(value, preview, "BLOB")
	value[0] = 'X'
	wantBytes := []byte{0, 1, 2, '\n', '\x1b', 255, 'A'}
	if got, ok := reference.rawValue.([]byte); !ok || !bytes.Equal(got, wantBytes) || reference.value != string(wantBytes) {
		t.Fatal("binary preview lost or aliased the complete raw value")
	}
	if reference.isNull || reference.truncated {
		t.Fatal("complete binary preview was marked NULL or truncated")
	}
}

func TestEmptyBinaryResultPreviewIsDistinctFromNullAndEmptyText(t *testing.T) {
	binary, _ := formatCellValueForDatabaseType([]byte{}, "BYTEA")
	null, _ := formatCellValueForDatabaseType(nil, "BYTEA")
	text, _ := formatCellValueForDatabaseType([]byte{}, "TEXT")
	if binary != "BLOB 0 B (empty)" || null != "NULL" || text != "" {
		t.Fatalf("empty previews = binary %q, NULL %q, text %q", binary, null, text)
	}
}

func TestLargeBinaryResultPreviewIsBounded(t *testing.T) {
	value := bytes.Repeat([]byte{255}, 1024*1024)
	preview, _ := formatCellValueForDatabaseType(value, "BYTEA")
	want := "BLOB 1048576 B: 0x" + strings.Repeat("ff", maxBinaryPreviewLen) + "..."
	if preview != want || len(preview) > maxCellPreviewRunes {
		t.Fatalf("large binary preview = %q, want bounded %q", preview, want)
	}
	reference := newResultCellReferenceForDatabaseType(value, preview, "BYTEA")
	if !reference.truncated || len(reference.value) != len(value) {
		t.Fatal("large binary preview lost its truncation marker or full value")
	}
}

func TestResultTextPreviewPreservesGraphemeBoundaries(t *testing.T) {
	longCluster := "a" + strings.Repeat("\u0301", 400)
	tests := []struct {
		name      string
		value     string
		limit     int
		want      string
		truncated bool
	}{
		{name: "empty", limit: 5},
		{name: "exact limit", value: "क्षक्षक्षक्षक्ष", limit: 5, want: "क्षक्षक्षक्षक्ष"},
		{name: "Indic conjunct", value: "क्षक्षक्षक्षक्षक्ष", limit: 5, want: "क्षक्ष...", truncated: true},
		{name: "emoji ZWJ", value: strings.Repeat("👩‍💻", 7), limit: 6, want: "👩‍💻👩‍💻👩‍💻...", truncated: true},
		{name: "regional indicators", value: strings.Repeat("🇮🇳", 7), limit: 6, want: "🇮🇳🇮🇳🇮🇳...", truncated: true},
		{name: "cluster crosses prefix", value: longCluster + "BCDE", limit: 1, want: longCluster, truncated: true},
		{name: "one large cluster", value: longCluster, limit: 1, want: longCluster},
		{name: "zero limit", value: "hello", want: "", truncated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, truncated := resultTextPreview(tt.value, tt.limit)
			if got != tt.want || truncated != tt.truncated {
				t.Fatalf("preview = (%q, %v), want (%q, %v)", got, truncated, tt.want, tt.truncated)
			}
		})
	}
}

func TestResultTextPreviewPreservesMalformedUTF8Behavior(t *testing.T) {
	for _, value := range []string{"e\u0301\xffabcdef", "\xffe\u0301abcde", "ab\xff\xfeक्ष👩‍💻XYZ"} {
		for limit := 1; limit <= 7; limit++ {
			got, _ := resultTextPreview(value, limit)
			if want := legacyResultTextPreview(value, limit); got != want {
				t.Fatalf("preview(%q, %d) = %q, want legacy %q", value, limit, got, want)
			}
		}
	}
}

func TestLargeResultValueKeepsLosslessReference(t *testing.T) {
	value := strings.Repeat("界", 350000)
	preview, truncated := resultTextPreview(value, maxCellPreviewRunes)
	if !truncated || preview != strings.Repeat("界", maxCellPreviewRunes-3)+"..." {
		t.Fatalf("large value preview = %q, truncated = %v", preview, truncated)
	}
	reference := newResultCellReferenceForDatabaseType(value, preview, "TEXT")
	if reference.value != value || reference.rawValue != value || !reference.truncated {
		t.Fatal("large value lost its complete copy/export value")
	}
}

// Keep the previous implementation as a short-input behavior oracle, not a
// benchmark path: its repeated full-string segmentation is prohibitively slow
// even for a few kilobytes.
func legacyResultTextPreview(value string, limit int) string {
	if limit <= 0 || value == "" {
		return ""
	}
	if uniseg.GraphemeClusterCount(value) <= limit {
		return value
	}
	var clusters []string
	for len(value) > 0 {
		cluster, rest, _, _ := uniseg.FirstGraphemeClusterInString(value, -1)
		clusters = append(clusters, cluster)
		value = rest
	}
	if limit <= 3 {
		return strings.Join(clusters[:limit], "")
	}
	return strings.Join(clusters[:limit-3], "") + "..."
}

var resultTextPreviewBenchmarkSink string

func BenchmarkResultTextPreview(b *testing.B) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "120B", value: strings.Repeat("x", 120)},
		{name: "512B", value: strings.Repeat("x", 512)},
		{name: "1KiB", value: strings.Repeat("x", 1024)},
		{name: "1MiB", value: strings.Repeat("x", 1024*1024)},
		{name: "4MiB", value: strings.Repeat("x", 4*1024*1024)},
		{name: "1MiB_Unicode", value: strings.Repeat("界", 350000)},
	}
	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				resultTextPreviewBenchmarkSink, _ = resultTextPreview(tt.value, maxCellPreviewRunes)
			}
		})
	}
}

// Measure actual terminal drawing separately from query/preview preparation.
// The row count remains fixed while cell length changes, so regressions in
// visible-cell layout are not mistaken for database or pagination latency.
func BenchmarkResultTableDraw(b *testing.B) {
	for _, cellRunes := range []int{16, 100} {
		name := "short_cells"
		if cellRunes == 100 {
			name = "preview_limit_cells"
		}
		b.Run(name, func(b *testing.B) {
			screen := tcell.NewSimulationScreen("UTF-8")
			if err := screen.Init(); err != nil {
				b.Fatal(err)
			}
			defer screen.Fini()
			screen.SetSize(120, 36)
			table := tview.NewTable().SetBorders(true).SetSelectable(true, true).SetFixed(1, 0)
			table.SetRect(0, 0, 120, 36)
			for row := 0; row <= 100; row++ {
				for col := 0; col < 8; col++ {
					table.SetCell(row, col, tview.NewTableCell(strings.Repeat("x", cellRunes)).SetMaxWidth(30))
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				table.Draw(screen)
			}
		})
	}
}
