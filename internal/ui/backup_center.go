package ui

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	"github.com/shreyam1008/dbterm/internal/appdirs"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/database"
	"github.com/shreyam1008/dbterm/internal/folderpicker"
	"github.com/shreyam1008/dbterm/internal/osservice"
	"github.com/shreyam1008/dbterm/internal/processinfo"
)

const (
	pageBackupCenter           = "backupCenter"
	pageBackupForm             = "backupJobForm"
	pageBackupConnectionPicker = "backupConnectionPicker"

	backupDecodedGiB         = int64(1 << 30)
	backupAgentProcessPoll   = 100 * time.Millisecond
	backupAgentActionTimeout = 45 * time.Second
)

func (a *App) showBackupCenter() {
	if _, err := a.ensureBackupStore(); err != nil {
		returnPage, _ := a.pages.GetFrontPage()
		a.ShowAlert(fmt.Sprintf("%s Backup Center is unavailable:\n\n%v", iconWarn, err), returnPage)
		return
	}
	if !a.pages.HasPage(pageBackupCenter) {
		a.backupCenterReturnPage, _ = a.pages.GetFrontPage()
		a.backupCenterReturnFocus = a.app.GetFocus()
		if a.backupCenterReturnPage == "" {
			a.backupCenterReturnPage = "dashboard"
		}
	}

	jobs, err := a.backupStore.ListJobs(context.Background())
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not load backup jobs:\n\n%v", iconWarn, err), a.backupCenterReturnPage)
		return
	}
	runs, _ := a.backupStore.ListRuns(context.Background(), "", 250)
	latest := make(map[string]backupcore.Run)
	for _, run := range runs {
		if _, exists := latest[run.JobID]; !exists {
			latest[run.JobID] = run
		}
	}

	header := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	header.SetBackgroundColor(bg)
	enabled := 0
	for _, job := range jobs {
		if job.Enabled && job.Schedule.Kind != backupcore.ScheduleManual {
			enabled++
		}
	}
	agentLabel := "[#6c7086]agent offline[-]"
	if status, statusErr := backupcore.AgentHealth(context.Background(), a.backupStore, time.Now()); statusErr == nil && status.Healthy {
		agentLabel = fmt.Sprintf("[green]agent active[-] [#6c7086]pid %d[-]", status.PID)
		if status.Activity != nil && strings.TrimSpace(status.Activity.JobName) != "" {
			agentLabel = fmt.Sprintf("[yellow]running %s[-] [#6c7086]· %s[-]", tview.Escape(status.Activity.JobName), tview.Escape(nonEmptyOr(status.Activity.Phase, "working")))
		}
	}
	header.SetText(fmt.Sprintf(
		"\n[::b][#cba6f7]%s Backup Center[-][-]\n[#a6e3a1]%d enabled[-]  •  %d plans  •  %s\n[#a6adc8]N chooses a database for a new plan. Alt+B remains the instant backup for the open database.[-]",
		iconBackup, enabled, len(jobs), agentLabel,
	))

	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).SetTitle(fmt.Sprintf(" %s Backup Plans (%d) ", iconBackup, len(jobs))).SetBorderColor(surface1).SetTitleColor(mauve)
	list.SetBackgroundColor(bg)
	list.SetMainTextColor(text).SetSecondaryTextColor(subtext0)
	list.SetSelectedBackgroundColor(surface0).SetSelectedTextColor(green)
	if len(jobs) == 0 {
		list.AddItem("  [#6c7086]No backup plans yet[-]", "  Press N to choose a saved database or add a connection.", 0, nil)
		a.backupCenterSelectedJob = ""
	} else {
		for _, job := range jobs {
			state := "[#6c7086]○ disabled[-]"
			if job.Schedule.Kind == backupcore.ScheduleManual {
				state = "[#89b4fa]◆ on demand[-]"
			} else if job.Enabled {
				state = "[green]● enabled[-]"
			}
			source := backupJobConnectionLabel(a.store.Connections, job.ConnectionID)
			last := "never run"
			if run, ok := latest[job.ID]; ok {
				last = backupRunSummary(run)
			}
			list.AddItem(fmt.Sprintf("  %s  [::b]%s[-]", state, tview.Escape(job.Name)),
				fmt.Sprintf("  %s  │  %s  │  last %s", tview.Escape(source), backupScheduleLabel(job.Schedule), last), 0, nil)
		}
	}
	selectedIndex := 0
	for index, job := range jobs {
		if job.ID == a.backupCenterSelectedJob {
			selectedIndex = index
			break
		}
	}

	detail := tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetWordWrap(true)
	detail.SetBorder(true).SetTitle(" Plan Details ").SetTitleColor(mauve).SetBorderColor(surface1).SetBackgroundColor(mantle)
	updateDetail := func(index int) {
		if index < 0 || index >= len(jobs) {
			detail.SetText(" [#a6adc8]Start with a database connection. Safe defaults cover the destination, daily timing, compression, and retention.[-]")
			return
		}
		job := jobs[index]
		next := "manual only"
		if !job.NextRunAt.IsZero() {
			next = job.NextRunAt.Local().Format("Mon 02 Jan, 15:04 MST")
		}
		encryption := "not encrypted"
		if job.Encryption == backupcore.EncryptionAge {
			encryption = "age encrypted"
		}
		detail.SetText(fmt.Sprintf(
			" [#89b4fa]DATABASE[-] %s\n [#89b4fa]SAVE TO[-]  %s\n [#89b4fa]SCHEDULE[-] %s  │  next %s\n [#89b4fa]POLICY[-]   keep %s  │  %s  │  %s",
			tview.Escape(backupJobConnectionDetail(a.store.Connections, job.ConnectionID)),
			tview.Escape(job.Destination), tview.Escape(backupScheduleLabel(job.Schedule)), tview.Escape(next),
			tview.Escape(backupRetentionSummary(job.Retention)), tview.Escape(string(job.Compression)), encryption,
		))
	}
	list.SetChangedFunc(func(index int, _, _ string, _ rune) {
		if index >= 0 && index < len(jobs) {
			a.backupCenterSelectedJob = jobs[index].ID
		}
		updateDetail(index)
	})
	if len(jobs) > 0 {
		a.backupCenterSelectedJob = jobs[selectedIndex].ID
		list.SetCurrentItem(selectedIndex)
	}
	updateDetail(selectedIndex)

	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	screenWidth, _ := a.getScreenSize()
	footer.SetText(backupCenterFooterText(screenWidth))

	closeCenter := func() {
		a.pages.RemovePage(pageBackupCenter)
		returnState := loadingReturnState{page: a.backupCenterReturnPage, focus: a.backupCenterReturnFocus}
		a.backupCenterReturnPage = ""
		a.backupCenterReturnFocus = nil
		if returnState.page == "dashboard" {
			a.pages.RemovePage("dashboard")
			a.showDashboard()
			return
		}
		if returnState.page != "" && a.pages.HasPage(returnState.page) {
			a.pages.ShowPage(returnState.page)
			a.restoreLoadingReturnState(returnState)
			return
		}
		a.showDashboard()
	}
	selectedJob := func() (*backupcore.Job, bool) {
		index := list.GetCurrentItem()
		if index < 0 || index >= len(jobs) {
			a.ShowAlert(fmt.Sprintf("%s Create a backup job first (N).", iconInfo), pageBackupCenter)
			return nil, false
		}
		job := jobs[index]
		return &job, true
	}
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			closeCenter()
			return nil
		}
		if event.Key() == tcell.KeyEnter {
			if job, ok := selectedJob(); ok {
				a.showBackupJobForm(job)
			}
			return nil
		}
		if event.Key() == tcell.KeyRune && event.Modifiers()&(tcell.ModCtrl|tcell.ModAlt|tcell.ModMeta) == 0 {
			switch event.Rune() {
			case 'n', 'N':
				a.showBackupConnectionPicker()
				return nil
			case 'e', 'E':
				if job, ok := selectedJob(); ok {
					a.showBackupJobForm(job)
				}
				return nil
			case 'r', 'R':
				if job, ok := selectedJob(); ok {
					a.runBackupJobNow(job.ID)
				}
				return nil
			case 'p', 'P':
				if job, ok := selectedJob(); ok {
					a.confirmPruneBackupJob(*job)
				}
				return nil
			case ' ':
				if job, ok := selectedJob(); ok {
					if job.Schedule.Kind == backupcore.ScheduleManual {
						a.ShowAlert(fmt.Sprintf("%s This plan is on demand. Press R whenever you want to run it; choose a timed schedule in Edit to let the agent run it automatically.", iconInfo), pageBackupCenter)
						return nil
					}
					if err := a.backupStore.SetJobEnabled(context.Background(), job.ID, !job.Enabled); err != nil {
						a.ShowAlert(fmt.Sprintf("%s Could not change job state:\n\n%v", iconWarn, err), pageBackupCenter)
					} else {
						a.showBackupCenter()
						if !job.Enabled && job.Schedule.Kind != backupcore.ScheduleManual {
							a.offerBackupAgentStart()
						}
					}
				}
				return nil
			case 'h', 'H':
				a.showBackupHistory()
				return nil
			case 'i', 'I':
				a.showBackupInspectForm()
				return nil
			case 'a', 'A':
				a.showBackupAgentManager()
				return nil
			case 'g', 'G':
				a.showBackupAgeKeyGenerator()
				return nil
			case 'd', 'D':
				if job, ok := selectedJob(); ok {
					a.confirmDeleteBackupJob(*job)
				}
				return nil
			}
		}
		return event
	})

	layout := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 5, 0, false).
		AddItem(list, 0, 1, true).
		AddItem(detail, 6, 0, false).
		AddItem(footer, 1, 0, false)
	a.pages.AddAndSwitchToPage(pageBackupCenter, layout, true)
	a.app.SetFocus(list)
}

// ensureBackupStore opens the persistent backup catalog only when a feature
// needs it. Instant backups do not use this catalog, so normal database
// browsing avoids SQLite initialization, file descriptors, and idle memory.
func (a *App) ensureBackupStore() (*backupcore.Store, error) {
	if a.backupStore != nil {
		return a.backupStore, nil
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return nil, err
	}
	a.backupStore = store
	return store, nil
}

func (a *App) showBackupJobForm(existing *backupcore.Job) {
	a.showBackupJobFormForConnection(existing, "")
}

type backupConnectionChoice struct {
	connectionID string
	name         string
	detail       string
	addNew       bool
}

func backupConnectionChoices(connections []config.ConnectionConfig) []backupConnectionChoice {
	choices := make([]backupConnectionChoice, 0, len(connections)+1)
	for _, connection := range connections {
		choices = append(choices, backupConnectionChoice{
			connectionID: connection.ID,
			name:         nonEmptyOr(strings.TrimSpace(connection.Name), "unnamed connection"),
			detail:       backupConnectionSummary(connection),
		})
	}
	choices = append(choices, backupConnectionChoice{
		name:   "Add a new connection…",
		detail: "Save database access, then continue directly to this backup.",
		addNew: true,
	})
	return choices
}

func (a *App) showBackupConnectionPicker() {
	choices := backupConnectionChoices(a.store.Connections)
	returnFocus := a.app.GetFocus()
	header := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	header.SetBackgroundColor(bg)
	header.SetText(fmt.Sprintf(
		"\n[::b][#cba6f7]%s New Backup[-][-]\n[#a6adc8]Choose the database first. Scheduled backups always use a saved connection.[-]",
		iconBackup,
	))

	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).SetTitle(" Choose Database ").SetTitleColor(mauve).SetBorderColor(surface1)
	list.SetBackgroundColor(bg)
	list.SetMainTextColor(text).SetSecondaryTextColor(subtext0)
	list.SetSelectedBackgroundColor(surface0).SetSelectedTextColor(green)
	for _, choice := range choices {
		if choice.addNew {
			list.AddItem(fmt.Sprintf("  [#a6e3a1]+[-] [::b]%s[-]", tview.Escape(choice.name)), "  "+tview.Escape(choice.detail), 'n', nil)
			continue
		}
		list.AddItem(fmt.Sprintf("  %s [::b]%s[-]", iconDatabase, tview.Escape(choice.name)), "  "+tview.Escape(choice.detail), 0, nil)
	}

	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(" [yellow]Enter[-] Continue  │  [yellow]N[-] Add connection  │  [yellow]Esc[-] Cancel ")

	addConnection := func() {
		a.showNewConnectionForBackup(func(saved config.ConnectionConfig) {
			a.pages.RemovePage(pageBackupConnectionPicker)
			if returnFocus != nil {
				a.app.SetFocus(returnFocus)
			}
			a.showBackupJobFormForConnection(nil, saved.ID)
		})
	}
	continueWithSelection := func() {
		index := list.GetCurrentItem()
		if index < 0 || index >= len(choices) {
			return
		}
		choice := choices[index]
		if choice.addNew {
			addConnection()
			return
		}
		a.pages.RemovePage(pageBackupConnectionPicker)
		if returnFocus != nil {
			a.app.SetFocus(returnFocus)
		}
		a.showBackupJobFormForConnection(nil, choice.connectionID)
	}
	closePicker := func() {
		a.pages.RemovePage(pageBackupConnectionPicker)
		a.pages.ShowPage(pageBackupCenter)
		if returnFocus != nil {
			a.app.SetFocus(returnFocus)
		}
	}
	list.SetSelectedFunc(func(_ int, _ string, _ string, _ rune) { continueWithSelection() })
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			closePicker()
			return nil
		}
		if event.Key() == tcell.KeyRune && event.Modifiers()&(tcell.ModCtrl|tcell.ModAlt|tcell.ModMeta) == 0 {
			switch event.Rune() {
			case 'n', 'N', 'a', 'A':
				addConnection()
				return nil
			}
		}
		return event
	})

	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 4, 0, false).
		AddItem(list, 0, 1, true).
		AddItem(footer, 1, 0, false)
	w, h := a.modalSize(60, 88, 14, 26)
	grid := tview.NewGrid().SetColumns(0, w, 0).SetRows(0, h, 0).AddItem(content, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage(pageBackupConnectionPicker, grid, true, true)
	a.app.SetFocus(list)
}

