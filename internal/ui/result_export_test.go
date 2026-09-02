package ui

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/config"
)

func TestPrepareSelectedResultExportUsesFullCellReferences(t *testing.T) {
	longValue := strings.Repeat("full-value-", 18)
	preciseFloat := 1.23456789012345
	app := &App{results: newResultTable()}
	app.results.SetCell(0, 0, tview.NewTableCell("DESCRIPTION ▲").SetReference("description"))
	app.results.SetCell(0, 1, tview.NewTableCell("AMOUNT").SetReference("amount"))
	app.results.SetCell(1, 0, tview.NewTableCell("full-value-...").SetReference(newResultCellReference(longValue, "full-value-...")))
	app.results.SetCell(1, 1, tview.NewTableCell("1.235").SetReference(newResultCellReference(preciseFloat, "1.235")))
	app.results.SetCell(2, 0, tview.NewTableCell("ignored").SetReference(newResultCellReference("ignored", "ignored")))
	app.results.SetCell(2, 1, tview.NewTableCell("9").SetReference(newResultCellReference(int64(9), "9")))
	if !app.setResultRowSelected(1, true) {
		t.Fatal("select first result row")
	}

	plan, err := app.prepareResultExportPlan(resultExportScopeOption{
		scope: resultExportSelectedRows,
		label: "Selected rows (1)",
	}, filepath.Join(t.TempDir(), "selected.csv"))
	if err != nil {
		t.Fatalf("prepare selected export: %v", err)
	}
	if len(plan.snapshot.rows) != 1 {
		t.Fatalf("snapshot rows = %d, want 1", len(plan.snapshot.rows))
	}
	if got := plan.snapshot.headers[0]; got != "description" {
		t.Fatalf("header = %q, want exact reference", got)
	}
	if got := plan.snapshot.rows[0][0]; got != longValue {
		t.Fatalf("long value was not preserved: got length %d, want %d", len(got), len(longValue))
	}
	if got, want := plan.snapshot.rows[0][1], fullCellValue(preciseFloat); got != want {
		t.Fatalf("float = %q, want full precision %q", got, want)
	}
}

