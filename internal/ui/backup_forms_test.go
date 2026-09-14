package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
	"github.com/shreyam1008/dbterm/internal/config"
)

func TestBackupOptionsPreserveHiddenAndEditedSettings(t *testing.T) {
	a := backupRoundTestApp(t)
	a.store.Connections = []config.ConnectionConfig{{ID: "orders", Name: "Orders", Type: config.SQLite}}
	a.pages.AddAndSwitchToPage(pageBackupCenter, tview.NewList(), true)
	job := backupcore.Job{ID: "settings", Name: "Orders backup", ConnectionID: "orders", Destination: t.TempDir(), Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual}, Compression: backupcore.CompressionZstd, CompressionLevel: 3, Retention: backupcore.Retention{KeepLast: 9, MaxTotalBytes: 3*(1<<30) + 123}, TimeoutMinutes: 30, Notification: backupcore.EmailNotification{Policy: backupcore.NotificationNever, Password: "fixture-password"}}
	form := a.showBackupJobFormForConnection(&job, "")
	destination := t.TempDir()
	form.GetFormItemByLabel(backupFormLabelDestination).(*backupFolderField).SetText(destination)
	key := func(key tcell.Key) {
		form.InputHandler()(tcell.NewEventKey(key, 0, tcell.ModNone), func(p tview.Primitive) { a.app.SetFocus(p) })
	}
	key(tcell.KeyF4)
	form.GetFormItemByLabel("Section").(*tview.DropDown).SetCurrentOption(3)
	form.GetFormItemByLabel("Max Attempts (1 = no retry)").(*tview.InputField).SetText("3")
	form.GetFormItemByLabel("Initial Retry Seconds").(*tview.InputField).SetText("5")
	form.GetFormItemByLabel("Maximum Retry Seconds").(*tview.InputField).SetText("30")
	form.GetFormItemByLabel("Section").(*tview.DropDown).SetCurrentOption(2)
	if form.GetFormItemByLabel("SMTP Host") != nil || form.GetFormItemByLabel("Max Attempts (1 = no retry)") != nil {
		t.Fatal("unrelated option categories are visible")
	}
	form.GetFormItemByLabel("Compression").(*tview.DropDown).SetCurrentOption(1)
	key(tcell.KeyF4)
	if field := form.GetFormItemByLabel(backupFormLabelDestination).(*backupFolderField); field.GetText() != destination {
		t.Fatal("returning to basics lost destination edit")
	}
	form.GetButton(0).InputHandler()(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(p tview.Primitive) { a.app.SetFocus(p) })
	saved, err := a.backupStore.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Destination != destination || saved.Compression != backupcore.CompressionGzip || saved.MaxAttempts != 3 || saved.RetryInitialSeconds != 5 || saved.RetryMaxSeconds != 30 || saved.Retention != job.Retention || saved.Notification.Password != job.Notification.Password {
		t.Fatalf("options were lost across category changes: compression=%s attempts=%d retention=%#v", saved.Compression, saved.MaxAttempts, saved.Retention)
	}
}