func backupConnectionSummary(connection config.ConnectionConfig) string {
	typeLabel := connection.TypeLabel()
	switch connection.Type {
	case config.SQLite:
		return fmt.Sprintf("%s · %s", typeLabel, nonEmptyOr(connection.FilePath, "path not set"))
	case config.Turso:
		return fmt.Sprintf("%s · %s", typeLabel, nonEmptyOr(connection.Host, "URL not set"))
	case config.CloudflareD1:
		return fmt.Sprintf("%s · database %s", typeLabel, nonEmptyOr(connection.DatabaseID, "ID not set"))
	default:
		host := nonEmptyOr(connection.Host, "host not set")
		if strings.TrimSpace(connection.Port) != "" {
			host += ":" + strings.TrimSpace(connection.Port)
		}
		database := nonEmptyOr(connection.Database, "database not set")
		return fmt.Sprintf("%s · %s/%s", typeLabel, host, database)
	}
}

func backupJobConnectionLabel(connections []config.ConnectionConfig, connectionID string) string {
	index := backupConnectionIndex(connections, connectionID)
	if index < 0 {
		return "missing connection"
	}
	return nonEmptyOr(strings.TrimSpace(connections[index].Name), "unnamed connection")
}

func backupJobConnectionDetail(connections []config.ConnectionConfig, connectionID string) string {
	index := backupConnectionIndex(connections, connectionID)
	if index < 0 {
		return "Missing saved connection — edit this plan before running"
	}
	connection := connections[index]
	return fmt.Sprintf("%s · %s", nonEmptyOr(strings.TrimSpace(connection.Name), "unnamed"), backupConnectionSummary(connection))
}

func backupRetentionSummary(retention backupcore.Retention) string {
	parts := make([]string, 0, 3)
	if retention.KeepLast > 0 {
		parts = append(parts, fmt.Sprintf("latest %d", retention.KeepLast))
	}
	if retention.MaxAgeDays > 0 {
		parts = append(parts, fmt.Sprintf("%d days", retention.MaxAgeDays))
	}
	if retention.MaxTotalBytes > 0 {
		parts = append(parts, backupcore.FormatByteSize(uint64(retention.MaxTotalBytes)))
	}
	if len(parts) == 0 {
		return "unlimited"
	}
	return strings.Join(parts, " / ")
}

const (
	backupFormLabelConnection  = "Database Connection"
	backupFormLabelDestination = "Save To [F2 choose]"
	backupFormLabelAdvanced    = "Advanced Options"
)

func backupConnectionOptionLabel(connection config.ConnectionConfig) string {
	return fmt.Sprintf("%s  [%s]", nonEmptyOr(strings.TrimSpace(connection.Name), "unnamed"), connection.TypeLabel())
}

func backupDefaultJobName(connection config.ConnectionConfig) string {
	return nonEmptyOr(strings.TrimSpace(connection.Name), "database") + " daily backup"
}

type backupJobFormDraft struct {
	job               backupcore.Job
	everyMinutes      string
	weekdays          string
	compressionLevel  string
	keepLatest        string
	maxAgeDays        string
	maxStorageGiB     string
	maxStorageChanged bool
	timeoutMinutes    string
	smtpPort          string
	recipients        string
	expanded          bool
}