func TestResultExportCellTextSupportsTypedAndLegacyReferences(t *testing.T) {
	tests := []struct {
		name string
		cell *tview.TableCell
		want string
	}{
		{
			name: "typed raw beats rounded preview",
			cell: tview.NewTableCell("9.877").SetReference(resultCellReference{
				value:    "stale",
				rawValue: 9.87654321098765,
			}),
			want: "9.87654321098765",
		},
		{
			name: "authoritative SQL null",
			cell: tview.NewTableCell("NULL").SetReference(resultCellReference{
				value:  "not-null",
				isNull: true,
			}),
			want: "NULL",
		},
		{
			name: "legacy full value",
			cell: tview.NewTableCell("short...").SetReference(resultCellReference{
				value: "complete legacy value",
			}),
			want: "complete legacy value",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resultExportCellText(test.cell); got != test.want {
				t.Fatalf("resultExportCellText() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWriteResultExportCSVAtomicPublishesPrivateCompleteFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "results.csv")
	plan := resultExportPlan{
		scope: resultExportCurrentPage,
		snapshot: resultExportSnapshot{
			headers: []string{"id", "value"},
			rows: [][]string{
				{"1", "comma,value"},
				{"2", "line\nbreak"},
			},
		},
	}

	rows, err := writeResultExportCSVAtomic(context.Background(), path, plan.csvProducer(), nil)
	if err != nil {
		t.Fatalf("write atomic CSV: %v", err)
	}
	if rows != 2 {
		t.Fatalf("rows = %d, want 2", rows)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat CSV: %v", err)
	}
	// Windows reports synthetic Unix permission bits; access is governed by
	// the destination directory's inherited ACL instead.
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("CSV permissions = %o, want 600", got)
	}

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open CSV: %v", err)
	}
	records, readErr := csv.NewReader(file).ReadAll()
	_ = file.Close()
	if readErr != nil {
		t.Fatalf("read CSV: %v", readErr)
	}
	if len(records) != 3 || records[1][1] != "comma,value" || records[2][1] != "line\nbreak" {
		t.Fatalf("unexpected CSV records: %#v", records)
	}
	assertNoResultExportTempFiles(t, directory)
}

func TestWriteResultExportCSVAtomicCleansTempOnCancel(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "canceled.csv")
	ctx, cancel := context.WithCancel(context.Background())
	producer := func(ctx context.Context, writer *csv.Writer, _ func(int)) (int, error) {
		if err := writer.Write([]string{"id"}); err != nil {
			return 0, err
		}
		cancel()
		return 0, ctx.Err()
	}

	_, err := writeResultExportCSVAtomic(ctx, path, producer, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("write error = %v, want context canceled", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("canceled destination exists or has unexpected error: %v", statErr)
	}
	assertNoResultExportTempFiles(t, directory)
}

func TestWriteResultExportCSVAtomicNeverReplacesExistingDestination(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "existing.csv")
	if err := os.WriteFile(path, []byte("keep-me\n"), 0o600); err != nil {
		t.Fatalf("seed destination: %v", err)
	}
	plan := resultExportPlan{
		scope: resultExportCurrentPage,
		snapshot: resultExportSnapshot{
			headers: []string{"id"},
			rows:    [][]string{{"1"}},
		},
	}
	if _, err := writeResultExportCSVAtomic(context.Background(), path, plan.csvProducer(), nil); err == nil {
		t.Fatal("writeResultExportCSVAtomic() replaced an existing destination")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read preserved destination: %v", err)
	}
	if string(contents) != "keep-me\n" {
		t.Fatalf("destination contents = %q, want original", contents)
	}
	assertNoResultExportTempFiles(t, directory)
}

func TestPortableResultExportPublicationIsPrivateAndNoClobber(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "complete.tmp")
	destinationPath := filepath.Join(directory, "portable.csv")
	if err := os.WriteFile(sourcePath, []byte("id,value\n1,complete\n"), 0o600); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if err := copyResultExportNoReplace(context.Background(), sourcePath, destinationPath); err != nil {
		t.Fatalf("portable publication: %v", err)
	}
	contents, err := os.ReadFile(destinationPath)
	if err != nil {
		t.Fatalf("read portable destination: %v", err)
	}
	if got := string(contents); got != "id,value\n1,complete\n" {
		t.Fatalf("portable contents = %q", got)
	}
	info, err := os.Stat(destinationPath)
	if err != nil {
		t.Fatalf("stat portable destination: %v", err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("portable permissions = %o, want 600", got)
	}

	if err := copyResultExportNoReplace(context.Background(), sourcePath, destinationPath); err == nil {
		t.Fatal("portable publication replaced an existing destination")
	}
	contents, err = os.ReadFile(destinationPath)
	if err != nil || string(contents) != "id,value\n1,complete\n" {
		t.Fatalf("existing portable destination changed: contents=%q err=%v", contents, err)
	}
}

func TestResultExportLifecycleCanBeCanceledByApp(t *testing.T) {
	app := &App{}
	ctx, cancel := context.WithCancel(context.Background())
	if !app.beginResultExport(cancel) {
		t.Fatal("beginResultExport() = false")
	}
	if !app.cancelActiveResultExport() {
		t.Fatal("cancelActiveResultExport() = false")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("result export context was not canceled")
	}
	app.finishResultExport()
	if app.cancelActiveResultExport() {
		t.Fatal("finished export still reported as running")
	}
}

func TestStreamAllMatchingResultCSVKeepsFullValues(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	longValue := strings.Repeat("database-value-", 12)
	if _, err := db.Exec(`CREATE TABLE items (id INTEGER, category TEXT, detail TEXT, amount REAL);
		INSERT INTO items VALUES (1, 'keep', ?, 1.23456789012345),
		                         (2, 'skip', 'not exported', 8.0),
		                         (3, 'keep', NULL, 9.87654321098765);`, longValue); err != nil {
		t.Fatalf("seed sqlite: %v", err)
	}

	var output strings.Builder
	writer := csv.NewWriter(&output)
	rowCount, err := streamAllMatchingResultCSV(
		context.Background(),
		writer,
		db,
		`SELECT id, detail, amount FROM items WHERE category = ? ORDER BY id`,
		[]any{"keep"},
		nil,
	)
	writer.Flush()
	if err != nil {
		t.Fatalf("stream matching CSV: %v", err)
	}
	if err := writer.Error(); err != nil {
		t.Fatalf("flush streamed CSV: %v", err)
	}
	if rowCount != 2 {
		t.Fatalf("row count = %d, want 2", rowCount)
	}
	records, err := csv.NewReader(strings.NewReader(output.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse streamed CSV: %v", err)
	}
	if got := records[1][1]; got != longValue {
		t.Fatalf("long database value length = %d, want %d", len(got), len(longValue))
	}
	if got, want := records[1][2], fullCellValue(1.23456789012345); got != want {
		t.Fatalf("precise database float = %q, want %q", got, want)
	}
	if got := records[2][1]; got != "NULL" {
		t.Fatalf("SQL NULL = %q, want NULL", got)
	}
}

func TestAllMatchingResultExportQueryHasNoPageLimit(t *testing.T) {
	app := &App{
		db:                 &sql.DB{},
		dbType:             config.SQLite,
		selectedTable:      "items",
		tableResultsActive: true,
		resultFilter: &resultValueFilter{
			table: "items",
			predicates: []resultFilterPredicate{
				{column: "category", operator: resultFilterEqual, value: "keep"},
			},
		},
		results:    newResultTable(),
		sortColumn: 0,
		sortAsc:    false,
		sortMode:   "server",
	}
	app.results.SetCell(0, 0, tview.NewTableCell("ID ▼").SetReference("id"))

	query, args, err := app.allMatchingResultExportQuery()
	if err != nil {
		t.Fatalf("build all-matching query: %v", err)
	}
	if strings.Contains(strings.ToUpper(query), "LIMIT") || strings.Contains(strings.ToUpper(query), "OFFSET") {
		t.Fatalf("all-matching query was paginated: %s", query)
	}
	want := `SELECT * FROM "items" WHERE "category" = ? ORDER BY "id" DESC`
	if query != want {
		t.Fatalf("query = %q, want %q", query, want)
	}
	if len(args) != 1 || args[0] != "keep" {
		t.Fatalf("query args = %#v, want [keep]", args)
	}
}

func assertNoResultExportTempFiles(t *testing.T, directory string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, ".*.tmp-*"))
	if err != nil {
		t.Fatalf("glob temporary files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary export files remain: %#v", matches)
	}
}
