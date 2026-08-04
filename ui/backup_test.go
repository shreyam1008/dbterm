package ui

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/config"
)

func TestAltBOpensInstantBackupFromQueryAndEscapeIsReadOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	keymap, err := newActionKeymap(config.DefaultSettings())
	if err != nil {
		t.Fatalf("build keymap: %v", err)
	}
	application := tview.NewApplication()
	pages := tview.NewPages()
	tables := tview.NewList()
	query := tview.NewTextArea()
	results := tview.NewTable()
	pages.AddPage("main", query, true, true)
	app := &App{
		app:        application,
		pages:      pages,
		db:         &sql.DB{},
		store:      &config.Store{},
		settings:   config.DefaultSettings(),
		keymap:     keymap,
		dbType:     config.SQLite,
		dbName:     "orders",
		activeConn: &config.ConnectionConfig{Name: "orders", Type: config.SQLite, FilePath: filepath.Join(home, "orders.sqlite3")},
		tables:     tables,
		queryInput: query,
		results:    results,
		statusBar:  tview.NewTextView(),
	}
	application.SetFocus(query)
	app.setupKeyBindings()

	capture := application.GetInputCapture()
	if capture == nil {
		t.Fatal("application input capture was not installed")
	}
	if returned := capture(tcell.NewEventKey(tcell.KeyRune, 'b', tcell.ModAlt)); returned != nil {
		t.Fatalf("Alt+B was not consumed: %#v", returned)
	}
	if !pages.HasPage(instantBackupPage) {
		t.Fatal("Alt+B from the query editor did not open instant backup")
	}
	destinationField, ok := application.GetFocus().(*tview.InputField)
	if !ok {
		t.Fatalf("focus = %T, want instant-backup destination field", application.GetFocus())
	}
	defaultDestination := filepath.Join(home, "dbterm-backups")
	if destinationField.GetLabel() != instantBackupDestinationLabel {
		t.Fatalf("focused field = %q, want %q", destinationField.GetLabel(), instantBackupDestinationLabel)
	}
	if got := destinationField.GetText(); got != defaultDestination {
		t.Fatalf("default destination = %q, want %q", got, defaultDestination)
	}
	if _, err := os.Stat(defaultDestination); !os.IsNotExist(err) {
		t.Fatalf("opening instant backup created its destination: %v", err)
	}

	modalRoot := pages.GetPage(instantBackupPage)
	if modalRoot == nil {
		t.Fatal("instant-backup page has no root primitive")
	}
	modalRoot.InputHandler()(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone), func(primitive tview.Primitive) {
		application.SetFocus(primitive)
	})
	if pages.HasPage(instantBackupPage) {
		t.Fatal("Escape left the instant-backup page mounted")
	}
	frontPage, _ := pages.GetFrontPage()
	if frontPage != "main" {
		t.Fatalf("front page = %q, want unchanged main workspace", frontPage)
	}
	if application.GetFocus() != query {
		t.Fatalf("focus = %T, want original query editor", application.GetFocus())
	}
	if _, err := os.Stat(defaultDestination); !os.IsNotExist(err) {
		t.Fatalf("canceling instant backup created its destination: %v", err)
	}
}

func TestPrepareInstantBackupOutputIsReadOnly(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "new", "nested")

	output, err := prepareInstantBackupOutput(destination, "orders", "fallback.dump", ".dump")
	if err != nil {
		t.Fatalf("prepare output: %v", err)
	}
	if output.directory != destination {
		t.Fatalf("directory = %q, want %q", output.directory, destination)
	}
	if output.filename != "orders.dump" {
		t.Fatalf("filename = %q, want orders.dump", output.filename)
	}
	if output.path != filepath.Join(destination, "orders.dump") {
		t.Fatalf("path = %q", output.path)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("read-only preparation created destination or returned unexpected error: %v", err)
	}
}

func TestPrepareInstantBackupOutputExpandsHomeAndDefaultsFilename(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	destination := filepath.Join(home, "backups")

	output, err := prepareInstantBackupOutput("~/backups", "", "orders.dump", ".dump")
	if err != nil {
		t.Fatalf("prepare output: %v", err)
	}
	if output.directory != destination {
		t.Fatalf("directory = %q, want %q", output.directory, destination)
	}
	if output.filename != "orders.dump" {
		t.Fatalf("filename = %q, want orders.dump", output.filename)
	}
}

func TestPrepareInstantBackupOutputRequiresBasename(t *testing.T) {
	for _, filename := range []string{"../orders.dump", "nested/orders.dump", `nested\orders.dump`, ".", ".."} {
		t.Run(strings.ReplaceAll(filename, "/", "_"), func(t *testing.T) {
			if _, err := prepareInstantBackupOutput(t.TempDir(), filename, "fallback.dump", ".dump"); err == nil || !strings.Contains(err.Error(), "single name") {
				t.Fatalf("error = %v, want basename validation", err)
			}
		})
	}
}

func TestPrepareInstantBackupOutputRefusesExistingPath(t *testing.T) {
	destination := t.TempDir()
	existing := filepath.Join(destination, "orders.dump")
	if err := os.WriteFile(existing, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	_, err := prepareInstantBackupOutput(destination, "orders.dump", "fallback.dump", ".dump")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %v, want no-overwrite error", err)
	}
	contents, readErr := os.ReadFile(existing)
	if readErr != nil || string(contents) != "keep me" {
		t.Fatalf("existing output changed: contents=%q err=%v", contents, readErr)
	}
}

func TestPrepareInstantBackupOutputRejectsFileAsDestination(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "not-a-folder")
	if err := os.WriteFile(destination, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := prepareInstantBackupOutput(destination, "orders.dump", "fallback.dump", ".dump"); err == nil || !strings.Contains(err.Error(), "not a folder") {
		t.Fatalf("error = %v, want destination type error", err)
	}
}