func (a *App) showBackupJobFormForConnection(existing *backupcore.Job, preferredConnectionID string) {
	if len(a.store.Connections) == 0 {
		a.showBackupConnectionPicker()
		return
	}
	returnFocus := a.app.GetFocus()
	job := backupcore.Job{
		Enabled: true, FilenameTemplate: backupcore.DefaultFilenameTemplate,
		Compression: backupcore.CompressionZstd, CompressionLevel: 3,
		Encryption: backupcore.EncryptionNone,
		Schedule:   backupcore.Schedule{Kind: backupcore.ScheduleDaily, TimeOfDay: "02:00", Timezone: "Local", RunMissedOnWake: true},
		Retention:  backupcore.Retention{KeepLast: 14, MaxAgeDays: 30}, TimeoutMinutes: backupcore.DefaultTimeoutMinutes,
		Notification: backupcore.EmailNotification{
			Policy:   backupcore.NotificationNever,
			SMTPHost: "smtp.gmail.com", SMTPPort: 587, TLSMode: backupcore.SMTPTLSStartTLS,
		},
	}
	if home, err := os.UserHomeDir(); err == nil {
		job.Destination = filepath.Join(home, "dbterm-backups")
	}
	if existing != nil {
		job = *existing
	} else if strings.TrimSpace(preferredConnectionID) != "" {
		job.ConnectionID = strings.TrimSpace(preferredConnectionID)
	}

	connections := make([]string, len(a.store.Connections))
	for index, conn := range a.store.Connections {
		connections[index] = backupConnectionOptionLabel(conn)
	}
	connectionIndex := backupConnectionIndex(a.store.Connections, job.ConnectionID)
	if job.ConnectionID == "" {
		connectionIndex = 0
		job.ConnectionID = a.store.Connections[connectionIndex].ID
	} else if existing == nil && backupConnectionIndex(a.store.Connections, job.ConnectionID) < 0 {
		job.ConnectionID = a.store.Connections[0].ID
		connectionIndex = 0
	}
	if job.Name == "" {
		nameConnectionIndex := connectionIndex
		if nameConnectionIndex < 0 {
			nameConnectionIndex = 0
		}
		job.Name = backupDefaultJobName(a.store.Connections[nameConnectionIndex])
	}
	if job.Notification.Policy == "" {
		job.Notification.Policy = backupcore.NotificationNever
	}
	if job.Notification.SMTPHost == "" {
		job.Notification.SMTPHost = "smtp.gmail.com"
	}
	if job.Notification.SMTPPort == 0 {
		job.Notification.SMTPPort = 587
	}
	if job.Notification.TLSMode == "" {
		job.Notification.TLSMode = backupcore.SMTPTLSStartTLS
	}
	if job.FilenameTemplate == "" {
		job.FilenameTemplate = backupcore.DefaultFilenameTemplate
	}
	if job.Schedule.TimeOfDay == "" {
		job.Schedule.TimeOfDay = "02:00"
	}
	if job.Schedule.Timezone == "" {
		job.Schedule.Timezone = "Local"
	}
	if len(job.Schedule.Weekdays) == 0 {
		job.Schedule.Weekdays = []int{int(time.Monday)}
	}

	scheduleOptions := []string{"Manual / run on demand", "Every N minutes", "Daily", "Weekly"}
	compressionOptions := []string{"Zstandard (zstd)", "Gzip", "ZIP", "None"}
	notificationOptions := []string{"Never", "Failures only", "Success only", "Success and failure"}
	tlsOptions := []string{"STARTTLS (recommended)", "Implicit TLS", "None (localhost only)"}
	draft := backupJobFormDraft{
		job:              job,
		everyMinutes:     strconv.Itoa(max(5, job.Schedule.EveryMinutes)),
		weekdays:         weekdayText(job.Schedule.Weekdays),
		compressionLevel: strconv.Itoa(job.CompressionLevel),
		keepLatest:       strconv.Itoa(job.Retention.KeepLast),
		maxAgeDays:       strconv.Itoa(job.Retention.MaxAgeDays),
		maxStorageGiB:    formatOptionalGiB(job.Retention.MaxTotalBytes),
		timeoutMinutes:   strconv.Itoa(job.TimeoutMinutes),
		smtpPort:         strconv.Itoa(job.Notification.SMTPPort),
		recipients:       strings.Join(job.Notification.Recipients, ", "),
		expanded:         existing != nil,
	}

	container := tview.NewFlex().SetDirection(tview.FlexRow)
	var renderForm func(focusLabel string)
	closeForm := func() {
		a.pages.RemovePage(pageBackupForm)
		a.pages.ShowPage(pageBackupCenter)
		if returnFocus != nil {
			a.app.SetFocus(returnFocus)
		}
	}
	chooseFolder := func() {
		ctx, cancel := context.WithCancel(context.Background())
		token := a.showLoadingModal("Opening the system folder chooser...", withLoadingCancelOutcome("Press Esc to keep the typed destination.", cancel))
		go func(initial string) {
			selected, err := folderpicker.Choose(ctx, initial)
			cancel()
			a.app.QueueUpdateDraw(func() {
				if !a.finishLoadingModal(token) {
					return
				}
				if err != nil {
					if errors.Is(err, folderpicker.ErrCancelled) || errors.Is(err, context.Canceled) {
						a.pages.ShowPage(pageBackupForm)
						return
					}
					a.ShowAlert(fmt.Sprintf("%s Could not open a graphical folder chooser:\n\n%v\n\nThe destination field remains editable; paste or type a path instead.", iconInfo, err), pageBackupForm)
					return
				}
				draft.job.Destination = selected
				renderForm(backupFormLabelDestination)
			})
		}(draft.job.Destination)
	}
	addConnection := func() {
		previousIndex := backupConnectionIndex(a.store.Connections, draft.job.ConnectionID)
		previousName := ""
		if previousIndex >= 0 {
			previousName = backupDefaultJobName(a.store.Connections[previousIndex])
		}
		a.showNewConnectionForBackup(func(saved config.ConnectionConfig) {
			connections = append(connections, backupConnectionOptionLabel(saved))
			draft.job.ConnectionID = saved.ID
			if strings.TrimSpace(draft.job.Name) == "" || draft.job.Name == previousName {
				draft.job.Name = backupDefaultJobName(saved)
			}
			renderForm(backupFormLabelConnection)
		})
	}
	testEmail := func() {
		notification, err := backupNotificationFromDraft(&draft)
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s Email settings are incomplete:\n\n%v", iconWarn, err), pageBackupForm)
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		const cancelText = "Press Esc to cancel the SMTP test. No backup is created or changed."
		title := fmt.Sprintf("Testing SMTP delivery through %s:%d...", notification.SMTPHost, notification.SMTPPort)
		token := a.showLoadingModal(title, withLoadingCancelOutcome(cancelText, cancel))
		go func() {
			err := backupcore.TestEmailNotification(ctx, notification)
			cancel()
			a.app.QueueUpdateDraw(func() {
				if !a.finishLoadingModal(token) {
					return
				}
				if err != nil {
					a.ShowAlert(fmt.Sprintf("%s Test email failed:\n\n%v\n\nThe password is never included in this diagnostic.", iconFail, err), pageBackupForm)
					return
				}
				a.ShowAlert(fmt.Sprintf("%s Test email sent. Check the configured recipient inbox and spam folder. No backup was run or modified.", iconSuccess), pageBackupForm)
			})
		}()
	}

	saveJob := func() {
		selectedConnection := backupConnectionIndex(a.store.Connections, draft.job.ConnectionID)
		if selectedConnection < 0 || selectedConnection >= len(a.store.Connections) {
			a.ShowAlert(fmt.Sprintf("%s Select a saved connection.", iconInfo), pageBackupForm)
			return
		}
		candidate := draft.job
		var parseErr error
		if candidate.Schedule.EveryMinutes, parseErr = parseBackupFormInt("Every minutes", draft.everyMinutes, 1, math.MaxInt); parseErr != nil {
			a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, parseErr), pageBackupForm)
			return
		}
		if candidate.CompressionLevel, parseErr = parseBackupFormInt("Compression level", draft.compressionLevel, 0, 22); parseErr != nil {
			a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, parseErr), pageBackupForm)
			return
		}
		if candidate.Retention.KeepLast, parseErr = parseBackupFormInt("Keep latest", draft.keepLatest, 0, math.MaxInt); parseErr != nil {
			a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, parseErr), pageBackupForm)
			return
		}
		if candidate.Retention.MaxAgeDays, parseErr = parseBackupFormInt("Max age days", draft.maxAgeDays, 0, math.MaxInt); parseErr != nil {
			a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, parseErr), pageBackupForm)
			return
		}
		if draft.maxStorageChanged {
			if candidate.Retention.MaxTotalBytes, parseErr = parseOptionalGiB(draft.maxStorageGiB); parseErr != nil {
				a.ShowAlert(fmt.Sprintf("%s Maximum stored GiB: %v", iconWarn, parseErr), pageBackupForm)
				return
			}
		}
		if candidate.TimeoutMinutes, parseErr = parseBackupFormInt("Timeout minutes", draft.timeoutMinutes, 1, 24*60); parseErr != nil {
			a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, parseErr), pageBackupForm)
			return
		}
		if candidate.Notification, parseErr = backupNotificationFromDraft(&draft); parseErr != nil {
			a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, parseErr), pageBackupForm)
			return
		}
		weekdays, parseErr := parseWeekdays(draft.weekdays)
		if parseErr != nil {
			a.ShowAlert(fmt.Sprintf("%s %v", iconWarn, parseErr), pageBackupForm)
			return
		}
		candidate.Schedule.Weekdays = weekdays
		if candidate.Schedule.Kind == backupcore.ScheduleManual {
			candidate.Enabled = false
		}
		candidate.NextRunAt = time.Time{}
		if candidate.Destination == "" {
			a.ShowAlert(fmt.Sprintf("%s Destination folder is required.", iconInfo), pageBackupForm)
			return
		}
		expandedDestination, pathErr := expandHomePath(candidate.Destination)
		if pathErr != nil {
			a.ShowAlert(fmt.Sprintf("%s Invalid destination:\n\n%v", iconWarn, pathErr), pageBackupForm)
			return
		}
		destination, pathErr := filepath.Abs(filepath.Clean(expandedDestination))
		if pathErr != nil {
			a.ShowAlert(fmt.Sprintf("%s Invalid destination:\n\n%v", iconWarn, pathErr), pageBackupForm)
			return
		}
		if mkdirErr := os.MkdirAll(destination, 0o700); mkdirErr != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not create destination:\n\n%v", iconWarn, mkdirErr), pageBackupForm)
			return
		}
		candidate.Destination = destination
		if err := a.backupStore.UpsertJob(context.Background(), &candidate); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not save backup job:\n\n%v", iconWarn, err), pageBackupForm)
			return
		}
		a.backupCenterSelectedJob = candidate.ID
		a.pages.RemovePage(pageBackupForm)
		a.showBackupCenter()
		if candidate.Enabled && candidate.Schedule.Kind != backupcore.ScheduleManual {
			a.offerBackupAgentStart()
		}
	}

	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(" [yellow]Tab / Shift+Tab[-] Move  │  [yellow]F2[-] Choose folder  │  [yellow]F3[-] Disk space  │  [yellow]Ctrl+N[-] Add database  │  [yellow]Esc[-] Cancel\n [#a6adc8]Only database, destination, and schedule are required; advanced settings already have safe defaults.[-] ")

	renderForm = func(focusLabel string) {
		form := tview.NewForm()
		formTitle := " New Backup Plan "
		if existing != nil {
			formTitle = " Edit Backup Plan "
		}
		form.SetBorder(true).SetTitle(formTitle).SetTitleColor(mauve).SetBorderColor(surface1)
		form.SetBackgroundColor(bg)
		form.SetFieldBackgroundColor(mantle).SetFieldTextColor(text).SetLabelColor(text).
			SetButtonBackgroundColor(surface1).SetButtonTextColor(green)
		addBackupFormSection(form, "BACKUP SETUP", "Choose the database, destination, and timing")
		connectionIndex := backupConnectionIndex(a.store.Connections, draft.job.ConnectionID)
		form.AddDropDown(backupFormLabelConnection, connections, connectionIndex, func(_ string, index int) {
			if index >= 0 && index < len(a.store.Connections) {
				oldIndex := backupConnectionIndex(a.store.Connections, draft.job.ConnectionID)
				oldDefaultName := ""
				if oldIndex >= 0 {
					oldDefaultName = backupDefaultJobName(a.store.Connections[oldIndex])
				}
				draft.job.ConnectionID = a.store.Connections[index].ID
				if strings.TrimSpace(draft.job.Name) == "" || draft.job.Name == oldDefaultName {
					draft.job.Name = backupDefaultJobName(a.store.Connections[index])
					if nameField, ok := form.GetFormItemByLabel("Backup Name").(*tview.InputField); ok {
						nameField.SetText(draft.job.Name)
					}
				}
			}
		})
		if connectionIndex < 0 {
			form.AddTextView("Connection Status", "[#f9e2af]The original saved connection is missing. Choose a replacement before saving or running this plan.[-]", 0, 1, true, false)
		}
		form.AddInputField("Backup Name", draft.job.Name, 48, nil, func(value string) { draft.job.Name = value })
		var storageView *tview.TextView
		form.AddInputField(backupFormLabelDestination, draft.job.Destination, 72, nil, func(value string) {
			draft.job.Destination = value
			if storageView != nil {
				storageView.SetText("[#a6adc8]Path changed; press F3 to inspect its destination volume.[-]")
			}
		})
		form.AddTextView("Disk Space", backupDestinationStorageText(draft.job.Destination), 0, 2, true, false)
		storageView, _ = form.GetFormItemByLabel("Disk Space").(*tview.TextView)
		form.AddDropDown("Schedule", scheduleOptions, backupScheduleIndex(draft.job.Schedule.Kind), func(_ string, index int) {
			if index >= 0 && index < 4 {
				kind := []backupcore.ScheduleKind{backupcore.ScheduleManual, backupcore.ScheduleInterval, backupcore.ScheduleDaily, backupcore.ScheduleWeekly}[index]
				if kind != draft.job.Schedule.Kind {
					draft.job.Schedule.Kind = kind
					if kind == backupcore.ScheduleManual {
						draft.job.Enabled = false
					}
					renderForm("Schedule")
				}
			}
		})
		if draft.job.Schedule.Kind == backupcore.ScheduleManual {
			form.AddTextView("Agent", "[#89b4fa]On demand only.[-] Run with R; no background schedule is enabled.", 0, 1, true, false)
		} else {
			form.AddCheckbox("Enable Schedule", draft.job.Enabled, func(value bool) { draft.job.Enabled = value })
		}
		form.AddCheckbox(backupFormLabelAdvanced, draft.expanded, func(value bool) {
			if value == draft.expanded {
				return
			}
			draft.expanded = value
			renderForm(backupFormLabelAdvanced)
		})

		if draft.expanded {
			addBackupFormSection(form, "TIMING", "Only fields relevant to the chosen schedule are used")
			form.AddInputField("Every Minutes (interval)", draft.everyMinutes, 8, func(value string, _ rune) bool { return digitsOnly(value) }, func(value string) { draft.everyMinutes = value })
			form.AddInputField("Run At HH:MM (daily/weekly)", nonEmptyOr(draft.job.Schedule.TimeOfDay, "02:00"), 8, nil, func(value string) { draft.job.Schedule.TimeOfDay = value })
			form.AddInputField("Weekdays (weekly)", draft.weekdays, 34, nil, func(value string) { draft.weekdays = value })
			form.AddInputField("Timezone", nonEmptyOr(draft.job.Schedule.Timezone, "Local"), 32, nil, func(value string) { draft.job.Schedule.Timezone = value })
			form.AddCheckbox("Catch up one missed run", draft.job.Schedule.RunMissedOnWake, func(value bool) { draft.job.Schedule.RunMissedOnWake = value })

			addBackupFormSection(form, "STORAGE & RETENTION", "Newest verified artifact is always retained")
			form.AddInputField("Filename Template", draft.job.FilenameTemplate, 64, nil, func(value string) { draft.job.FilenameTemplate = value })
			form.AddInputField("Keep Latest", draft.keepLatest, 8, func(value string, _ rune) bool { return digitsOnly(value) }, func(value string) { draft.keepLatest = value })
			form.AddInputField("Max Age Days (0 = off)", draft.maxAgeDays, 8, func(value string, _ rune) bool { return digitsOnly(value) }, func(value string) { draft.maxAgeDays = value })
			form.AddInputField("Max Stored GiB (0 = off)", draft.maxStorageGiB, 10, func(value string, _ rune) bool { return decimalOnly(value) }, func(value string) {
				draft.maxStorageGiB = value
				draft.maxStorageChanged = true
			})

			addBackupFormSection(form, "COMPRESSION & ENCRYPTION", "zstd level 3 is the balanced default")
			form.AddDropDown("Compression", compressionOptions, backupCompressionIndex(draft.job.Compression), func(_ string, index int) {
				if index >= 0 && index < 4 {
					draft.job.Compression = []backupcore.Compression{backupcore.CompressionZstd, backupcore.CompressionGzip, backupcore.CompressionZip, backupcore.CompressionNone}[index]
				}
			})
			form.AddInputField("Compression Level", draft.compressionLevel, 5, func(value string, _ rune) bool { return digitsOnly(value) }, func(value string) { draft.compressionLevel = value })
			form.AddCheckbox("Encrypt with age X25519", draft.job.Encryption == backupcore.EncryptionAge, func(value bool) {
				if value {
					draft.job.Encryption = backupcore.EncryptionAge
				} else {
					draft.job.Encryption = backupcore.EncryptionNone
				}
			})
			form.AddInputField("age Recipient (age1…)", draft.job.AgeRecipient, 72, nil, func(value string) { draft.job.AgeRecipient = value })
			form.AddInputField("Timeout Minutes", draft.timeoutMinutes, 8, func(value string, _ rune) bool { return digitsOnly(value) }, func(value string) { draft.timeoutMinutes = value })

			addBackupFormSection(form, "EMAIL NOTIFICATIONS", "Gmail defaults shown; any SMTP server is supported")
			form.AddDropDown("Send Email", notificationOptions, backupNotificationIndex(draft.job.Notification.Policy), func(_ string, index int) {
				if index >= 0 && index < 4 {
					draft.job.Notification.Policy = []backupcore.NotificationPolicy{backupcore.NotificationNever, backupcore.NotificationFailure, backupcore.NotificationSuccess, backupcore.NotificationBoth}[index]
				}
			})
			form.AddInputField("SMTP Host", draft.job.Notification.SMTPHost, 48, nil, func(value string) { draft.job.Notification.SMTPHost = value })
			form.AddInputField("SMTP Port", draft.smtpPort, 8, func(value string, _ rune) bool { return digitsOnly(value) }, func(value string) { draft.smtpPort = value })
			form.AddDropDown("TLS", tlsOptions, backupTLSIndex(draft.job.Notification.TLSMode), func(_ string, index int) {
				if index >= 0 && index < 3 {
					draft.job.Notification.TLSMode = []backupcore.SMTPTLSMode{backupcore.SMTPTLSStartTLS, backupcore.SMTPTLSImplicit, backupcore.SMTPTLSNone}[index]
				}
			})
			form.AddInputField("Recipients (comma separated)", draft.recipients, 72, nil, func(value string) { draft.recipients = value })
			form.AddInputField("SMTP Username", draft.job.Notification.Username, 56, nil, func(value string) { draft.job.Notification.Username = value })
			form.AddPasswordField("SMTP App Password", draft.job.Notification.Password, 40, '•', func(value string) { draft.job.Notification.Password = value })
			form.AddInputField("From Address", draft.job.Notification.From, 56, nil, func(value string) { draft.job.Notification.From = value })
			form.AddTextView("Email Test", "[#a6adc8]Send a test even when the delivery policy is Never; no job or backup is modified.[-]", 0, 1, true, false)
		}

		form.AddButton("Save Backup", saveJob)
		form.AddButton("Choose Folder…", chooseFolder)
		form.AddButton("Add Database…", addConnection)
		if draft.expanded {
			form.AddButton("Send Test Email", testEmail)
		}
		form.AddButton("Cancel", closeForm)
		form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyF2 {
				chooseFolder()
				return nil
			}
			if event.Key() == tcell.KeyF3 {
				if storageView != nil {
					storageView.SetText(backupDestinationStorageText(draft.job.Destination))
				}
				return nil
			}
			if event.Key() == tcell.KeyCtrlN {
				addConnection()
				return nil
			}
			if event.Key() == tcell.KeyEscape {
				closeForm()
				return nil
			}
			return event
		})
		container.Clear().AddItem(form, 0, 1, true).AddItem(footer, 2, 0, false)
		if focusLabel != "" {
			setBackupFormFocus(form, focusLabel)
		}
		a.app.SetFocus(form)
	}

	w, h := a.modalSize(78, 116, 25, 35)
	grid := tview.NewGrid().SetColumns(0, w, 0).SetRows(0, h, 0).AddItem(container, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage(pageBackupForm, grid, true, true)
	renderForm("")
}

func addBackupFormSection(form *tview.Form, title, summary string) {
	if form == nil {
		return
	}
	form.AddTextView("", fmt.Sprintf("[::b][#89b4fa]%s[-][-]  [#a6adc8]%s[-]", tview.Escape(title), tview.Escape(summary)), 0, 1, true, false)
}

func backupConnectionIndex(connections []config.ConnectionConfig, connectionID string) int {
	for index, connection := range connections {
		if connection.ID == connectionID {
			return index
		}
	}
	return -1
}

func backupScheduleIndex(kind backupcore.ScheduleKind) int {
	if index, ok := map[backupcore.ScheduleKind]int{
		backupcore.ScheduleManual: 0, backupcore.ScheduleInterval: 1,
		backupcore.ScheduleDaily: 2, backupcore.ScheduleWeekly: 3,
	}[kind]; ok {
		return index
	}
	return 2
}

func backupCompressionIndex(compression backupcore.Compression) int {
	if index, ok := map[backupcore.Compression]int{
		backupcore.CompressionZstd: 0, backupcore.CompressionGzip: 1,
		backupcore.CompressionZip: 2, backupcore.CompressionNone: 3,
	}[compression]; ok {
		return index
	}
	return 0
}

func backupNotificationIndex(policy backupcore.NotificationPolicy) int {
	if index, ok := map[backupcore.NotificationPolicy]int{
		backupcore.NotificationNever: 0, backupcore.NotificationFailure: 1,
		backupcore.NotificationSuccess: 2, backupcore.NotificationBoth: 3,
	}[policy]; ok {
		return index
	}
	return 0
}

func backupTLSIndex(mode backupcore.SMTPTLSMode) int {
	if index, ok := map[backupcore.SMTPTLSMode]int{
		backupcore.SMTPTLSStartTLS: 0, backupcore.SMTPTLSImplicit: 1, backupcore.SMTPTLSNone: 2,
	}[mode]; ok {
		return index
	}
	return 0
}

func setBackupFormFocus(form *tview.Form, label string) {
	if form == nil {
		return
	}
	for index := 0; index < form.GetFormItemCount(); index++ {
		if form.GetFormItem(index).GetLabel() == label {
			form.SetFocus(index)
			return
		}
	}
}

func parseBackupFormInt(label, value string, minimum, maximum int) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be a whole number from %d to %d", label, minimum, maximum)
	}
	return parsed, nil
}

