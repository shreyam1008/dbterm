package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
	"github.com/shreyam1008/dbterm/internal/folderpicker"
)

const (
	pageBackupFileSets      = "backupFileSets"
	pageBackupFileSetForm   = "backupFileSetForm"
	pageBackupFileSetDelete = "backupFileSetDelete"
)

func splitFileSetPatterns(value string) []string {
	patterns := make([]string, 0)
	for _, pattern := range strings.Split(value, ",") {
		if pattern = strings.TrimSpace(pattern); pattern != "" {
			patterns = append(patterns, pattern)
		}
	}
	return patterns
}

func (a *App) showBackupFileSets(jobID string) {
	job, err := a.backupStore.GetJob(context.Background(), jobID)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not load included folders:\n\n%v", iconWarn, err), pageBackupCenter)
		return
	}
	header := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	header.SetBackgroundColor(bg)
	header.SetText(fmt.Sprintf("\n[::b][#94e2d5]Included Folders[-][-]  [#a6adc8]%s[-]\n[#a6adc8]%d named set(s) · future backups become self-contained dbterm bundles[-]", tview.Escape(job.Name), len(job.FileSets)))

	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).SetTitle(fmt.Sprintf(" File Sets (%d) ", len(job.FileSets))).SetTitleColor(mauve).SetBorderColor(surface1)
	list.SetBackgroundColor(bg)
	list.SetMainTextColor(text).SetSecondaryTextColor(subtext0).SetSelectedBackgroundColor(surface0).SetSelectedTextColor(green)
	if len(job.FileSets) == 0 {
		list.AddItem("  [#f9e2af]Database only[-]", "  Press N to include photos, documents, or another application folder.", 0, nil)
	} else {
		for _, set := range job.FileSets {
			policy := "REQUIRED"
			if !set.Required {
				policy = "OPTIONAL"
			}
			list.AddItem(fmt.Sprintf("  [::b]%s[-]  [#89b4fa]%s[-]", tview.Escape(set.Label), policy),
				tview.Escape("  "+set.Root), 0, nil)
		}
	}
	detail := tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	detail.SetBorder(true).SetTitle(" Selected Folder Policy ").SetTitleColor(mauve).SetBorderColor(surface1)
	detail.SetBackgroundColor(mantle)
	updateDetail := func(index int) {
		if index < 0 || index >= len(job.FileSets) {
			detail.SetText(" [#a6adc8]Database only. Add a named folder to produce a dbterm bundle; engine-native database verification still runs first.[-]")
			return
		}
		set := job.FileSets[index]
		policy := "required · any unsafe, missing, or changing file fails the backup"
		if !set.Required {
			policy = "optional · the whole set is omitted with a warning if capture is unsafe or unstable"
		}
		detail.SetText(fmt.Sprintf(" [#89b4fa]ROOT[-]     %s\n [#89b4fa]INCLUDE[-]  %s\n [#89b4fa]EXCLUDE[-]  %s\n [#89b4fa]POLICY[-]   %s",
			tview.Escape(set.Root), tview.Escape(strings.Join(set.Include, ", ")), tview.Escape(nonEmptyOr(strings.Join(set.Exclude, ", "), "none")), tview.Escape(policy)))
	}
	list.SetChangedFunc(func(index int, _, _ string, _ rune) { updateDetail(index) })
	updateDetail(list.GetCurrentItem())

	selected := func() (int, *backupcore.FileSet, bool) {
		index := list.GetCurrentItem()
		if index < 0 || index >= len(job.FileSets) {
			a.ShowAlert(fmt.Sprintf("%s Add an included folder first (N).", iconInfo), pageBackupFileSets)
			return -1, nil, false
		}
		set := job.FileSets[index]
		return index, &set, true
	}
	closePage := func() {
		a.pages.RemovePage(pageBackupFileSets)
		a.showBackupPlanActions(job)
	}
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyBackspace || event.Key() == tcell.KeyBackspace2 {
			closePage()
			return nil
		}
		if event.Key() == tcell.KeyEnter {
			if index, set, ok := selected(); ok {
				a.showBackupFileSetForm(job, index, set)
			} else {
				a.showBackupFileSetForm(job, -1, nil)
			}
			return nil
		}
		if shortcut, ok := plainShortcutRune(event); ok {
			switch shortcut {
			case 'n':
				a.showBackupFileSetForm(job, -1, nil)
				return nil
			case 'e':
				if index, set, ok := selected(); ok {
					a.showBackupFileSetForm(job, index, set)
				}
				return nil
			case 'd':
				if index, set, ok := selected(); ok {
					a.confirmDeleteBackupFileSet(job, index, *set)
				}
				return nil
			}
		}
		return event
	})
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(" [yellow]N[-] Add folder · [yellow]Enter/E[-] Edit · [yellow]D[-] Remove from future backups · [yellow]Esc[-] Back ")
	layout := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(header, 4, 0, false).AddItem(list, 0, 1, true).AddItem(detail, 6, 0, false).AddItem(footer, 1, 0, false)
	a.pages.AddAndSwitchToPage(pageBackupFileSets, layout, true)
	a.app.SetFocus(list)
}