func TestBackupCreationFormsFitCommonTerminals(t *testing.T) {
	for _, dimensions := range [][2]int{{80, 24}, {120, 34}} {
		for _, kind := range []string{"plan", "instant"} {
			t.Run(fmt.Sprintf("%s_%dx%d", kind, dimensions[0], dimensions[1]), func(t *testing.T) {
				a := backupRoundTestApp(t)
				t.Setenv("USERPROFILE", `C:\Users\you`)
				t.Setenv("HOME", "/home/you")
				connection := config.ConnectionConfig{ID: "orders", Name: "Orders", Type: config.SQLite, FilePath: "orders.sqlite3"}
				a.store.Connections = []config.ConnectionConfig{connection}
				a.lastScreenW, a.lastScreenH = dimensions[0], dimensions[1]
				screen := tcell.NewSimulationScreen("UTF-8")
				a.app.SetScreen(screen)
				screen.SetSize(dimensions[0], dimensions[1])
				t.Cleanup(screen.Fini)
				button := "Save Backup"
				var form *tview.Form
				if kind == "plan" {
					form = a.showBackupJobFormForConnection(nil, "orders")
				} else {
					a.db = &sql.DB{}
					a.activeConn, a.dbType, a.dbName = &connection, config.SQLite, "Orders"
					a.showBackupModal()
					button = "Create Backup"
				}
				a.app.ForceDraw()
				rendered := backupSimulationScreenText(screen)
				t.Logf("Initial form:\n%s", rendered)
				for _, want := range []string{"Database", "Orders", "Save to", "Browse…", button, "Cancel"} {
					if !strings.Contains(rendered, want) {
						t.Fatalf("essential control %q is not initially visible:\n%s", want, rendered)
					}
				}
				for _, unwanted := range []string{"rclone", "SMTP", "Filename Template", "engine-native", "STORAGE & RETENTION", "Nothing is written", "GiB", "pg_dump", "Timeout Minutes"} {
					if strings.Contains(rendered, unwanted) {
						t.Fatalf("initial form includes optional detail %q:\n%s", unwanted, rendered)
					}
				}
				exportBackupFormScreen(t, screen, fmt.Sprintf("%s-%dx%d", kind, dimensions[0], dimensions[1]))
				if dimensions[0] > 80 {
					screen.SetSize(80, 24)
					a.app.ForceDraw()
					resized := backupSimulationScreenText(screen)
					if !strings.Contains(resized, button) || !strings.Contains(resized, "Esc Cancel") {
						t.Fatalf("resize hid form actions:\n%s", resized)
					}
					screen.SetSize(dimensions[0], dimensions[1])
				}
				if kind == "instant" {
					field := a.app.GetFocus().(*tview.InputField)
					field.SetText(filepath.Join(t.TempDir(), "chosen-folder"))
					root := a.pages.GetPage(instantBackupPage)
					press := func() {
						root.InputHandler()(tcell.NewEventKey(tcell.KeyF4, 0, tcell.ModNone), func(p tview.Primitive) { a.app.SetFocus(p) })
					}
					press()
					a.app.ForceDraw()
					if !strings.Contains(backupSimulationScreenText(screen), "Format") {
						t.Fatalf("instant details did not open, focus=%T:\n%s", a.app.GetFocus(), backupSimulationScreenText(screen))
					}
					press()
					a.app.ForceDraw()
					if strings.Contains(backupSimulationScreenText(screen), "Format") || !strings.HasSuffix(field.GetText(), "chosen-folder") {
						t.Fatal("details toggle lost the edit or failed to collapse")
					}
				}
				if form != nil {
					form.InputHandler()(tcell.NewEventKey(tcell.KeyF4, 0, tcell.ModNone), func(p tview.Primitive) { a.app.SetFocus(p) })
					form.GetFormItemByLabel("Section").(*tview.DropDown).SetCurrentOption(2)
					a.app.ForceDraw()
					exportBackupFormScreen(t, screen, fmt.Sprintf("options-%dx%d", dimensions[0], dimensions[1]))
				}
			})
		}
	}
}

// Optional review artifacts capture the real tcell layout and colors using a
// synthetic database; they never connect to or export a user's database.
func exportBackupFormScreen(t *testing.T, screen tcell.SimulationScreen, name string) {
	t.Helper()
	directory := os.Getenv("DBTERM_FORM_SNAPSHOTS")
	if directory == "" {
		return
	}
	type pixelCell struct {
		Text                   string
		Foreground, Background [3]int32
	}
	cells, width, height := screen.GetContents()
	snapshot := struct {
		Width, Height int
		Cells         []pixelCell
	}{Width: width, Height: height}
	for _, cell := range cells {
		foreground, background, _ := cell.Style.Decompose()
		fr, fg, fb := foreground.RGB()
		br, bg, bb := background.RGB()
		characters := string(cell.Runes)
		if characters == "" {
			characters = " "
		}
		snapshot.Cells = append(snapshot.Cells, pixelCell{characters, [3]int32{fr, fg, fb}, [3]int32{br, bg, bb}})
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name+".json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
}