func formatOptionalGiB(bytes int64) string {
	if bytes <= 0 {
		return "0"
	}
	value := float64(bytes) / float64(backupDecodedGiB)
	return strconv.FormatFloat(value, 'f', 3, 64)
}

func backupByteSize(bytes uint64) string {
	return backupcore.FormatByteSize(bytes)
}

func parseOptionalGiB(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("enter 0 for no limit, or a positive size")
	}
	gib, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(gib) || math.IsInf(gib, 0) || gib < 0 {
		return 0, fmt.Errorf("enter 0 for no limit, or a positive number")
	}
	if gib > float64(math.MaxInt64)/float64(backupDecodedGiB) {
		return 0, fmt.Errorf("value is too large")
	}
	return int64(math.Round(gib * float64(backupDecodedGiB))), nil
}

func decimalOnly(value string) bool {
	if value == "" {
		return true
	}
	if strings.Count(value, ".") > 1 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && character != '.' {
			return false
		}
	}
	return true
}

func splitBackupEmailRecipients(value string) []string {
	var recipients []string
	for _, recipient := range strings.FieldsFunc(value, func(character rune) bool {
		return character == ',' || character == ';' || character == '\n'
	}) {
		if recipient = strings.TrimSpace(recipient); recipient != "" {
			recipients = append(recipients, recipient)
		}
	}
	return recipients
}

func backupNotificationFromDraft(draft *backupJobFormDraft) (backupcore.EmailNotification, error) {
	if draft == nil {
		return backupcore.EmailNotification{}, fmt.Errorf("email settings are unavailable")
	}
	notification := draft.job.Notification
	notification.Recipients = splitBackupEmailRecipients(draft.recipients)
	port, err := parseBackupFormInt("SMTP port", draft.smtpPort, 1, 65535)
	if err != nil {
		return backupcore.EmailNotification{}, err
	}
	notification.SMTPPort = port
	return notification, nil
}

func backupDestinationStorageText(path string) string {
	path = strings.TrimSpace(path)
	stagePath, stagePathErr := backupcore.DefaultStagingPath()
	stageLine := backupStorageVolumeLine("STAGE", stagePath, stagePathErr)
	if path == "" {
		return "[#f9e2af]DEST[-] Enter or choose a folder.\n" + stageLine
	}
	expanded, expandErr := expandHomePath(path)
	if expandErr != nil {
		return fmt.Sprintf("[#f9e2af]DEST[-] %s\n%s", tview.Escape(expandErr.Error()), stageLine)
	}
	return backupStorageVolumeLine("DEST", expanded, nil) + "\n" + stageLine
}

func backupStorageVolumeLine(label, path string, pathErr error) string {
	if pathErr != nil {
		return fmt.Sprintf("[#f9e2af]%s[-] unavailable: %s", label, tview.Escape(pathErr.Error()))
	}
	usage, err := backupcore.DestinationDiskUsage(path)
	if err != nil {
		return fmt.Sprintf("[#f9e2af]%s[-] unavailable: %s", label, tview.Escape(err.Error()))
	}
	volume := strings.TrimSpace(usage.Volume)
	if volume == "" {
		volume = "volume"
	}
	return fmt.Sprintf("[#89b4fa]%s[-] [#a6e3a1]%s[-]  [#a6adc8]%s available • %s total[-]",
		label, tview.Escape(volume), backupByteSize(usage.AvailableBytes), backupByteSize(usage.CapacityBytes))
}

func (a *App) runBackupJobNow(jobID string) {
	ctx, cancel := context.WithCancel(context.Background())
	var canceled atomic.Bool
	const cancelText = "Press Esc to cancel safely."
	loadingTitle := fmt.Sprintf("%s Running backup job...", iconBackup)
	token := a.showLoadingModal(loadingTitle, withLoadingCancelOutcome(cancelText, func() {
		canceled.Store(true)
		cancel()
	}))
	go func() {
		started := time.Now()
		var lastProgress atomic.Value
		lastProgress.Store(backupcore.ProgressEvent{Phase: "preflight", Message: "preparing the backup"})
		run, err := backupcore.RunJobNowWithProgress(ctx, a.backupStore, jobID, func(event backupcore.ProgressEvent) {
			if event.Elapsed <= 0 {
				event.Elapsed = time.Since(started)
			}
			lastProgress.Store(event)
			a.updateBackupProgress(token, loadingTitle, event, cancelText)
		})
		cancel()
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				return
			}
			a.backupCenterSelectedJob = jobID
			a.showBackupCenter()
			if canceled.Load() && strings.TrimSpace(run.Artifact.Path) != "" {
				detail := ""
				if err != nil {
					detail = "\n\nPost-backup outcome: " + tview.Escape(err.Error())
				}
				a.ShowAlert(fmt.Sprintf("%s Cancel was requested after the complete artifact was safely published. It was preserved at:\n\n%s%s", iconWarn, tview.Escape(run.Artifact.Path), detail), pageBackupCenter)
				return
			}
			if canceled.Load() && (run.Status == backupcore.RunCanceled || errors.Is(err, context.Canceled)) {
				a.ShowAlert(fmt.Sprintf("%s Backup canceled. No partial artifact was published.", iconWarn), pageBackupCenter)
				return
			}
			if err != nil {
				last := lastProgress.Load().(backupcore.ProgressEvent)
				a.ShowAlert(fmt.Sprintf("%s Backup failed:\n\n%s\n\nLast phase: %s — %s", iconFail, tview.Escape(err.Error()), tview.Escape(nonEmptyOr(last.Phase, "unknown")), tview.Escape(nonEmptyOr(last.Message, "no progress detail"))), pageBackupCenter)
				return
			}
			notification := backupRunNotificationSummary(run)
			if strings.TrimSpace(run.NotificationError) != "" {
				notification += ": " + run.NotificationError
			}
			a.ShowAlert(fmt.Sprintf("%s Backup complete\n\nPath: %s\nSize: %s\nSHA-256: %s\nNotification: %s", iconSuccess, tview.Escape(run.Artifact.Path), backupByteSize(uint64(run.Artifact.Size)), run.Artifact.SHA256, tview.Escape(notification)), pageBackupCenter)
		})
	}()
}

func (a *App) showBackupHistory() {
	runs, err := a.backupStore.ListRuns(context.Background(), "", 250)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not load backup history:\n\n%v", iconWarn, err), pageBackupCenter)
		return
	}
	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).SetTitle(fmt.Sprintf(" Backup History (%d) ", len(runs))).SetTitleColor(mauve).SetBorderColor(surface1)
	list.SetBackgroundColor(bg)
	list.SetMainTextColor(text).SetSecondaryTextColor(subtext0).SetSelectedBackgroundColor(surface0).SetSelectedTextColor(green)
	if len(runs) == 0 {
		list.AddItem("  [#6c7086]No backup runs recorded[-]", "  Run a job now or enable the background agent.", 0, nil)
	}
	jobNames := make(map[string]string)
	if jobs, jobsErr := a.backupStore.ListJobs(context.Background()); jobsErr == nil {
		for _, job := range jobs {
			jobNames[job.ID] = job.Name
		}
	}
	for _, run := range runs {
		run := run
		color := "green"
		if run.Status == backupcore.RunFailed || run.Status == backupcore.RunCanceled {
			color = "red"
		}
		jobName := nonEmptyOr(jobNames[run.JobID], run.JobID)
		result := nonEmptyOr(run.Artifact.Path, run.Error)
		if strings.TrimSpace(result) == "" {
			result = "no artifact detail"
		}
		list.AddItem(fmt.Sprintf("  [%s]%s[-]  %s", color, strings.ToUpper(string(run.Status)), run.StartedAt.Local().Format("2006-01-02 15:04:05")),
			tview.Escape(fmt.Sprintf("  %s  │  %s  │  %s", jobName, result, backupRunNotificationSummary(run))), 0, func() {
				a.showBackupRunDetails(run, jobName)
			})
	}
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyBackspace || event.Key() == tcell.KeyBackspace2 {
			a.pages.RemovePage("backupHistory")
			a.pages.ShowPage(pageBackupCenter)
			return nil
		}
		return event
	})
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter).SetText(" [yellow]↑/↓[-] Browse  │  [yellow]Enter[-] Run details  │  Artifacts survive uninstall/purge  │  [yellow]Esc[-] Back ")
	footer.SetBackgroundColor(crust)
	layout := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(list, 0, 1, true).AddItem(footer, 1, 0, false)
	a.pages.AddAndSwitchToPage("backupHistory", layout, true)
	a.app.SetFocus(list)
}

func backupRunNotificationSummary(run backupcore.Run) string {
	switch {
	case run.NotificationSent:
		return "email sent"
	case strings.TrimSpace(run.NotificationError) != "":
		return "email failed"
	case run.NotificationAttempted:
		return "email attempted"
	default:
		return "email not requested"
	}
}

func (a *App) showBackupRunDetails(run backupcore.Run, jobName string) {
	finished := "not finished"
	duration := time.Since(run.StartedAt)
	if !run.FinishedAt.IsZero() {
		finished = run.FinishedAt.Local().Format(time.RFC3339)
		duration = run.FinishedAt.Sub(run.StartedAt)
	}
	if duration < 0 {
		duration = 0
	}
	artifact := "No artifact recorded."
	if strings.TrimSpace(run.Artifact.Path) != "" {
		artifactSize := run.Artifact.Size
		if artifactSize < 0 {
			artifactSize = 0
		}
		artifact = fmt.Sprintf("Path: %s\nSize: %s\nSHA-256: %s\nVerified: %t",
			run.Artifact.Path, backupByteSize(uint64(artifactSize)), nonEmptyOr(run.Artifact.SHA256, "not recorded"), run.Artifact.Verified)
	}
	failure := nonEmptyOr(run.Error, "none")
	notification := backupRunNotificationSummary(run)
	if strings.TrimSpace(run.NotificationError) != "" {
		notification += ": " + run.NotificationError
	}
	message := fmt.Sprintf("%s Backup run details\n\nJob: %s\nRun: %s\nTrigger: %s\nStatus: %s\nStarted: %s\nFinished: %s\nDuration: %s\n\n%s\n\nBackup error: %s\nNotification: %s",
		iconBackup, tview.Escape(nonEmptyOr(jobName, run.JobID)), tview.Escape(run.ID), run.Trigger, run.Status,
		run.StartedAt.Local().Format(time.RFC3339), finished, formatBackupProgressDuration(duration),
		tview.Escape(artifact), tview.Escape(failure), tview.Escape(notification))
	modal := tview.NewModal().SetText(message).AddButtons([]string{" Close "}).SetDoneFunc(func(_ int, _ string) {
		a.pages.RemovePage("backupRunDetails")
	})
	modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
	a.pages.AddPage("backupRunDetails", modal, true, true)
}

func (a *App) confirmDeleteBackupJob(job backupcore.Job) {
	modal := tview.NewModal().SetText(fmt.Sprintf("%s Delete backup job [yellow]%s[-]?\n\nHistory and backup files stay untouched.", iconWarn, tview.Escape(job.Name))).
		AddButtons([]string{" Delete job ", " Cancel "}).SetDoneFunc(func(index int, _ string) {
		a.pages.RemovePage("deleteBackupJob")
		if index == 0 {
			if err := a.backupStore.DeleteJob(context.Background(), job.ID); err != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not delete backup job:\n\n%v", iconWarn, err), pageBackupCenter)
				return
			}
			if a.backupCenterSelectedJob == job.ID {
				a.backupCenterSelectedJob = ""
			}
		}
		a.showBackupCenter()
	})
	modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
	a.pages.AddPage("deleteBackupJob", modal, true, true)
}

func (a *App) confirmPruneBackupJob(job backupcore.Job) {
	retention := fmt.Sprintf("keep latest %d", job.Retention.KeepLast)
	if job.Retention.MaxAgeDays > 0 {
		retention += fmt.Sprintf(", max age %d days", job.Retention.MaxAgeDays)
	}
	if job.Retention.MaxTotalBytes > 0 {
		retention += fmt.Sprintf(", max stored %s", backupByteSize(uint64(job.Retention.MaxTotalBytes)))
	}
	modal := tview.NewModal().SetText(fmt.Sprintf(
		"%s Apply retention to [yellow]%s[-] now?\n\nPolicy: %s\n\nThe newest successful artifact is always retained. Only successful artifacts recorded for this job are eligible, and dbterm verifies each file before deleting it. Files removed by retention cannot be recovered through dbterm.",
		iconWarn, tview.Escape(job.Name), tview.Escape(retention),
	)).AddButtons([]string{" Apply retention ", " Cancel "}).SetDoneFunc(func(index int, _ string) {
		a.pages.RemovePage("pruneBackupJob")
		if index != 0 {
			a.pages.ShowPage(pageBackupCenter)
			return
		}
		a.pruneBackupJobNow(job)
	})
	modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
	a.pages.AddPage("pruneBackupJob", modal, true, true)
}