func (a *App) showBackupFileSetForm(job backupcore.Job, index int, existing *backupcore.FileSet) *tview.Form {
	set := backupcore.FileSet{Label: "documents", Include: []string{"**"}, Required: true}
	if existing != nil {
		set = *existing
	}
	includes := strings.Join(set.Include, ", ")
	excludes := strings.Join(set.Exclude, ", ")
	returnFocus := a.app.GetFocus()
	container := tview.NewFlex().SetDirection(tview.FlexRow)
	var render func(string)
	var current *tview.Form
	closeForm := func() {
		a.pages.RemovePage(pageBackupFileSetForm)
		a.pages.ShowPage(pageBackupFileSets)
		if returnFocus != nil {
			a.app.SetFocus(returnFocus)
		}
	}
	chooseFolder := func() {
		ctx, cancel := context.WithCancel(context.Background())
		token := a.showLoadingModal("Opening the system folder chooser...", withLoadingCancelOutcome("Press Esc to keep the typed folder.", cancel))
		go func(initial string) {
			selected, err := folderpicker.Choose(ctx, initial)
			cancel()
			a.app.QueueUpdateDraw(func() {
				if !a.finishLoadingModal(token) {
					return
				}
				if err != nil {
					if errors.Is(err, folderpicker.ErrCancelled) || errors.Is(err, context.Canceled) {
						a.pages.ShowPage(pageBackupFileSetForm)
						return
					}
					a.ShowAlert(fmt.Sprintf("%s Could not open a folder chooser:\n\n%v", iconWarn, err), pageBackupFileSetForm)
					return
				}
				set.Root = selected
				render("Folder")
			})
		}(set.Root)
	}
	save := func() {
		expanded, err := expandHomePath(strings.TrimSpace(set.Root))
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s Invalid application folder:\n\n%v", iconWarn, err), pageBackupFileSetForm)
			return
		}
		absolute, err := filepath.Abs(expanded)
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s Invalid application folder:\n\n%v", iconWarn, err), pageBackupFileSetForm)
			return
		}
		set.Root = filepath.Clean(absolute)
		set.Label = strings.TrimSpace(set.Label)
		set.Include = splitFileSetPatterns(includes)
		if len(set.Include) == 0 {
			set.Include = []string{"**"}
		}
		set.Exclude = splitFileSetPatterns(excludes)
		candidate := job
		if index >= 0 && index < len(candidate.FileSets) {
			candidate.FileSets[index] = set
		} else {
			candidate.FileSets = append(candidate.FileSets, set)
		}
		if err := a.backupStore.UpsertJob(context.Background(), &candidate); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not save included folder:\n\n%v", iconWarn, err), pageBackupFileSetForm)
			return
		}
		a.pages.RemovePage(pageBackupFileSetForm)
		a.pages.RemovePage(pageBackupFileSets)
		a.showBackupFileSets(candidate.ID)
	}
	w, h := a.modalSize(82, 108, 22, 30)
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(" [yellow]F2[-] Browse · [yellow]Tab[-] Move · [yellow]Enter[-] Choose/save · [yellow]Esc[-] Cancel ")
	render = func(focus string) {
		form := tview.NewForm()
		current = form
		form.SetBorder(true).SetTitle(" Included Application Folder ").SetTitleColor(mauve).SetBorderColor(surface1)
		form.SetBackgroundColor(bg)
		form.SetFieldBackgroundColor(mantle).SetFieldTextColor(text).SetLabelColor(text).SetButtonBackgroundColor(surface1).SetButtonTextColor(green)
		addBackupFormSection(form, "FOLDER", "Captured beside the engine-native database payload")
		form.AddInputField("Label", set.Label, 32, nil, func(value string) { set.Label = value })
		form.AddInputField("Folder", set.Root, 54, nil, func(value string) { set.Root = value })
		form.AddCheckbox("Required", set.Required, func(value bool) { set.Required = value })
		form.AddInputField("Include (comma-separated)", includes, 54, nil, func(value string) { includes = value })
		form.AddInputField("Exclude (comma-separated)", excludes, 54, nil, func(value string) { excludes = value })
		form.AddTextView("Consistency", "[#a6adc8]Live folders use a private best-effort capture with change detection, not an atomic application or filesystem snapshot. A changed, unsafe, or missing required set fails the backup; an optional set is omitted with a warning.[-]", 0, 4, true, false)
		form.AddTextView("Paths", "[#a6adc8]Use slash-separated globs. Symlinks, reparse points, non-regular files, and paths outside this root are refused. The absolute root is never stored in the artifact.[-]", 0, 3, true, false)
		form.AddButton("Save Folder", save)
		form.AddButton("Browse...", chooseFolder)
		form.AddButton("Cancel", closeForm)
		form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyF2 {
				chooseFolder()
				return nil
			}
			if event.Key() == tcell.KeyEscape {
				closeForm()
				return nil
			}
			return event
		})
		container.Clear().AddItem(form, 0, 1, true).AddItem(footer, 1, 0, false)
		if focus != "" {
			setBackupFormFocus(form, focus)
		}
		a.app.SetFocus(form)
	}
	a.pages.AddPage(pageBackupFileSetForm, backupModalGrid(container, w, h), true, true)
	render("")
	return current
}

