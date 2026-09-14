package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
	"github.com/shreyam1008/dbterm/internal/config"
)

func TestBackupSmallTerminalHelpAndFieldBounds(t *testing.T) {
	for _, kind := range []string{"retries", "email", "copy", "sftp", "folders"} {
		t.Run(kind, func(t *testing.T) {
			a := backupRoundTestApp(t)
			a.lastScreenW, a.lastScreenH = 80, 24
			a.store.Connections = []config.ConnectionConfig{{ID: "demo", Name: "Demo", Type: config.SQLite}}
			screen := tcell.NewSimulationScreen("UTF-8")
			a.app.SetScreen(screen)
			screen.SetSize(80, 24)
			t.Cleanup(screen.Fini)
			var form *tview.Form
			var expected []string
			switch kind {
			case "retries", "email":
				form = a.showBackupJobFormForConnection(nil, "demo")
				form.InputHandler()(tcell.NewEventKey(tcell.KeyF4, 0, 0), func(p tview.Primitive) { a.app.SetFocus(p) })
				if kind == "retries" {
					form.GetFormItemByLabel("Section").(*tview.DropDown).SetCurrentOption(3)
					expected = []string{"RETRY POLICY", "Attempts include the first run", "Transient failures only.", "Retries share the job timeout.", "Publication safeguards still apply.", "Save Backup", "Cancel"}
				} else {
					form.GetFormItemByLabel("Section").(*tview.DropDown).SetCurrentOption(4)
					form.GetFormItemByLabel("Send Email").(*tview.DropDown).SetCurrentOption(1)
					expected = []string{"Sends email only; no backup runs.", "Send Test Email", "Cancel"}
				}
			case "copy":
				form = a.showBackupCopyForm(nil)
				expected = []string{"TOPOLOGY", "Moves completed artifacts; never creates a database dump", "Source Folder", "Destination Folder", "Browse…"}
			case "sftp":
				job := backupcore.CopyJob{Name: "SFTP fixture", Mode: backupcore.CopyModePush, Trigger: backupcore.CopyTriggerManual, Schedule: backupcore.Schedule{Kind: backupcore.ScheduleManual}, Source: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: t.TempDir()}, Destination: backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointSFTP, Location: "sftp://demo@example.invalid/vault"}}
				form = a.showBackupCopyForm(&job)
				expected = []string{"SFTP Location", "Private Identity File", "Pinned Host Key", "Browse…", "Use sftp://user@host/absolute/path.", "Password URLs are refused."}
			case "folders":
				form = a.showBackupFileSetForm(backupcore.Job{Name: "demo"}, -1, nil)
				expected = []string{"FOLDER", "Captured beside the engine-native database payload", "Best-effort capture", "Use slash-separated globs.", "Save Folder", "Cancel"}
			}
			a.app.ForceDraw()
			rendered := backupSimulationScreenText(screen)
			for _, want := range expected {
				if !strings.Contains(rendered, want) {
					t.Fatalf("missing visible text %q:\n%s", want, rendered)
				}
			}
			_, _, width, _ := form.GetInnerRect()
			labelWidth := 0
			for index := 0; index < form.GetFormItemCount(); index++ {
				labelWidth = max(labelWidth, tview.TaggedStringWidth(form.GetFormItem(index).GetLabel())+1)
			}
			for index := 0; index < form.GetFormItemCount(); index++ {
				if field, ok := form.GetFormItem(index).(*tview.InputField); ok && field.GetFieldWidth() > 0 && labelWidth+field.GetFieldWidth() > width {
					t.Fatalf("%s paints beyond the form: label=%d field=%d available=%d", field.GetLabel(), labelWidth, field.GetFieldWidth(), width)
				}
			}
			exportBackupFormScreen(t, screen, kind+"-verified-80x24")
		})
	}
}

func TestBackupOutcomeKeepsActionsVisibleAndFullDetailsAccessible(t *testing.T) {
	a := backupRoundTestApp(t)
	origin := a.app.GetFocus()
	screen := tcell.NewSimulationScreen("UTF-8")
	a.app.SetScreen(screen)
	screen.SetSize(80, 24)
	t.Cleanup(screen.Fini)
	details := "Backup complete\n\nPath: C:/" + strings.Repeat("long-folder/", 30) + "backup.sqlite3\n\n" + strings.Repeat("diagnostic\n", 40) + "Final verification passed."
	a.showBackupOutcome("Backup complete\n\n8 KiB · verified\nArtifact and manifest saved.", details, "origin")
	a.app.ForceDraw()
	for _, want := range []string{"Backup complete", "Details", "Done"} {
		if !strings.Contains(backupSimulationScreenText(screen), want) {
			t.Fatalf("outcome hid %s", want)
		}
	}
	exportBackupFormScreen(t, screen, "outcome-verified-80x24")
	backupRoundKey(a, tcell.KeyEnter, 0)
	view, ok := a.app.GetFocus().(*tview.TextView)
	if !ok || view.GetText(false) != details {
		t.Fatal("Details lost the full diagnostic")
	}
	backupRoundKey(a, tcell.KeyEnd, 0)
	a.app.ForceDraw()
	if !strings.Contains(backupSimulationScreenText(screen), "Final verification passed.") {
		t.Fatal("the end of the result is inaccessible")
	}
	backupRoundKey(a, tcell.KeyEscape, 0)
	if page, _ := a.pages.GetFrontPage(); page != "backupOutcome" {
		t.Fatalf("Details returned to %s", page)
	}
	backupRoundKey(a, tcell.KeyEscape, 0)
	if page, _ := a.pages.GetFrontPage(); page != "origin" || a.app.GetFocus() != origin {
		t.Fatal("closing the outcome did not restore the caller")
	}
}