func (a *App) pruneBackupJobNow(job backupcore.Job) {
	ctx, cancel := context.WithCancel(context.Background())
	token := a.showLoadingModal("Checking recorded artifacts and applying retention...", withLoadingCancelOutcome("Press Esc to stop before the next file; files already pruned stay deleted.", cancel))
	go func() {
		removed, err := backupcore.ApplyRetention(ctx, a.backupStore, job, time.Now())
		cancel()
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				return
			}
			if err != nil {
				detail := ""
				if len(removed) > 0 {
					detail = fmt.Sprintf("\n\n%d artifact(s) were safely removed before the error.", len(removed))
				}
				a.ShowAlert(fmt.Sprintf("%s Retention stopped:\n\n%v%s", iconFail, err, detail), pageBackupCenter)
				return
			}
			if len(removed) == 0 {
				a.ShowAlert(fmt.Sprintf("%s Retention is already satisfied. No recorded artifact was removed; the newest successful backup remains protected.", iconInfo), pageBackupCenter)
				return
			}
			a.ShowAlert(fmt.Sprintf("%s Retention complete\n\nRemoved %d recorded artifact(s):\n%s\n\nThe newest successful backup was retained.", iconSuccess, len(removed), backupPrunedPathSummary(removed, 8)), pageBackupCenter)
		})
	}()
}

func backupPrunedPathSummary(paths []string, limit int) string {
	if limit < 1 {
		limit = 1
	}
	lines := make([]string, 0, min(len(paths), limit)+1)
	for index, path := range paths {
		if index >= limit {
			lines = append(lines, fmt.Sprintf("…and %d more", len(paths)-limit))
			break
		}
		lines = append(lines, "• "+filepath.Base(path))
	}
	return strings.Join(lines, "\n")
}

func (a *App) showBackupInspectForm() {
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" Inspect / Restore Backup ").SetTitleColor(mauve).SetBorderColor(surface1)
	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle).SetFieldTextColor(text).SetLabelColor(text).
		SetButtonBackgroundColor(surface1).SetButtonTextColor(green)
	form.AddInputField("Backup File", "", 80, nil, nil)
	form.AddInputField("age Identity (optional)", "", 80, nil, nil)
	defaultDecodedGiB := (backupcore.DefaultMaxDecodedBytes + backupDecodedGiB - 1) / backupDecodedGiB
	if defaultDecodedGiB < 1 {
		defaultDecodedGiB = 1
	}
	form.AddInputField("Max Decoded GiB", strconv.FormatInt(defaultDecodedGiB, 10), 8, func(text string, _ rune) bool { return digitsOnly(text) }, nil)
	form.AddButton("Inspect", func() {
		path := formInputValueByLabel(form, "Backup File")
		identity := formInputValueByLabel(form, "age Identity (optional)")
		if path == "" {
			a.ShowAlert(fmt.Sprintf("%s Select a backup file to inspect.", iconInfo), "backupInspect")
			return
		}
		maxDecodedBytes, limitErr := parseBackupDecodedGiB(formInputValueByLabel(form, "Max Decoded GiB"))
		if limitErr != nil {
			a.ShowAlert(fmt.Sprintf("%s Invalid decoded-size limit:\n\n%v", iconWarn, limitErr), "backupInspect")
			return
		}
		a.pages.RemovePage("backupInspect")
		a.inspectBackupAsync(path, identity, maxDecodedBytes)
	})
	form.AddButton("Cancel", func() {
		a.pages.RemovePage("backupInspect")
		a.pages.ShowPage(pageBackupCenter)
	})
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.pages.RemovePage("backupInspect")
			a.pages.ShowPage(pageBackupCenter)
			return nil
		}
		return event
	})
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(" Content—not the filename—decides the engine  │  Decode limit applies to each gzip, zstd, ZIP, or age layer ")
	container := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(form, 0, 1, true).AddItem(footer, 1, 0, false)
	w, h := a.modalSize(74, 116, 12, 16)
	grid := tview.NewGrid().SetColumns(0, w, 0).SetRows(0, h, 0).AddItem(container, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage("backupInspect", grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) showBackupAgeKeyGenerator() {
	configDir, err := appdirs.ConfigDir()
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not resolve the key directory:\n\n%v", iconWarn, err), pageBackupCenter)
		return
	}
	defaultPath := filepath.Join(configDir, "backup", "age-identity.txt")
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" Generate age X25519 Identity ").SetTitleColor(mauve).SetBorderColor(surface1)
	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle).SetFieldTextColor(text).SetLabelColor(text).
		SetButtonBackgroundColor(surface1).SetButtonTextColor(green)
	form.AddInputField("Private Identity File", defaultPath, 80, nil, nil)
	form.AddButton("Generate", func() {
		path := formInputValueByLabel(form, "Private Identity File")
		if path == "" {
			a.ShowAlert(fmt.Sprintf("%s Identity file path is required.", iconInfo), "backupKeygen")
			return
		}
		absolute, pathErr := filepath.Abs(filepath.Clean(path))
		if pathErr != nil {
			a.ShowAlert(fmt.Sprintf("%s Invalid identity path:\n\n%v", iconWarn, pathErr), "backupKeygen")
			return
		}
		recipient, generateErr := backupcore.GenerateAgeIdentity(absolute)
		if generateErr != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not generate identity:\n\n%v", iconWarn, generateErr), "backupKeygen")
			return
		}
		a.pages.RemovePage("backupKeygen")
		a.showGeneratedAgeRecipient(absolute, recipient)
	})
	form.AddButton("Cancel", func() {
		a.pages.RemovePage("backupKeygen")
		a.pages.ShowPage(pageBackupCenter)
	})
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.pages.RemovePage("backupKeygen")
			a.pages.ShowPage(pageBackupCenter)
			return nil
		}
		return event
	})
	w, h := a.modalSize(74, 116, 9, 13)
	grid := tview.NewGrid().SetColumns(0, w, 0).SetRows(0, h, 0).AddItem(form, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage("backupKeygen", grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) showGeneratedAgeRecipient(path, recipient string) {
	modal := tview.NewModal().SetText(fmt.Sprintf("%s age identity created\n\nPrivate identity file:\n%s\n\nPublic recipient for scheduled jobs:\n%s\n\nKeep the private identity separately from off-site backup files.", iconSuccess, tview.Escape(path), recipient)).
		AddButtons([]string{" Copy recipient ", " Close "}).SetDoneFunc(func(index int, _ string) {
		a.pages.RemovePage("backupKeygenResult")
		if index == 0 {
			a.copyValueAsync(recipient, func(copyErr error) {
				if copyErr != nil {
					a.ShowAlert(fmt.Sprintf("%s Recipient is kept in dbterm's internal clipboard, but the system clipboard was unavailable:\n\n%v", iconInfo, copyErr), pageBackupCenter)
					return
				}
				a.ShowAlert(fmt.Sprintf("%s Public age recipient copied.", iconSuccess), pageBackupCenter)
			})
			return
		}
		a.pages.ShowPage(pageBackupCenter)
	})
	modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
	a.pages.AddPage("backupKeygenResult", modal, true, true)
}

func (a *App) inspectBackupAsync(path, identity string, maxDecodedBytes int64) {
	ctx, cancel := context.WithCancel(context.Background())
	var canceled atomic.Bool
	token := a.showLoadingModal("Inspecting backup contents and checksum...", withLoadingCancelOutcome("Press Esc to cancel inspection.", func() {
		canceled.Store(true)
		cancel()
	}))
	go func() {
		inspection, err := backupcore.Inspect(ctx, path, backupcore.InspectOptions{
			AgeIdentityPath: identity,
			MaxDecodedBytes: maxDecodedBytes,
		})
		cancel()
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				return
			}
			if canceled.Load() {
				a.ShowAlert(fmt.Sprintf("%s Backup inspection canceled.", iconWarn), pageBackupCenter)
				return
			}
			if err != nil {
				a.ShowAlert(fmt.Sprintf("%s Backup inspection failed:\n\n%v", iconFail, err), pageBackupCenter)
				return
			}
			a.showBackupInspectionResult(inspection, identity, maxDecodedBytes)
		})
	}()
}

func (a *App) showBackupInspectionResult(inspection *backupcore.Inspection, identity string, maxDecodedBytes int64) {
	if inspection == nil {
		return
	}
	wrappers := "none"
	if len(inspection.Wrappers) > 0 {
		parts := make([]string, len(inspection.Wrappers))
		for index, wrapper := range inspection.Wrappers {
			parts[index] = string(wrapper)
		}
		wrappers = strings.Join(parts, " → ")
	}
	engine := string(inspection.Engine)
	if engine == "" {
		engine = "not determined"
	}
	warnings := "none"
	if len(inspection.Warnings) > 0 {
		warnings = strings.Join(inspection.Warnings, "\n • ")
		warnings = "• " + warnings
	}
	restoreTools := "built into dbterm"
	if inspection.Locked {
		restoreTools = "shown after the age backup is unlocked"
	} else if len(inspection.RequiredTools) > 0 {
		restoreTools = strings.Join(inspection.RequiredTools, ", ")
	}
	textValue := fmt.Sprintf(
		"[::b]%s[-]\n\nType: [green]%s[-]  │  Engine: [green]%s[-]  │  Confidence: %s\nWrappers: %s\nRestore tool: %s\nSize: %d bytes  │  Decode cap: %s per layer\nSHA-256: %s\n\nWarnings:\n%s",
		tview.Escape(inspection.Path), inspection.Format, engine, inspection.Confidence, wrappers, restoreTools, inspection.Size, formatBackupDecodedLimit(maxDecodedBytes), inspection.SHA256, tview.Escape(warnings),
	)
	buttons := []string{" Close "}
	if !inspection.Locked && inspection.Engine != "" && inspection.Format != backupcore.FormatUnknown && inspection.Format != backupcore.FormatGenericSQL {
		buttons = []string{" Restore… ", " Close "}
	}
	modal := tview.NewModal().SetText(textValue).AddButtons(buttons).SetDoneFunc(func(index int, _ string) {
		a.pages.RemovePage("backupInspectionResult")
		if len(buttons) == 2 && index == 0 {
			a.showRestoreTargetForm(inspection, identity, maxDecodedBytes)
			return
		}
		a.pages.ShowPage(pageBackupCenter)
	})
	modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
	a.pages.AddPage("backupInspectionResult", modal, true, true)
}

func (a *App) showRestoreTargetForm(inspection *backupcore.Inspection, identity string, maxDecodedBytes int64) {
	var matching []config.ConnectionConfig
	for _, connection := range a.store.Connections {
		if connection.Type == inspection.Engine {
			matching = append(matching, connection)
		}
	}
	if len(matching) == 0 {
		a.ShowAlert(fmt.Sprintf("%s No saved %s connection can receive this backup.\n\nAdd the restore target on Dashboard, then inspect the file again.", iconInfo, inspection.Engine), pageBackupCenter)
		return
	}
	labels := make([]string, len(matching))
	for index, connection := range matching {
		labels[index] = fmt.Sprintf("%s  —  %s", connection.Name, restoreTargetLabel(connection))
	}
	selectedTarget := 0
	selectedMode := defaultBackupRestoreMode(inspection.Format)
	modeOptions := []string{"Merge (keep existing objects)", "Clean / replace (destructive)"}
	if selectedMode == 1 {
		modeOptions[1] = "Replace SQLite file (pre-restore snapshot kept)"
	}
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" Restore Destination ").SetTitleColor(mauve).SetBorderColor(surface1)
	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle).SetFieldTextColor(text).SetLabelColor(text).
		SetButtonBackgroundColor(surface1).SetButtonTextColor(green)
	form.AddDropDown("Target", labels, selectedTarget, func(_ string, index int) { selectedTarget = index })
	form.AddDropDown("Mode", modeOptions, selectedMode, func(_ string, index int) { selectedMode = index })
	form.AddCheckbox("Stop on first error", true, nil)
	form.AddCheckbox("Single transaction", inspection.Engine == config.PostgreSQL, nil)
	form.AddInputField("Confirm Clean Target", "", 48, nil, nil)
	form.AddButton("Review Restore", func() {
		if selectedTarget < 0 || selectedTarget >= len(matching) {
			a.ShowAlert(fmt.Sprintf("%s Select a restore target.", iconInfo), "restoreTarget")
			return
		}
		target := matching[selectedTarget]
		if target.ReadOnly {
			a.ShowAlert(fmt.Sprintf("%s Target %q is saved as read-only. Edit the connection before restoring.", iconWarn, target.Name), "restoreTarget")
			return
		}
		mode := backupcore.RestoreModeMerge
		if selectedMode == 1 {
			mode = backupcore.RestoreModeClean
			expected := restoreConfirmationValue(target)
			if formInputValueByLabel(form, "Confirm Clean Target") != expected {
				a.ShowAlert(fmt.Sprintf("%s Clean restore can remove or replace data.\n\nType exactly: %s", iconWarn, expected), "restoreTarget")
				return
			}
		}
		options := backupcore.RestoreOptions{
			Mode: mode, StopOnError: checkboxValue(form, "Stop on first error"),
			SingleTransaction: checkboxValue(form, "Single transaction"), AgeIdentityPath: identity,
			MaxDecodedBytes: maxDecodedBytes,
		}
		plan, err := backupcore.BuildRestorePlan(inspection, &target, options)
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s Restore cannot start:\n\n%v", iconWarn, err), "restoreTarget")
			return
		}
		a.pages.RemovePage("restoreTarget")
		a.showRestoreConfirmation(plan)
	})
	form.AddButton("Cancel", func() {
		a.pages.RemovePage("restoreTarget")
		a.pages.ShowPage(pageBackupCenter)
	})
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			a.pages.RemovePage("restoreTarget")
			a.pages.ShowPage(pageBackupCenter)
			return nil
		}
		return event
	})
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(fmt.Sprintf(" Detected [green]%s[-] with %s confidence  │  Clean mode requires typing the target  │  [yellow]Esc[-] Cancel ", inspection.Format, inspection.Confidence))
	container := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(form, 0, 1, true).AddItem(footer, 1, 0, false)
	w, h := a.modalSize(78, 116, 15, 20)
	grid := tview.NewGrid().SetColumns(0, w, 0).SetRows(0, h, 0).AddItem(container, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage("restoreTarget", grid, true, true)
	a.app.SetFocus(form)
}