func (a *App) confirmDeleteBackupFileSet(job backupcore.Job, index int, set backupcore.FileSet) {
	modal := tview.NewModal().SetText(fmt.Sprintf("%s Remove [yellow]%s[-] from future backups of %s?\n\nExisting dbterm bundles remain immutable and are not changed.", iconWarn, tview.Escape(set.Label), tview.Escape(job.Name))).
		AddButtons([]string{" Remove policy ", " Cancel "}).SetDoneFunc(func(button int, _ string) {
		a.pages.RemovePage(pageBackupFileSetDelete)
		if button != 0 {
			a.pages.ShowPage(pageBackupFileSets)
			return
		}
		if index < 0 || index >= len(job.FileSets) || !strings.EqualFold(job.FileSets[index].Label, set.Label) {
			a.ShowAlert(fmt.Sprintf("%s Included-folder selection changed; nothing was removed.", iconWarn), pageBackupFileSets)
			return
		}
		job.FileSets = append(job.FileSets[:index], job.FileSets[index+1:]...)
		if err := a.backupStore.UpsertJob(context.Background(), &job); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not remove included folder:\n\n%v", iconWarn, err), pageBackupFileSets)
			return
		}
		a.pages.RemovePage(pageBackupFileSets)
		a.showBackupFileSets(job.ID)
	})
	modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
	a.pages.AddPage(pageBackupFileSetDelete, modal, true, true)
}
