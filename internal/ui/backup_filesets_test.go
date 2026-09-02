package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
	"github.com/shreyam1008/dbterm/internal/config"
)

func TestSplitFileSetPatternsTrimsAndDropsEmptyValues(t *testing.T) {
	got := splitFileSetPatterns(" ** , photos/*.jpg, , **/*.png ")
	if strings.Join(got, "|") != "**|photos/*.jpg|**/*.png" {
		t.Fatalf("splitFileSetPatterns() = %#v", got)
	}
}

func TestBackupFileSetFormShowsConsistencyAndSafetyControls(t *testing.T) {
	store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	application := tview.NewApplication()
	pages := tview.NewPages()
	pages.AddPage(pageBackupFileSets, tview.NewList(), true, true)
	application.SetRoot(pages, true)
	app := &App{app: application, pages: pages, backupStore: store, store: &config.Store{}}
	job := backupcore.Job{ID: "job_files", Name: "Registration", ConnectionID: "conn", Destination: filepath.Join(t.TempDir(), "backups"), Compression: backupcore.CompressionZstd, Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual}}
	form := app.showBackupFileSetForm(job, -1, nil)
	if form == nil {
		t.Fatal("file-set form was not created")
	}
	for _, label := range []string{"Label", "Folder", "Required", "Include (comma-separated)", "Exclude (comma-separated)", "Consistency", "Paths"} {
		if form.GetFormItemByLabel(label) == nil {
			t.Errorf("file-set form missing %q", label)
		}
	}
}

func TestBackupFileSetsRenderAtCommonTerminalSizes(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {120, 35}} {
		t.Run(strconvItoa(size.width)+"x"+strconvItoa(size.height), func(t *testing.T) {
			store, err := backupcore.OpenStore(filepath.Join(t.TempDir(), "backups.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			root := filepath.Join(t.TempDir(), "profile_pics")
			if err := os.MkdirAll(root, 0o700); err != nil {
				t.Fatal(err)
			}
			job := backupcore.Job{
				ID: "job_registration", Name: "JKP Registration", ConnectionID: "conn", Destination: filepath.Join(t.TempDir(), "backups"),
				Compression: backupcore.CompressionZstd, Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual},
				FileSets: []backupcore.FileSet{{Label: "profile-photos", Root: root, Include: []string{"**"}, Exclude: []string{"**/*.tmp"}, Required: true}},
			}
			if err := store.UpsertJob(context.Background(), &job); err != nil {
				t.Fatal(err)
			}
			application := tview.NewApplication()
			pages := tview.NewPages()
			pages.AddPage(pageBackupCenter, tview.NewTextView(), true, true)
			application.SetRoot(pages, true)
			app := &App{app: application, pages: pages, backupStore: store, store: &config.Store{}, lastScreenW: size.width, lastScreenH: size.height}
			screen := tcell.NewSimulationScreen("UTF-8")
			application.SetScreen(screen)
			screen.SetSize(size.width, size.height)
			t.Cleanup(screen.Fini)

			app.showBackupFileSets(job.ID)
			application.ForceDraw()
			rendered := backupSimulationScreenText(screen)
			t.Logf("%dx%d File Sets render:\n%s", size.width, size.height, rendered)
			for _, want := range []string{"Included Folders", "JKP Registration", "profile-photos", "REQUIRED", "INCLUDE  **", "EXCLUDE  **/*.tmp", "future backups", "dbterm bundles"} {
				if !strings.Contains(rendered, want) {
					t.Errorf("render missing %q:\n%s", want, rendered)
				}
			}
		})
	}
}