func defaultBackupRestoreMode(format backupcore.Format) int {
	if format == backupcore.FormatSQLiteDatabase {
		return 1
	}
	return 0
}

func (a *App) showRestoreConfirmation(plan *backupcore.RestorePlan) {
	warnings := append([]string{}, plan.Inspection.Warnings...)
	warnings = append(warnings, plan.Warnings...)
	warningText := "No additional warnings."
	if len(warnings) > 0 {
		warningText = "• " + strings.Join(warnings, "\n• ")
	}
	transaction := "off"
	if plan.Options.SingleTransaction {
		transaction = "on"
	}
	message := fmt.Sprintf(
		"%s Final restore review\n\nSource: %s\nDetected: %s (%s)\nTarget: %s\nMode: %s  │  Stop on error: %t  │  Transaction: %s\nDecode cap: %s per wrapper layer\n\n%s\n\nSQL/archive restores can execute code contained in the backup. Continue only if you trust its source.",
		iconWarn, tview.Escape(plan.Inspection.Path), plan.Inspection.Format, plan.Inspection.Confidence,
		tview.Escape(restoreTargetLabel(plan.Target)), plan.Options.Mode, plan.Options.StopOnError, transaction,
		formatBackupDecodedLimit(plan.Options.MaxDecodedBytes), tview.Escape(warningText),
	)
	modal := tview.NewModal().SetText(message).AddButtons([]string{" Restore now ", " Cancel "}).SetDoneFunc(func(index int, _ string) {
		a.pages.RemovePage("restoreConfirm")
		if index == 0 {
			a.runRestoreAsync(plan)
			return
		}
		a.pages.ShowPage(pageBackupCenter)
	})
	modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
	a.pages.AddPage("restoreConfirm", modal, true, true)
}

func (a *App) runRestoreAsync(plan *backupcore.RestorePlan) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Minute)
	var canceled atomic.Bool
	loadingTitle := fmt.Sprintf("Restoring %s into %s...", plan.Inspection.Format, plan.Target.Name)
	cancelText := "Press Esc to request cancellation."
	if plan.Target.Type == config.MySQL {
		cancelText += " MySQL may retain statements already committed."
	}
	token := a.showLoadingModal(loadingTitle, withLoadingCancelOutcome(cancelText, func() {
		canceled.Store(true)
		cancel()
	}))
	reconnect := a.detachActiveSQLiteRestoreTarget(plan.Target)
	go func() {
		var progress []string
		err := backupcore.ExecuteRestore(ctx, plan, func(line string) {
			if len(progress) >= 12 {
				progress = progress[1:]
			}
			progress = append(progress, line)
			a.updateBackupLoadingProgress(token, loadingTitle, line, cancelText)
		})
		cancel()
		var reconnectDBErr error
		var reconnectedDB *sql.DB
		if reconnect != nil {
			reconnectedDB, reconnectDBErr = database.Connect(reconnect)
		}
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				if reconnectedDB != nil {
					_ = reconnectedDB.Close()
				}
				return
			}
			if reconnect != nil && reconnectDBErr == nil {
				a.db = reconnectedDB
				a.dbType = reconnect.Type
				a.dbName = reconnect.Name
				a.activeConn = cloneConnectionConfig(reconnect)
			}
			if canceled.Load() {
				note := "The restore was canceled."
				if plan.Target.Type == config.MySQL {
					note += " MySQL may have applied earlier statements; inspect the target before retrying."
				}
				a.ShowAlert(fmt.Sprintf("%s %s", iconWarn, note), pageBackupCenter)
				return
			}
			if err != nil {
				detail := ""
				if len(progress) > 0 {
					detail = "\n\nLast phase: " + progress[len(progress)-1]
				}
				a.ShowAlert(fmt.Sprintf("%s Restore failed:\n\n%v%s", iconFail, err, detail), pageBackupCenter)
				return
			}
			reconnectNote := ""
			if reconnect != nil {
				if reconnectDBErr != nil {
					reconnectNote = fmt.Sprintf("\nWorkspace reconnect failed: %v", reconnectDBErr)
				} else {
					reconnectNote = "\nSQLite workspace: reconnected"
				}
			}
			a.ShowAlert(fmt.Sprintf("%s Restore complete\n\nTarget: %s\nMode: %s%s\n\nPress Ctrl+F5 in the workspace to refresh metadata.", iconSuccess, restoreTargetLabel(plan.Target), plan.Options.Mode, reconnectNote), pageBackupCenter)
		})
	}()
}

func (a *App) detachActiveSQLiteRestoreTarget(target config.ConnectionConfig) *config.ConnectionConfig {
	if target.Type != config.SQLite || a.db == nil || a.activeConn == nil {
		return nil
	}
	activePath, _ := filepath.Abs(filepath.Clean(a.activeConn.FilePath))
	targetPath, _ := filepath.Abs(filepath.Clean(target.FilePath))
	if activePath == "" || activePath != targetPath {
		return nil
	}
	reconnect := cloneConnectionConfig(a.activeConn)
	_ = a.db.Close()
	a.db = nil
	a.activeConn = nil
	return reconnect
}

func restoreTargetLabel(target config.ConnectionConfig) string {
	if target.Type == config.SQLite {
		return target.FilePath
	}
	return fmt.Sprintf("%s@%s:%s/%s", nonEmptyOr(target.User, "user"), nonEmptyOr(target.Host, "localhost"), defaultPortFor(&target), target.Database)
}

func restoreConfirmationValue(target config.ConnectionConfig) string {
	if target.Type == config.SQLite {
		return filepath.Base(target.FilePath)
	}
	return target.Database
}

func (a *App) showBackupAgentManager() {
	userManager, userManagerErr := backupAgentServiceManagerForScope(osservice.ScopeUser)
	systemManager, systemManagerErr := backupAgentServiceManagerForScope(osservice.ScopeSystem)
	if userManagerErr != nil && systemManagerErr != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not initialize native agent managers:\n\nDesktop/user: %v\nServer/system: %v", iconWarn, userManagerErr, systemManagerErr), pageBackupCenter)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	token := a.showLoadingModal(fmt.Sprintf("%s Checking desktop and server backup agents...", iconBackup), withLoadingCancelOutcome("Press Esc to stop waiting.", cancel))
	go func() {
		user := backupAgentScopeState{scope: osservice.ScopeUser, manager: userManager, err: userManagerErr}
		system := backupAgentScopeState{scope: osservice.ScopeSystem, manager: systemManager, err: systemManagerErr}
		if user.manager != nil {
			user.status, user.err = user.manager.Status(ctx)
		}
		if system.manager != nil {
			system.status, system.err = system.manager.Status(ctx)
		}
		processRunning, processErr := backupcore.AgentProcessRunning()
		heartbeat, heartbeatErr := backupcore.AgentHealth(ctx, a.backupStore, time.Now())
		var metrics processinfo.Metrics
		var metricsErr error
		if heartbeat.PID > 0 {
			metrics, metricsErr = processinfo.Read(heartbeat.PID)
		}
		cancel()
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				return
			}
			if heartbeatErr != nil {
				heartbeat = backupcore.AgentStatus{}
			}
			a.showBackupAgentOverview(user, system, processRunning, processErr, heartbeat, metrics, metricsErr)
		})
	}()
}

type backupAgentScopeState struct {
	scope   osservice.Scope
	manager osservice.Manager
	status  osservice.Status
	err     error
}

type backupAgentStatusView struct {
	registration string
	process      string
	note         string
	foreground   bool
}

func describeBackupAgentStatus(status osservice.Status, processRunning bool, heartbeat backupcore.AgentStatus) backupAgentStatusView {
	view := backupAgentStatusView{registration: "not installed", process: "not running"}
	if status.Installed {
		view.registration = "installed, stopped"
	}
	if status.Running {
		view.registration = "installed, manager reports running"
	}
	if processRunning {
		view.process = "scheduler process active (heartbeat pending)"
		if heartbeat.Healthy {
			view.process = fmt.Sprintf("scheduler process active (pid %d, heartbeat %s)", heartbeat.PID, heartbeat.Heartbeat.Local().Format("15:04:05"))
		}
		if !status.Running {
			view.foreground = true
			view.note = "An unmanaged/foreground agent owns the scheduler lock. Stop it with Ctrl+C in the terminal that launched it, then reopen this dialog before changing the native service."
		}
	} else if status.Running {
		view.process = "not active yet; the native manager may still be starting it"
		view.note = "If the process stays inactive, inspect the agent logs and use Restart."
	} else if heartbeat.Healthy {
		view.note = "A recent heartbeat exists, but no live process owns the scheduler lock; the heartbeat is stale runtime metadata."
	}
	return view
}

func (a *App) showBackupAgentOverview(user, system backupAgentScopeState, processRunning bool, processErr error, heartbeat backupcore.AgentStatus, metrics processinfo.Metrics, metricsErr error) {
	process := backupAgentProcessSummary(processRunning, processErr, heartbeat, metrics, metricsErr)
	modal := tview.NewModal().SetText(fmt.Sprintf("%s Backup Agent\n\nDESKTOP / USER\n%s\n\nSERVER / SYSTEM\n%s\n\nLIVE SCHEDULER\n%s\n\nOnly one agent can own the scheduler lock. System scope can run before login; changing it requires Administrator/root elevation.",
		iconBackup, tview.Escape(backupAgentScopeSummary(user)), tview.Escape(backupAgentScopeSummary(system)), tview.Escape(process))).
		AddButtons([]string{" Desktop / user ", " Server / system ", " View logs ", " Close "}).SetDoneFunc(func(index int, _ string) {
		a.pages.RemovePage("backupAgentManager")
		switch index {
		case 0:
			a.showBackupAgentScopeActions(user, system, processRunning, heartbeat)
		case 1:
			a.showBackupAgentScopeActions(system, user, processRunning, heartbeat)
		case 2:
			a.showBackupAgentLogs()
		default:
			a.pages.ShowPage(pageBackupCenter)
		}
	})
	modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
	a.pages.AddPage("backupAgentManager", modal, true, true)
}

func backupAgentScopeSummary(state backupAgentScopeState) string {
	if state.err != nil {
		return fmt.Sprintf("Status unavailable: %v", state.err)
	}
	status := state.status
	registration := "not installed"
	if status.Installed {
		registration = "installed"
	}
	runtimeState := "stopped"
	if status.Running {
		runtimeState = "manager reports running"
	}
	startup := "off"
	if status.StartupEnabled {
		startup = "on"
	}
	scope := string(status.Scope)
	if scope == "" {
		scope = string(state.scope)
	}
	return fmt.Sprintf("Manager: %s  •  Scope: %s\nRegistration: %s  •  Runtime: %s  •  Startup: %s\nDefinition: %s\nDetail: %s",
		nonEmptyOr(status.Manager, "native manager"), scope, registration, runtimeState, startup,
		nonEmptyOr(status.Name, "not created"), nonEmptyOr(status.Detail, "no additional detail"))
}

func backupAgentProcessSummary(running bool, processErr error, heartbeat backupcore.AgentStatus, metrics processinfo.Metrics, metricsErr error) string {
	if processErr != nil {
		return "Scheduler lock could not be inspected: " + processErr.Error()
	}
	if !running {
		if heartbeat.Healthy {
			return fmt.Sprintf("Not running. Last heartbeat metadata for PID %d is stale.", heartbeat.PID)
		}
		return "Not running. Enabled jobs will not run after dbterm exits."
	}
	if !heartbeat.Healthy {
		return "Scheduler lock is active; waiting for a healthy heartbeat."
	}
	parts := []string{fmt.Sprintf("Active • PID %d • heartbeat %s", heartbeat.PID, heartbeat.Heartbeat.Local().Format("15:04:05"))}
	if metricsErr != nil {
		parts = append(parts, "process metrics unavailable: "+metricsErr.Error())
	} else if metrics.Alive {
		parts = append(parts, fmt.Sprintf("Process: %s • uptime %s • RAM %s", nonEmptyOr(metrics.Name, "dbterm"), formatBackupProgressDuration(metrics.Uptime), backupByteSize(metrics.RSSBytes)))
	}
	return strings.Join(parts, "\n")
}

func (a *App) showBackupAgentScopeActions(selected, other backupAgentScopeState, processRunning bool, heartbeat backupcore.AgentStatus) {
	if selected.manager == nil || selected.err != nil {
		a.ShowAlert(fmt.Sprintf("%s %s agent status is unavailable:\n\n%v", iconWarn, backupAgentScopeLabel(selected.scope), selected.err), pageBackupCenter)
		return
	}
	status := selected.status
	view := describeBackupAgentStatus(status, processRunning && !other.status.Running, heartbeat)
	blockedByOther := processRunning && other.status.Running && !status.Running
	buttons := []string{" Install & start ", " Logs ", " Back "}
	actions := []string{"install", "logs", "back"}
	if view.foreground || blockedByOther {
		buttons = []string{" Logs ", " Back "}
		actions = []string{"logs", "back"}
	} else if status.Installed && status.Running {
		startupAction, startupButton := "enable", " Enable startup "
		if status.StartupEnabled {
			startupAction, startupButton = "disable", " Disable startup "
		}
		buttons = []string{" Restart ", " Stop ", startupButton, " Reinstall ", " Uninstall ", " Logs ", " Back "}
		actions = []string{"restart", "stop", startupAction, "install", "uninstall", "logs", "back"}
	} else if status.Installed {
		startupAction, startupButton := "enable", " Enable startup "
		if status.StartupEnabled {
			startupAction, startupButton = "disable", " Disable startup "
		}
		buttons = []string{" Start ", startupButton, " Reinstall ", " Uninstall ", " Logs ", " Back "}
		actions = []string{"start", startupAction, "install", "uninstall", "logs", "back"}
	}
	note := backupAgentPlatformNote(selected.scope)
	if blockedByOther {
		note = fmt.Sprintf("The %s registration currently owns the scheduler. Stop it before starting this scope.", backupAgentScopeLabel(other.scope))
	}
	if selected.scope == osservice.ScopeSystem {
		note += " System changes are never auto-elevated; dbterm will show a copyable command if Administrator/root permission is required."
	}
	modal := tview.NewModal().SetText(fmt.Sprintf("%s %s Backup Agent\n\nManager: %s\nRegistration: %s\nStartup: %t\nLive process: %s\nDetail: %s\n\n%s",
		iconBackup, backupAgentScopeLabel(selected.scope), tview.Escape(status.Manager), tview.Escape(view.registration), status.StartupEnabled,
		tview.Escape(view.process), tview.Escape(nonEmptyOr(status.Detail, "no additional detail")), tview.Escape(note))).
		AddButtons(buttons).SetDoneFunc(func(index int, _ string) {
		a.pages.RemovePage("backupAgentScopeManager")
		if index < 0 || index >= len(actions) || actions[index] == "back" {
			a.showBackupAgentManager()
			return
		}
		if actions[index] == "logs" {
			a.showBackupAgentLogs()
			return
		}
		a.manageBackupAgentAsync(selected.manager, actions[index], selected.scope)
	})
	modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
	a.pages.AddPage("backupAgentScopeManager", modal, true, true)
}

func backupAgentScopeLabel(scope osservice.Scope) string {
	if scope == osservice.ScopeSystem {
		return "Server / system"
	}
	return "Desktop / user"
}

func (a *App) showBackupAgentLogs() {
	logDir, err := appdirs.LogDir()
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not resolve the backup agent log directory:\n\n%v", iconWarn, err), pageBackupCenter)
		return
	}
	const perFileTail = int64(64 * 1024)
	content, paths, err := loadBackupAgentLogTail(logDir, perFileTail)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not read backup agent logs:\n\n%v", iconWarn, err), pageBackupCenter)
		return
	}

	logView := tview.NewTextView().SetWrap(false).SetScrollable(true).SetText(content)
	logView.SetBorder(true).SetTitle(" Backup Agent Logs (bounded tail) ").SetTitleColor(mauve).SetBorderColor(surface1).SetBackgroundColor(bg)
	logView.SetTextColor(text)
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(" [yellow]↑/↓ PgUp/PgDn[-] Scroll  │  [yellow]C[-] Copy visible tail  │  [yellow]P[-] Copy paths  │  [yellow]R[-] Refresh  │  [yellow]Esc[-] Agent status ")
	closeLogs := func() {
		a.pages.RemovePage("backupAgentLogs")
		a.showBackupAgentManager()
	}
	logView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyBackspace || event.Key() == tcell.KeyBackspace2 {
			closeLogs()
			return nil
		}
		if event.Key() == tcell.KeyRune && event.Modifiers()&(tcell.ModCtrl|tcell.ModAlt|tcell.ModMeta) == 0 {
			switch event.Rune() {
			case 'c', 'C':
				a.copyValueAsync(content, func(copyErr error) {
					if copyErr != nil {
						a.ShowAlert(fmt.Sprintf("%s Log tail is in dbterm's internal clipboard; system clipboard unavailable:\n\n%v", iconInfo, copyErr), "backupAgentLogs")
						return
					}
					a.ShowAlert(fmt.Sprintf("%s Bounded log tail copied.", iconSuccess), "backupAgentLogs")
				})
				return nil
			case 'p', 'P':
				a.copyValueAsync(strings.Join(paths, "\n"), func(copyErr error) {
					if copyErr != nil {
						a.ShowAlert(fmt.Sprintf("%s Log paths are in dbterm's internal clipboard; system clipboard unavailable:\n\n%v", iconInfo, copyErr), "backupAgentLogs")
						return
					}
					a.ShowAlert(fmt.Sprintf("%s Log paths copied.", iconSuccess), "backupAgentLogs")
				})
				return nil
			case 'r', 'R':
				a.pages.RemovePage("backupAgentLogs")
				a.showBackupAgentLogs()
				return nil
			}
		}
		return event
	})
	layout := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(logView, 0, 1, true).AddItem(footer, 1, 0, false)
	a.pages.AddAndSwitchToPage("backupAgentLogs", layout, true)
	a.app.SetFocus(logView)
}

func loadBackupAgentLogTail(logDir string, perFileLimit int64) (string, []string, error) {
	if perFileLimit < 1 {
		return "", nil, fmt.Errorf("log tail limit must be positive")
	}
	names := []string{"dbterm-backup-agent.log.1", "dbterm-backup-agent.log", "dbterm-backup-error.log", "dbterm-backup.log"}
	var content strings.Builder
	paths := make([]string, 0, len(names))
	for _, name := range names {
		path := filepath.Join(logDir, name)
		payload, exists, err := readRegularFileTail(path, perFileLimit)
		if err != nil {
			return "", nil, err
		}
		if !exists {
			continue
		}
		paths = append(paths, path)
		fmt.Fprintf(&content, "\n===== %s =====\nPath: %s\n", name, path)
		if len(payload) == 0 {
			content.WriteString("(empty)\n")
		} else {
			content.Write(payload)
			if payload[len(payload)-1] != '\n' {
				content.WriteByte('\n')
			}
		}
	}
	if len(paths) == 0 {
		path := filepath.Join(logDir, "dbterm-backup-agent.log")
		paths = append(paths, path)
		fmt.Fprintf(&content, "No backup agent logs exist yet.\n\nExpected rolling log:\n%s\n\nInstall/start the agent or run a job, then press R to refresh.", path)
	}
	return strings.TrimPrefix(content.String(), "\n"), paths, nil
}

func readRegularFileTail(path string, limit int64) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("inspect log %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("refusing non-regular log path: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, fmt.Errorf("open log %s: %w", path, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("inspect open log %s: %w", path, err)
	}
	if !os.SameFile(info, opened) {
		return nil, false, fmt.Errorf("log changed while opening: %s", path)
	}
	start := opened.Size() - limit
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, false, fmt.Errorf("seek log %s: %w", path, err)
	}
	payload, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil, false, fmt.Errorf("read log %s: %w", path, err)
	}
	if start > 0 {
		if newline := strings.IndexByte(string(payload), '\n'); newline >= 0 {
			payload = payload[newline+1:]
		}
	}
	return payload, true, nil
}

func (a *App) manageBackupAgentAsync(manager osservice.Manager, action string, scope osservice.Scope) {
	ctx, cancel := context.WithTimeout(context.Background(), backupAgentActionTimeout)
	loadingTitle := fmt.Sprintf("%s Backup agent: %s...", iconBackup, action)
	const cancelText = "Press Esc to stop waiting; the OS operation may already have completed."
	token := a.showLoadingModal(loadingTitle, withLoadingCancelOutcome(cancelText, cancel))
	go func() {
		err := executeBackupAgentAction(ctx, manager, action, backupcore.AgentProcessRunning, func(progress string) {
			a.updateBackupLoadingProgress(token, loadingTitle, progress, cancelText)
		})
		cancel()
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				return
			}
			a.showBackupCenter()
			if err != nil {
				if scope == osservice.ScopeSystem && osservice.RequiresElevation(err) {
					a.showBackupSystemElevationHelp(action, err)
					return
				}
				a.ShowAlert(fmt.Sprintf("%s Backup agent %s failed:\n\n%v\n\nOpen Agent again to refresh its native registration and process state.", iconFail, action, err), pageBackupCenter)
				return
			}
			a.ShowAlert(fmt.Sprintf("%s %s\n\nJobs and backup files were not changed.", iconSuccess, backupAgentActionSuccess(action)), pageBackupCenter)
		})
	}()
}

type backupAgentProcessProbe func() (bool, error)

func executeBackupAgentAction(ctx context.Context, manager osservice.Manager, action string, probe backupAgentProcessProbe, emit func(string)) error {
	if manager == nil {
		return fmt.Errorf("native backup agent manager is unavailable")
	}
	if probe == nil {
		return fmt.Errorf("backup agent process probe is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	progress := func(message string) {
		if emit != nil {
			emit(message)
		}
	}

	progress("Checking native registration and scheduler process lock")
	status, err := manager.Status(ctx)
	if err != nil {
		return fmt.Errorf("query native backup agent before %s: %w", action, err)
	}
	processRunning, err := probe()
	if err != nil {
		return fmt.Errorf("check backup agent process before %s: %w", action, err)
	}
	if processRunning && !status.Running {
		return fmt.Errorf("cannot %s while an unmanaged/foreground backup agent owns the scheduler lock; stop `dbterm backup agent` with Ctrl+C, then retry", action)
	}

	waitStopped := func(stage string) error {
		progress(stage)
		if err := waitForBackupAgentProcessState(ctx, false, probe); err != nil {
			return fmt.Errorf("%s: %w", strings.ToLower(stage), err)
		}
		return nil
	}
	waitStarted := func(stage string) error {
		progress(stage)
		if err := waitForBackupAgentProcessState(ctx, true, probe); err != nil {
			return fmt.Errorf("%s: %w", strings.ToLower(stage), err)
		}
		return nil
	}

	switch action {
	case "install":
		// Reinstall is deliberately stop-then-install so completion cannot be
		// reported while the old process still owns the scheduler lock.
		if status.Running {
			progress("Stopping the existing native agent before reinstall")
			if err := manager.Stop(ctx); err != nil {
				return err
			}
			if err := waitStopped("Waiting for the previous scheduler process to exit"); err != nil {
				return err
			}
		}
		progress("Installing the native registration and starting the agent")
		if err := manager.Install(ctx); err != nil {
			return err
		}
		return waitStarted("Waiting for the scheduler process to acquire its lock")
	case "uninstall":
		progress("Stopping the agent and removing its native registration")
		if err := manager.Uninstall(ctx); err != nil {
			return err
		}
		return waitStopped("Waiting for the scheduler process to exit")
	case "start":
		progress("Starting the native backup agent")
		if err := manager.Start(ctx); err != nil {
			return err
		}
		return waitStarted("Waiting for the scheduler process to acquire its lock")
	case "stop":
		progress("Stopping the native backup agent")
		if err := manager.Stop(ctx); err != nil {
			return err
		}
		return waitStopped("Waiting for the scheduler process to exit")
	case "restart":
		progress("Stopping the native backup agent")
		if err := manager.Stop(ctx); err != nil {
			return err
		}
		if err := waitStopped("Waiting for the previous scheduler process to exit"); err != nil {
			return err
		}
		progress("Starting the native backup agent")
		if err := manager.Start(ctx); err != nil {
			return err
		}
		return waitStarted("Waiting for the restarted scheduler process to acquire its lock")
	case "enable":
		progress("Enabling automatic startup for the native backup agent")
		return osservice.SetStartupEnabled(ctx, manager, true)
	case "disable":
		progress("Disabling automatic startup; the current agent process is unchanged")
		return osservice.SetStartupEnabled(ctx, manager, false)
	default:
		return fmt.Errorf("unknown agent action %q", action)
	}
}

func waitForBackupAgentProcessState(ctx context.Context, wantRunning bool, probe backupAgentProcessProbe) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		running, err := probe()
		if err != nil {
			return fmt.Errorf("probe scheduler process lock: %w", err)
		}
		if running == wantRunning {
			return nil
		}
		timer := time.NewTimer(backupAgentProcessPoll)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if wantRunning {
				return fmt.Errorf("scheduler process did not start before the operation ended; inspect the native agent logs: %w", ctx.Err())
			}
			return fmt.Errorf("scheduler process still owns its lock; a native child task or foreground agent may still be running: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func backupAgentActionSuccess(action string) string {
	switch action {
	case "install":
		return "Backup agent installed and running."
	case "uninstall":
		return "Backup agent stopped and native registration removed."
	case "start":
		return "Backup agent is running."
	case "stop":
		return "Backup agent stopped; native registration was kept."
	case "restart":
		return "Backup agent restarted and is running."
	case "enable":
		return "Backup agent automatic startup enabled."
	case "disable":
		return "Backup agent automatic startup disabled; its current runtime was not changed."
	default:
		return "Backup agent operation completed."
	}
}

func (a *App) showBackupSystemElevationHelp(action string, operationErr error) {
	command, configDir, stateDir, logDir, err := backupSystemServiceCommand(action)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Server/system agent requires elevation:\n\n%v\n\nCould not build the retry command: %v", iconWarn, operationErr, err), pageBackupCenter)
		return
	}
	instruction := "Run this command from a root-capable terminal. dbterm will not invoke sudo automatically."
	if runtime.GOOS == "windows" {
		instruction = "Open a terminal with Run as Administrator, then run this command. dbterm will not request elevation automatically."
	}
	modal := tview.NewModal().SetText(fmt.Sprintf("%s Server / system permission required\n\n%s\n\nConfig: %s\nState: %s\nLogs: %s\n\nCommand:\n%s\n\nOriginal error: %v",
		iconWarn, tview.Escape(instruction), tview.Escape(configDir), tview.Escape(stateDir), tview.Escape(logDir), tview.Escape(command), operationErr)).
		AddButtons([]string{" Copy command ", " Close "}).SetDoneFunc(func(index int, _ string) {
		a.pages.RemovePage("backupSystemElevation")
		if index != 0 {
			a.pages.ShowPage(pageBackupCenter)
			return
		}
		a.copyValueAsync(command, func(copyErr error) {
			if copyErr != nil {
				a.ShowAlert(fmt.Sprintf("%s Command is in dbterm's internal clipboard; system clipboard unavailable:\n\n%v", iconInfo, copyErr), pageBackupCenter)
				return
			}
			a.ShowAlert(fmt.Sprintf("%s Elevated server/system command copied. Review it, then run it in the appropriate Administrator/root terminal.", iconSuccess), pageBackupCenter)
		})
	})
	modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
	a.pages.AddPage("backupSystemElevation", modal, true, true)
}

func backupSystemServiceCommand(action string) (command, configDir, stateDir, logDir string, err error) {
	executable, err := os.Executable()
	if err != nil {
		return "", "", "", "", fmt.Errorf("resolve dbterm executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return "", "", "", "", err
	}
	configDir, err = appdirs.ConfigDir()
	if err != nil {
		return "", "", "", "", err
	}
	stateDir, err = appdirs.StateDir()
	if err != nil {
		return "", "", "", "", err
	}
	logDir, err = appdirs.LogDir()
	if err != nil {
		return "", "", "", "", err
	}
	action = strings.ToLower(strings.TrimSpace(action))
	parts := []string{executable, "backup", "service", action, "--system"}
	if runtime.GOOS != "windows" && action == "install" {
		username := strings.TrimSpace(os.Getenv("SUDO_USER"))
		if username == "" || username == "root" {
			if current, currentErr := user.Current(); currentErr == nil {
				username = strings.TrimSpace(current.Username)
			}
		}
		if username == "" || username == "root" {
			username = "<non-root-user>"
		}
		parts = append(parts, "--run-as", username)
	}
	parts = append(parts, "--config-dir", configDir, "--state-dir", stateDir, "--log-dir", logDir)
	quoted := make([]string, len(parts))
	for index, part := range parts {
		if runtime.GOOS == "windows" {
			quoted[index] = windowsCommandQuote(part)
		} else {
			quoted[index] = unixShellQuote(part)
		}
	}
	command = strings.Join(quoted, " ")
	if runtime.GOOS != "windows" {
		command = "sudo " + command
	}
	return command, configDir, stateDir, logDir, nil
}

func unixShellQuote(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n'\"\\$`!&;|<>()[]{}*?") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func windowsCommandQuote(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n\"") {
		return value
	}
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func (a *App) updateBackupLoadingProgress(token uint64, title, progress, cancelText string) {
	if a == nil || a.app == nil || a.pages == nil || a.loadingGeneration.Load() != token {
		return
	}
	a.app.QueueUpdateDraw(func() {
		if a.loadingGeneration.Load() != token {
			return
		}
		modal, ok := a.pages.GetPage("loading").(*tview.Modal)
		if !ok || modal == nil {
			return
		}
		modal.SetText(fmt.Sprintf("\n%s %s\n\n%s\n\n%s", iconRefresh, tview.Escape(title), tview.Escape(progress), tview.Escape(cancelText)))
	})
}

func (a *App) updateBackupProgress(token uint64, title string, event backupcore.ProgressEvent, cancelText string) {
	if a == nil || a.app == nil || a.pages == nil || a.loadingGeneration.Load() != token {
		return
	}
	a.app.QueueUpdateDraw(func() {
		if a.loadingGeneration.Load() != token {
			return
		}
		modal, ok := a.pages.GetPage("loading").(*tview.Modal)
		if !ok || modal == nil {
			return
		}
		modal.SetText(fmt.Sprintf("\n%s\n\n%s\n\n%s", tview.Escape(title), renderBackupProgress(event, 34), tview.Escape(cancelText)))
	})
}

func renderBackupProgress(event backupcore.ProgressEvent, barWidth int) string {
	if barWidth < 12 {
		barWidth = 12
	}
	elapsed := event.Elapsed
	if elapsed < 0 {
		elapsed = 0
	}
	phase := strings.ToUpper(strings.TrimSpace(event.Phase))
	if phase == "" {
		phase = "WORKING"
	}
	message := strings.TrimSpace(event.Message)
	if message == "" {
		message = "backup in progress"
	}

	bar := make([]byte, barWidth)
	for index := range bar {
		bar[index] = '-'
	}
	percent := "streaming"
	if event.TotalBytes > 0 {
		current := event.CurrentBytes
		if current < 0 {
			current = 0
		}
		if current > event.TotalBytes {
			current = event.TotalBytes
		}
		fraction := float64(current) / float64(event.TotalBytes)
		filled := int(math.Round(fraction * float64(barWidth)))
		filled = max(0, min(filled, barWidth))
		for index := 0; index < filled; index++ {
			bar[index] = '#'
		}
		if filled < barWidth {
			bar[filled] = '>'
		}
		percent = fmt.Sprintf("%5.1f%%", fraction*100)
	} else {
		segmentWidth := min(7, max(3, barWidth/5))
		positions := max(1, barWidth-segmentWidth+1)
		position := int(elapsed/(250*time.Millisecond)) % positions
		for index := position; index < position+segmentWidth; index++ {
			bar[index] = '='
		}
	}

	statistics := []string{"elapsed " + formatBackupProgressDuration(elapsed)}
	if event.CurrentBytes > 0 {
		processed := backupByteSize(uint64(event.CurrentBytes))
		if event.TotalBytes > 0 {
			processed += " / " + backupByteSize(uint64(event.TotalBytes))
		}
		statistics = append(statistics, "data "+processed)
	}
	if elapsed >= time.Second && event.CurrentBytes > 0 {
		rate := float64(event.CurrentBytes) / elapsed.Seconds()
		if rate > 0 {
			statistics = append(statistics, "rate "+backupByteSize(uint64(rate))+"/s")
			if event.TotalBytes > event.CurrentBytes {
				eta := time.Duration(float64(event.TotalBytes-event.CurrentBytes)/rate) * time.Second
				statistics = append(statistics, "ETA "+formatBackupProgressDuration(eta))
			}
		}
	}

	return fmt.Sprintf("%s\n%s\n|%s|  %s\n%s", tview.Escape(phase), tview.Escape(message), string(bar), percent, strings.Join(statistics, "  •  "))
}

func formatBackupProgressDuration(duration time.Duration) string {
	if duration < time.Second {
		return "<1s"
	}
	duration = duration.Round(time.Second)
	if duration < time.Minute {
		return duration.String()
	}
	hours := int(duration / time.Hour)
	minutes := int(duration/time.Minute) % 60
	seconds := int(duration/time.Second) % 60
	if hours > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", hours, minutes, seconds)
	}
	return fmt.Sprintf("%dm%02ds", minutes, seconds)
}

func backupAgentServiceManager() (osservice.Manager, error) {
	return backupAgentServiceManagerForScope(osservice.ScopeUser)
}

func backupAgentServiceManagerForScope(scope osservice.Scope) (osservice.Manager, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve dbterm executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	logDir, err := appdirs.LogDir()
	if err != nil {
		return nil, err
	}
	configDir, err := appdirs.ConfigDir()
	if err != nil {
		return nil, err
	}
	stateDir, err := appdirs.StateDir()
	if err != nil {
		return nil, err
	}
	return osservice.New(osservice.Options{Executable: executable, ConfigDir: configDir, StateDir: stateDir, LogDir: logDir, Scope: scope})
}

func backupAgentPlatformNote(scope osservice.Scope) string {
	switch runtime.GOOS {
	case "linux":
		if scope == osservice.ScopeSystem {
			return "systemd system service; it can start at boot without an interactive login and runs as the configured non-root user."
		}
		return "systemd user service; it starts at login. For backups after logout, explicitly enable user lingering with loginctl."
	case "darwin":
		if scope == osservice.ScopeSystem {
			return "launchd LaunchDaemon; it can start at boot without a user login and runs as the configured non-root user."
		}
		return "launchd LaunchAgent; it starts for the logged-in macOS user and restarts on failure."
	case "windows":
		if scope == osservice.ScopeSystem {
			return "Task Scheduler system task; it can start at boot without a user login and requires an Administrator terminal to change."
		}
		return "Task Scheduler entry for the current user; it starts at logon and restarts on failure without Administrator access."
	default:
		return "current-user native service registration."
	}
}

func (a *App) offerBackupAgentStart() {
	manager, err := backupAgentServiceManager()
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	go func() {
		status, statusErr := manager.Status(ctx)
		processRunning, processErr := backupcore.AgentProcessRunning()
		cancel()
		if statusErr != nil || processErr != nil || status.Running || processRunning {
			return
		}
		a.app.QueueUpdateDraw(func() {
			frontPage, _ := a.pages.GetFrontPage()
			if frontPage != pageBackupCenter {
				return
			}
			action := "install"
			button := " Install & start "
			if status.Installed {
				action = "start"
				button = " Start agent "
			}
			modal := tview.NewModal().SetText(fmt.Sprintf("%s Schedule saved\n\nThe job is enabled, but no backup agent process is running. Start the native agent now so backups continue after dbterm closes?", iconBackup)).
				AddButtons([]string{button, " Later "}).SetDoneFunc(func(index int, _ string) {
				a.pages.RemovePage("offerBackupAgent")
				if index == 0 {
					a.manageBackupAgentAsync(manager, action, osservice.ScopeUser)
					return
				}
				a.pages.ShowPage(pageBackupCenter)
			})
			modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
			a.pages.AddPage("offerBackupAgent", modal, true, true)
		})
	}()
}

func backupScheduleLabel(schedule backupcore.Schedule) string {
	switch schedule.Kind {
	case backupcore.ScheduleInterval:
		return fmt.Sprintf("every %s", (time.Duration(schedule.EveryMinutes) * time.Minute).String())
	case backupcore.ScheduleDaily:
		return fmt.Sprintf("daily %s %s", schedule.TimeOfDay, nonEmptyOr(schedule.Timezone, "Local"))
	case backupcore.ScheduleWeekly:
		return fmt.Sprintf("weekly %s · %s %s", weekdayText(schedule.Weekdays), schedule.TimeOfDay, nonEmptyOr(schedule.Timezone, "Local"))
	default:
		return "manual"
	}
}

func backupCenterFooterText(width int) string {
	full := " [yellow]N[-] New backup  │  [yellow]Enter[-] Edit  │  [yellow]R[-] Run now  │  [yellow]Space[-] Schedule  │  [yellow]H[-] History  │  [yellow]I[-] Restore  │  [yellow]P[-] Prune  │  [yellow]A[-] Agent  │  [yellow]D[-] Delete  │  [yellow]Esc[-] Back "
	medium := " [yellow]N[-] New backup  │  [yellow]Enter[-] Edit  │  [yellow]R[-] Run  │  [yellow]Space[-] Schedule  │  [yellow]P[-] Prune  │  [yellow]A[-] Agent  │  [yellow]Esc[-] Back "
	short := " [yellow]N[-] New  │  [yellow]Enter[-] Edit  │  [yellow]R[-] Run  │  [yellow]A[-] Agent  │  [yellow]Esc[-] Back "
	minimal := " [yellow]N[-] New backup  │  [yellow]R[-] Run  │  [yellow]Esc[-] Back "
	return firstDashboardFooterThatFits(width, full, medium, short, minimal)
}

func backupRunSummary(run backupcore.Run) string {
	when := run.StartedAt.Local().Format("Jan 02 15:04")
	return fmt.Sprintf("%s %s", run.Status, when)
}

func checkboxValue(form *tview.Form, label string) bool {
	item := form.GetFormItemByLabel(label)
	checkbox, ok := item.(*tview.Checkbox)
	return ok && checkbox.IsChecked()
}

func digitsOnly(value string) bool {
	if value == "" {
		return true
	}
	_, err := strconv.Atoi(value)
	return err == nil
}

func parseBackupDecodedGiB(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("enter a whole number of GiB (for example 1 or 8)")
	}
	gib, err := strconv.ParseInt(value, 10, 64)
	if err != nil || gib < 1 {
		return 0, fmt.Errorf("decoded-size limit must be a whole number of GiB greater than zero")
	}
	if gib > math.MaxInt64/backupDecodedGiB {
		return 0, fmt.Errorf("decoded-size limit is too large")
	}
	return gib * backupDecodedGiB, nil
}

func formatBackupDecodedLimit(value int64) string {
	if value == 0 {
		value = backupcore.DefaultMaxDecodedBytes
	}
	if value > 0 && value%backupDecodedGiB == 0 {
		return fmt.Sprintf("%d GiB", value/backupDecodedGiB)
	}
	return fmt.Sprintf("%d bytes", value)
}

func weekdayText(days []int) string {
	if len(days) == 0 {
		return "Mon"
	}
	names := []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	parts := make([]string, 0, len(days))
	for _, day := range days {
		if day >= 0 && day < len(names) {
			parts = append(parts, names[day])
		}
	}
	return strings.Join(parts, ",")
}

func parseWeekdays(raw string) ([]int, error) {
	aliases := map[string]int{"sun": 0, "sunday": 0, "mon": 1, "monday": 1, "tue": 2, "tues": 2, "tuesday": 2, "wed": 3, "wednesday": 3, "thu": 4, "thur": 4, "thurs": 4, "thursday": 4, "fri": 5, "friday": 5, "sat": 6, "saturday": 6}
	seen := map[int]bool{}
	var days []int
	for _, part := range strings.FieldsFunc(strings.ToLower(raw), func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		day, ok := aliases[part]
		if !ok {
			return nil, fmt.Errorf("unknown weekday %q; use Mon,Tue,…", part)
		}
		if !seen[day] {
			seen[day] = true
			days = append(days, day)
		}
	}
	sort.Ints(days)
	return days, nil
}
