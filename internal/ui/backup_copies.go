package ui

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
)

const (
	pageBackupCopies        = "backupCopies"
	pageBackupCopyForm      = "backupCopyForm"
	pageBackupCopyHistory   = "backupCopyHistory"
	pageBackupCopyRetention = "backupCopyRetention"
	pageBackupCopyInspect   = "backupCopyInspect"

	copyTopologyLocalLocal = iota
	copyTopologyLocalSFTP
	copyTopologyBackupSFTP
	copyTopologySFTPLocal
	copyTopologyRcloneLocal
)

var backupCopyTopologyOptions = []string{
	"Local folder -> local folder",
	"Local folder -> SFTP vault (push)",
	"Backup plan -> SFTP vault (push)",
	"SFTP producer -> local vault (pull)",
	"rclone source -> local vault (pull)",
}

type backupCopyFormDraft struct {
	job                   backupcore.CopyJob
	topology              int
	localSource           string
	localDestination      string
	sftpLocation          string
	sftpKind              backupcore.CopyEndpointKind
	identityPath          string
	pinnedHostKey         string
	rcloneSource          string
	backupJobID           string
	producerFilter        string
	jobFilter             string
	everyMinutes          string
	wallClockTimes        string
	weekdays              string
	timezone              string
	freshnessMinutes      string
	keepLatest            string
	timeoutMinutes        string
	volumeMode            backupcore.CopyVolumeMode
	volumeMountPoint      string
	volumeSentinelFile    string
	volumeIdentity        string
	volumeUUID            string
	volumeFilesystem      string
	volumeLabel           string
	volumeMountOptions    string
	volumeWarmupSeconds   string
	volumeCooldownSeconds string
	volumeSpindown        bool
	smtpPort              string
	recipients            string
}

var backupCopyVolumeModeLabels = []string{
	"Normal local folder (no volume binding)",
	"Already mounted (verify identity only)",
	"OS-managed mount (verify identity only)",
	"Managed Linux block device",
}

var backupCopyVolumeModeValues = []backupcore.CopyVolumeMode{
	"",
	backupcore.CopyVolumeAlreadyMounted,
	backupcore.CopyVolumeOSManaged,
	backupcore.CopyVolumeManagedLinuxBlockDevice,
}

func backupCopyHasLocalDestination(topology int) bool {
	return topology == copyTopologyLocalLocal || topology == copyTopologySFTPLocal || topology == copyTopologyRcloneLocal
}

func backupCopyVolumeModeIndex(mode backupcore.CopyVolumeMode) int {
	for index, candidate := range backupCopyVolumeModeValues {
		if candidate == mode {
			return index
		}
	}
	return 0
}

func copyDestinationVolumeLabel(volume *backupcore.CopyDestinationVolume) string {
	if volume == nil {
		return "normal local folder; no mount lifecycle is owned by dbterm"
	}
	label := fmt.Sprintf("%s · sentinel %s", volume.Mode, nonEmptyOr(volume.SentinelFile, ".dbterm-volume-id"))
	if volume.Mode == backupcore.CopyVolumeManagedLinuxBlockDevice {
		label += fmt.Sprintf(" · UUID %s · %s", nonEmptyOr(volume.FilesystemUUID, "not set"), nonEmptyOr(volume.ExpectedFilesystem, "filesystem not set"))
		if volume.ExpectedVolumeLabel != "" {
			label += " · label " + volume.ExpectedVolumeLabel
		}
		if volume.Spindown {
			label += " · spindown"
		}
	}
	label += " · mount " + volume.MountPoint
	return label
}

func copyJobCountLabel(count int) string {
	if count == 1 {
		return "1 copy job"
	}
	return fmt.Sprintf("%d copy jobs", count)
}

func latestCopyRuns(runs []backupcore.CopyRun) map[string]backupcore.CopyRun {
	latest := make(map[string]backupcore.CopyRun)
	for _, run := range runs {
		current, exists := latest[run.JobID]
		if !exists || copyRunRecordedAt(run).After(copyRunRecordedAt(current)) {
			latest[run.JobID] = run
		}
	}
	return latest
}

func copyRunRecordedAt(run backupcore.CopyRun) time.Time {
	if !run.FinishedAt.IsZero() {
		return run.FinishedAt
	}
	return run.StartedAt
}

func copyJobBoundToBackup(job backupcore.CopyJob, backupJobID string) bool {
	backupJobID = strings.TrimSpace(backupJobID)
	return backupJobID != "" && (job.SourceBackupJobID == backupJobID || job.ArtifactFilter.JobID == backupJobID)
}

func copyJobsBoundToBackup(jobs []backupcore.CopyJob, backupJobID string) []backupcore.CopyJob {
	bound := make([]backupcore.CopyJob, 0)
	for _, job := range jobs {
		if copyJobBoundToBackup(job, backupJobID) {
			bound = append(bound, job)
		}
	}
	return bound
}

func backupCopyTopologySummary(jobs []backupcore.CopyJob, backupJobID string) string {
	bound := copyJobsBoundToBackup(jobs, backupJobID)
	if len(bound) == 0 {
		return "none configured; C adds an independent recovery copy"
	}
	parts := make([]string, 0, min(len(bound), 3))
	for _, job := range bound {
		direction := "push to " + copyEndpointCompact(job.Destination)
		if job.Mode == backupcore.CopyModePull {
			direction = "pull from " + copyEndpointCompact(job.Source)
		}
		parts = append(parts, job.Name+" ("+direction+")")
		if len(parts) == 3 {
			break
		}
	}
	if remaining := len(bound) - len(parts); remaining > 0 {
		parts = append(parts, fmt.Sprintf("+%d more", remaining))
	}
	return fmt.Sprintf("%d configured: %s", len(bound), strings.Join(parts, "; "))
}

func backupCopyHealthSummary(jobs []backupcore.CopyJob, latest map[string]backupcore.CopyRun, backupJobID string, now time.Time) string {
	bound := copyJobsBoundToBackup(jobs, backupJobID)
	if len(bound) == 0 {
		return "local recovery point only; off-machine protection is not configured"
	}
	counts := make(map[string]int)
	for _, job := range bound {
		run, found := latest[job.ID]
		counts[copyHealthClass(job, run, found, now)]++
	}
	order := []string{"healthy", "warning", "failed", "running", "never", "disabled"}
	parts := make([]string, 0, len(order))
	for _, state := range order {
		if count := counts[state]; count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", count, state))
		}
	}
	return strings.Join(parts, " · ")
}

func copyHealthClass(job backupcore.CopyJob, run backupcore.CopyRun, found bool, now time.Time) string {
	if job.Trigger != backupcore.CopyTriggerManual && job.Enabled && !job.HasCurrentTransferProof() {
		return "warning"
	}
	if !found {
		if job.Trigger != backupcore.CopyTriggerManual && !job.Enabled {
			return "disabled"
		}
		return "never"
	}
	switch run.Status {
	case backupcore.RunRunning:
		return "running"
	case backupcore.RunFailed, backupcore.RunCanceled:
		return "failed"
	case backupcore.RunSucceeded:
		if (job.Trigger != backupcore.CopyTriggerManual && !job.Enabled) || len(run.Warnings) > 0 || strings.TrimSpace(run.RetentionError) != "" || strings.TrimSpace(run.NotificationError) != "" || copyRunHasIncompletePublication(run) {
			return "warning"
		}
		if run.Discovered == 0 && run.AlreadyPresent == 0 && len(run.Artifacts) == 0 {
			return "warning"
		}
		if job.ExpectedFreshnessMinutes > 0 {
			newest, ok := copyRunNewestSourceTime(run)
			if !ok || now.Sub(newest) > time.Duration(job.ExpectedFreshnessMinutes)*time.Minute {
				return "warning"
			}
		}
		return "healthy"
	default:
		return "warning"
	}
}

func copyRunHasIncompletePublication(run backupcore.CopyRun) bool {
	for _, artifact := range run.Artifacts {
		if artifact.PublicationState != backupcore.ArtifactPublicationComplete {
			return true
		}
	}
	return false
}

func copyRunNewestSourceTime(run backupcore.CopyRun) (time.Time, bool) {
	newest := run.NewestSourceAt
	for _, artifact := range run.Artifacts {
		if artifact.SourceCreatedAt.After(newest) {
			newest = artifact.SourceCreatedAt
		}
	}
	return newest, !newest.IsZero()
}

func copyEndpointCompact(endpoint backupcore.CopyEndpoint) string {
	location := strings.TrimSpace(endpoint.Location)
	if location == "" {
		return string(endpoint.Kind)
	}
	return location
}

func copyEndpointLabel(endpoint backupcore.CopyEndpoint, sourceBackupJobID string, backupNames map[string]string) string {
	if endpoint.Kind == backupcore.CopyEndpointLocal && strings.TrimSpace(endpoint.Location) == "" && sourceBackupJobID != "" {
		return fmt.Sprintf("local backup plan %q", nonEmptyOr(backupNames[sourceBackupJobID], sourceBackupJobID))
	}
	return fmt.Sprintf("%s · %s", endpoint.Kind, nonEmptyOr(strings.TrimSpace(endpoint.Location), "location not set"))
}

func copyTriggerLabel(job backupcore.CopyJob) string {
	switch job.Trigger {
	case backupcore.CopyTriggerAfterSuccess:
		if !job.Enabled {
			return "after successful backup (paused)"
		}
		return "after successful backup"
	case backupcore.CopyTriggerTimed:
		label := backupScheduleLabel(job.Schedule)
		if !job.Enabled {
			label += " (paused)"
		}
		return label
	default:
		return "manual"
	}
}

func copyAutomationProofLabel(job backupcore.CopyJob) string {
	if job.Trigger == backupcore.CopyTriggerManual {
		return "not required for on-demand runs"
	}
	if job.HasCurrentTransferProof() {
		return "real transfer verified " + job.TransferProofAt.Local().Format("Jan 02 15:04 MST")
	}
	if !job.TransferProofAt.IsZero() {
		return "configuration changed since the last real transfer; run manually again before enabling"
	}
	return "required before automatic enablement; press R to run a real transfer"
}

func copyLastResultLabel(run backupcore.CopyRun, found bool) string {
	if !found {
		return "never run"
	}
	when := copyRunRecordedAt(run)
	stamp := "time not recorded"
	if !when.IsZero() {
		stamp = when.Local().Format("Jan 02 15:04")
	}
	switch run.Status {
	case backupcore.RunRunning:
		return "running since " + run.StartedAt.Local().Format("Jan 02 15:04")
	case backupcore.RunSucceeded:
		if len(run.Artifacts) > 0 {
			return fmt.Sprintf("succeeded %s · %d copied · %s", stamp, len(run.Artifacts), run.RequiredVerification)
		}
		if run.AlreadyPresent > 0 {
			return fmt.Sprintf("scan succeeded %s · %d already present", stamp, run.AlreadyPresent)
		}
		return "scan succeeded " + stamp + " · no new copy"
	case backupcore.RunCanceled:
		return "canceled " + stamp
	default:
		return "failed " + stamp
	}
}

func copyRunSpeedLabel(run backupcore.CopyRun, found bool) string {
	if !found {
		return "not measured; run or test this copy"
	}
	duration := run.FinishedAt.Sub(run.StartedAt)
	if duration <= 0 {
		return "not measured"
	}
	if run.BytesCopied <= 0 {
		return fmt.Sprintf("%s scan; no bytes transferred", formatBackupProgressDuration(duration))
	}
	rate := float64(run.BytesCopied) / duration.Seconds()
	rateLabel := fmt.Sprintf("%.0f B/s", rate)
	if rate >= 1024*1024 {
		rateLabel = fmt.Sprintf("%.2f MiB/s", rate/(1024*1024))
	} else if rate >= 1024 {
		rateLabel = fmt.Sprintf("%.2f KiB/s", rate/1024)
	}
	return fmt.Sprintf("%s over %s", rateLabel, formatBackupProgressDuration(duration))
}

func copyLagLabel(job backupcore.CopyJob, run backupcore.CopyRun, found bool, now time.Time) string {
	if !found {
		return "unknown · no copy run"
	}
	newest, ok := copyRunNewestSourceTime(run)
	if !ok {
		return "unknown · newest source artifact time was not recorded in this run"
	}
	lag := now.Sub(newest)
	if lag < 0 {
		lag = 0
	}
	label := formatBackupProgressDuration(lag)
	if job.ExpectedFreshnessMinutes <= 0 {
		return label + " · no freshness limit"
	}
	limit := time.Duration(job.ExpectedFreshnessMinutes) * time.Minute
	if lag > limit {
		return fmt.Sprintf("STALE %s · expected within %s", label, formatBackupProgressDuration(limit))
	}
	return fmt.Sprintf("%s · within %s target", label, formatBackupProgressDuration(limit))
}

func copyWarningsLabel(job backupcore.CopyJob, run backupcore.CopyRun, found bool, now time.Time) string {
	warnings := make([]string, 0, len(run.Warnings)+3)
	if found {
		warnings = append(warnings, run.Warnings...)
		if strings.TrimSpace(run.Error) != "" {
			warnings = append(warnings, "run failed: "+run.Error)
		}
		if strings.TrimSpace(run.RetentionError) != "" {
			warnings = append(warnings, "retention: "+run.RetentionError)
		}
		if strings.TrimSpace(run.NotificationError) != "" {
			warnings = append(warnings, "notification: "+run.NotificationError)
		}
		if copyRunHasIncompletePublication(run) {
			warnings = append(warnings, "artifact publication incomplete; not recovery-ready")
		}
		if run.Status == backupcore.RunSucceeded && run.Discovered == 0 && run.AlreadyPresent == 0 && len(run.Artifacts) == 0 {
			warnings = append(warnings, "no completed artifact manifests matched this copy job")
		}
		if job.ExpectedFreshnessMinutes > 0 {
			newest, ok := copyRunNewestSourceTime(run)
			if !ok {
				warnings = append(warnings, "freshness cannot be proven from this run")
			} else if now.Sub(newest) > time.Duration(job.ExpectedFreshnessMinutes)*time.Minute {
				warnings = append(warnings, "newest recorded source artifact is stale")
			}
		}
	}
	if job.Trigger != backupcore.CopyTriggerManual && !job.Enabled {
		warnings = append(warnings, "automatic copy is paused")
	}
	if job.Trigger != backupcore.CopyTriggerManual && !job.HasCurrentTransferProof() {
		warnings = append(warnings, "automatic copy needs a successful real transfer for its current configuration")
	}
	if job.Mode == backupcore.CopyModePush && job.Destination.Kind == backupcore.CopyEndpointRclone {
		warnings = append(warnings, "rclone push is not supported")
	}
	if len(warnings) == 0 {
		return "none recorded"
	}
	if len(warnings) > 2 {
		return fmt.Sprintf("%s; %s; +%d more", warnings[0], warnings[1], len(warnings)-2)
	}
	return strings.Join(warnings, "; ")
}

func copyTransportLabel(job backupcore.CopyJob) string {
	source, destination := job.Source.Kind, job.Destination.Kind
	switch {
	case source == backupcore.CopyEndpointLocal && destination == backupcore.CopyEndpointLocal:
		return "local transfer available"
	case source == backupcore.CopyEndpointLocal && (destination == backupcore.CopyEndpointSFTP || destination == backupcore.CopyEndpointSSH):
		return "SFTP push available · pinned host key required"
	case (source == backupcore.CopyEndpointSFTP || source == backupcore.CopyEndpointSSH) && destination == backupcore.CopyEndpointLocal:
		return "SFTP pull available · pinned host key required"
	case source == backupcore.CopyEndpointRclone && destination == backupcore.CopyEndpointLocal:
		return "rclone pull available"
	case destination == backupcore.CopyEndpointRclone:
		return "UNSUPPORTED · rclone push is not available"
	default:
		return fmt.Sprintf("unsupported transport %s -> %s", source, destination)
	}
}

func copyStateStyled(job backupcore.CopyJob, run backupcore.CopyRun, found bool, now time.Time) string {
	state := copyHealthClass(job, run, found, now)
	color := map[string]string{
		"healthy": "green", "warning": "#f9e2af", "failed": "red", "running": "#89b4fa", "never": "#6c7086", "disabled": "#6c7086",
	}[state]
	return fmt.Sprintf("[%s]%s[-]", color, strings.ToUpper(state))
}

func (a *App) showBackupCopies(preferBackupJobID string) {
	if _, err := a.ensureBackupStore(); err != nil {
		a.ShowAlert(fmt.Sprintf("%s Copies are unavailable:\n\n%v", iconWarn, err), pageBackupCenter)
		return
	}
	jobs, err := a.backupStore.ListCopyJobs(context.Background())
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not load copy jobs:\n\n%v", iconWarn, err), pageBackupCenter)
		return
	}
	runs, err := a.backupStore.ListCopyRuns(context.Background(), "", 1000)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not load copy activity:\n\n%v", iconWarn, err), pageBackupCenter)
		return
	}
	latest := latestCopyRuns(runs)
	for _, job := range jobs {
		if _, exists := latest[job.ID]; exists {
			continue
		}
		run, found, latestErr := a.backupStore.LatestCopyRun(context.Background(), job.ID)
		if latestErr != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not load latest copy activity:\n\n%v", iconWarn, latestErr), pageBackupCenter)
			return
		}
		if found {
			latest[job.ID] = run
		}
	}
	backupJobs, err := a.backupStore.ListJobs(context.Background())
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not load backup plans for copy topology:\n\n%v", iconWarn, err), pageBackupCenter)
		return
	}
	backupNames := make(map[string]string, len(backupJobs))
	for _, job := range backupJobs {
		backupNames[job.ID] = job.Name
	}

	now := time.Now()
	header := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	header.SetBackgroundColor(bg)
	health := make(map[string]int)
	enabledTimed := 0
	for _, job := range jobs {
		run, found := latest[job.ID]
		health[copyHealthClass(job, run, found, now)]++
		if job.Enabled && job.Trigger == backupcore.CopyTriggerTimed {
			enabledTimed++
		}
	}
	header.SetText(fmt.Sprintf("\n[::b][#cba6f7]%s Copies[-][-]  [#a6adc8]backup artifacts move independently from generation[-]\n[#a6adc8]%d jobs · %d timed · %d healthy · %d warning/failed[-]",
		iconBackup, len(jobs), enabledTimed, health["healthy"], health["warning"]+health["failed"]))

	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).SetTitle(fmt.Sprintf(" Copy Jobs (%d) ", len(jobs))).SetBorderColor(surface1).SetTitleColor(mauve)
	list.SetBackgroundColor(bg)
	list.SetMainTextColor(text).SetSecondaryTextColor(subtext0).SetSelectedBackgroundColor(surface0).SetSelectedTextColor(green)
	if len(jobs) == 0 {
		list.AddItem("  [::b][#f9e2af]No independent copies configured[-][-]", "  Press N to add a local, SFTP, or rclone-pull copy job.", 0, nil)
		a.backupCenterSelectedCopy = ""
	} else {
		for _, job := range jobs {
			run, found := latest[job.ID]
			list.AddItem(fmt.Sprintf("  %s  [::b]%s[-]  [#89b4fa]%s[-]", copyStateStyled(job, run, found, now), tview.Escape(job.Name), strings.ToUpper(string(job.Mode))),
				tview.Escape(fmt.Sprintf("  %s -> %s · %s · %s", copyEndpointLabel(job.Source, job.SourceBackupJobID, backupNames), copyEndpointLabel(job.Destination, "", backupNames), copyTriggerLabel(job), copyLastResultLabel(run, found))), 0, nil)
		}
	}

	selectedIndex := 0
	if a.backupCenterSelectedCopy == "" && preferBackupJobID != "" {
		for _, job := range jobs {
			if copyJobBoundToBackup(job, preferBackupJobID) {
				a.backupCenterSelectedCopy = job.ID
				break
			}
		}
	}
	for index, job := range jobs {
		if job.ID == a.backupCenterSelectedCopy {
			selectedIndex = index
			break
		}
	}

	detail := tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	detail.SetBorder(true).SetTitle(" Selected Copy ").SetTitleColor(mauve).SetBorderColor(surface1).SetBackgroundColor(mantle)
	updateDetail := func(index int) {
		if index < 0 || index >= len(jobs) {
			detail.SetTitle(" Start Here ")
			detail.SetText(" [#89b4fa]N[-] Add a copy job\n [#a6adc8]A copy moves only completed artifacts with manifests. Backup generation and copy results remain independent.[-]")
			return
		}
		job := jobs[index]
		run, found := latest[job.ID]
		next := "not scheduled"
		if job.Trigger == backupcore.CopyTriggerTimed && !job.Enabled {
			next = "schedule paused"
		} else if job.Trigger == backupcore.CopyTriggerTimed && job.NextRunAt.IsZero() {
			next = "awaiting agent calculation"
		} else if job.Trigger == backupcore.CopyTriggerTimed && !job.NextRunAt.IsZero() {
			next = job.NextRunAt.Local().Format("Mon 02 Jan, 15:04 MST")
		}
		detail.SetTitle(" Selected Copy ")
		detailText := fmt.Sprintf(
			" [#89b4fa]MODE[-]      %s · owner: %s\n [#89b4fa]SOURCE[-]    %s\n [#89b4fa]DESTINATION[-] %s\n [#89b4fa]TRIGGER[-]   %s · next %s\n [#89b4fa]PROOF[-]     %s\n [#89b4fa]VERIFY[-]    %s · %s\n [#89b4fa]LAST[-]      %s\n [#89b4fa]SPEED[-]     %s\n [#89b4fa]LAG[-]       %s\n [#89b4fa]WARNINGS[-]  %s",
			tview.Escape(string(job.Mode)), map[backupcore.CopyMode]string{backupcore.CopyModePush: "producer", backupcore.CopyModePull: "vault"}[job.Mode],
			tview.Escape(copyEndpointLabel(job.Source, job.SourceBackupJobID, backupNames)),
			tview.Escape(copyEndpointLabel(job.Destination, "", backupNames)),
			tview.Escape(copyTriggerLabel(job)), tview.Escape(next), tview.Escape(copyAutomationProofLabel(job)), tview.Escape(string(job.Verification)), tview.Escape(copyTransportLabel(job)),
			tview.Escape(copyLastResultLabel(run, found)), tview.Escape(copyRunSpeedLabel(run, found)), tview.Escape(copyLagLabel(job, run, found, now)), tview.Escape(copyWarningsLabel(job, run, found, now)),
		)
		if job.Destination.Kind == backupcore.CopyEndpointLocal {
			volumeLine := "\n [#89b4fa]VOLUME[-]    " + tview.Escape(copyDestinationVolumeLabel(job.DestinationVolume))
			detailText = strings.Replace(detailText, "\n [#89b4fa]TRIGGER", volumeLine+"\n [#89b4fa]TRIGGER", 1)
		}
		detail.SetText(detailText)
	}
	list.SetChangedFunc(func(index int, _, _ string, _ rune) {
		if index >= 0 && index < len(jobs) {
			a.backupCenterSelectedCopy = jobs[index].ID
		}
		updateDetail(index)
	})
	if len(jobs) > 0 {
		a.backupCenterSelectedCopy = jobs[selectedIndex].ID
		list.SetCurrentItem(selectedIndex)
	}
	updateDetail(selectedIndex)

	selectedCopy := func() (*backupcore.CopyJob, bool) {
		index := list.GetCurrentItem()
		if index < 0 || index >= len(jobs) {
			a.ShowAlert(fmt.Sprintf("%s Create a copy job first (N).", iconInfo), pageBackupCopies)
			return nil, false
		}
		job := jobs[index]
		return &job, true
	}
	closeCopies := func() {
		a.pages.RemovePage(pageBackupCopies)
		a.showBackupCenter()
	}
	list.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyBackspace || event.Key() == tcell.KeyBackspace2 {
			closeCopies()
			return nil
		}
		if event.Key() == tcell.KeyEnter {
			if job, ok := selectedCopy(); ok {
				a.showBackupCopyForm(job)
			} else {
				a.showBackupCopyForm(nil)
			}
			return nil
		}
		if event.Key() == tcell.KeyRune && event.Modifiers()&(tcell.ModCtrl|tcell.ModAlt|tcell.ModMeta) == 0 {
			switch event.Rune() {
			case 'n', 'N':
				a.showBackupCopyForm(nil)
				return nil
			case 'e', 'E':
				if job, ok := selectedCopy(); ok {
					a.showBackupCopyForm(job)
				}
				return nil
			case 'r', 'R':
				if job, ok := selectedCopy(); ok {
					a.runBackupCopyNow(job.ID)
				}
				return nil
			case 't', 'T':
				if job, ok := selectedCopy(); ok {
					a.testBackupCopyEndpoint(*job)
				}
				return nil
			case 'i', 'I':
				if job, ok := selectedCopy(); ok {
					a.showBackupCopyInspectForm(*job)
				}
				return nil
			case 'h', 'H':
				if job, ok := selectedCopy(); ok {
					a.showBackupCopyHistory(*job)
				}
				return nil
			case 'p', 'P':
				if job, ok := selectedCopy(); ok {
					a.previewBackupCopyRetention(*job)
				}
				return nil
			case ' ':
				if job, ok := selectedCopy(); ok {
					if job.Trigger != backupcore.CopyTriggerTimed {
						a.ShowAlert(fmt.Sprintf("%s Space pauses or resumes timed copy jobs. This copy uses %s; edit it to change its trigger.", iconInfo, copyTriggerLabel(*job)), pageBackupCopies)
						return nil
					}
					if err := a.backupStore.SetCopyJobEnabled(context.Background(), job.ID, !job.Enabled); err != nil {
						a.ShowAlert(fmt.Sprintf("%s Could not change copy schedule:\n\n%v", iconWarn, err), pageBackupCopies)
						return nil
					}
					a.showBackupCopies("")
					if !job.Enabled {
						a.offerBackupAgentStart()
					}
				}
				return nil
			case 'd', 'D':
				if job, ok := selectedCopy(); ok {
					a.confirmDeleteBackupCopy(*job)
				}
				return nil
			}
		}
		return event
	})

	width, _ := a.getScreenSize()
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(backupCopiesFooterText(width))
	layout := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 4, 0, false).
		AddItem(list, 0, 1, true).
		AddItem(detail, 13, 0, false).
		AddItem(footer, 1, 0, false)
	a.pages.AddAndSwitchToPage(pageBackupCopies, layout, true)
	a.app.SetFocus(list)
}

func backupCopiesFooterText(width int) string {
	return footerTextThatFits(width,
		" [yellow]N[-] New · [yellow]E[-] Edit · [yellow]T[-] Test · [yellow]R[-] Run · [yellow]I[-] Inspect · [yellow]H[-] History · [yellow]P[-] Retention · [yellow]Space[-] Pause · [yellow]D[-] Delete · [yellow]Esc[-] Back ",
		" [yellow]N[-] New · [yellow]T[-] Test · [yellow]R[-] Run · [yellow]I[-] Inspect · [yellow]H[-] History · [yellow]P[-] Prune · [yellow]D[-] Delete · [yellow]Esc[-] Back ",
		" [yellow]N[-] New · [yellow]T[-] Test · [yellow]R[-] Run · [yellow]I[-] Inspect · [yellow]H[-] Hist · [yellow]P[-] Prune · [yellow]Esc[-] Back ",
		" [yellow]R[-] Run · [yellow]I[-] Inspect · [yellow]H[-] Hist · [yellow]P[-] Prune · [yellow]Esc[-] Back ",
	)
}

func (a *App) showBackupCopyInspectForm(job backupcore.CopyJob) {
	a.pages.RemovePage(pageBackupCopyInspect)
	artifacts, err := a.backupStore.ListCopyArtifactsForInspection(context.Background(), job.ID)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not load copied recovery points:\n\n%v", iconFail, err), pageBackupCopies)
		return
	}
	if len(artifacts) == 0 {
		a.ShowAlert(fmt.Sprintf("%s No completed, unpruned recovery copies are recorded for %s.\n\nRun the copy successfully, then inspect the verified result.", iconInfo, tview.Escape(job.Name)), pageBackupCopies)
		return
	}

	closePicker := func() {
		a.pages.RemovePage(pageBackupCopyInspect)
		a.pages.ShowPage(pageBackupCopies)
	}
	list := newBackupCopyArtifactPicker(job, artifacts, func(artifact backupcore.CopyArtifactResult) {
		a.showBackupCopyInspectOptions(job, artifact)
	}, closePicker)
	help := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	help.SetBackgroundColor(crust)
	help.SetText(" [yellow]Enter[-] Inspect selected recovery point · [yellow]Esc[-] Back ")
	container := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(list, 0, 1, true).AddItem(help, 1, 0, false)
	w, h := a.modalSize(76, 118, 14, 21)
	grid := tview.NewGrid().SetColumns(0, w, 0).SetRows(0, h, 0).AddItem(container, 1, 1, 1, 1, 0, 0, true)
	a.pages.AddPage(pageBackupCopyInspect, grid, true, true)
	a.app.SetFocus(list)
}

func newBackupCopyArtifactPicker(job backupcore.CopyJob, artifacts []backupcore.CopyArtifactResult, onSelect func(backupcore.CopyArtifactResult), onCancel func()) *tview.List {
	list := tview.NewList().ShowSecondaryText(true)
	list.SetBorder(true).
		SetTitle(fmt.Sprintf(" Recovery Points · %s (%d) ", tview.Escape(job.Name), len(artifacts))).
		SetTitleColor(mauve).
		SetBorderColor(surface1)
	list.SetBackgroundColor(bg)
	list.SetMainTextColor(text).
		SetSecondaryTextColor(subtext0).
		SetSelectedBackgroundColor(surface0).
		SetSelectedTextColor(green)
	for _, artifact := range artifacts {
		main, secondary := backupCopyArtifactPickerLabels(artifact)
		list.AddItem(main, secondary, 0, nil)
	}
	list.SetSelectedFunc(func(index int, _, _ string, _ rune) {
		if index >= 0 && index < len(artifacts) && onSelect != nil {
			onSelect(artifacts[index])
		}
	})
	list.SetDoneFunc(func() {
		if onCancel != nil {
			onCancel()
		}
	})
	return list
}

func backupCopyArtifactPickerLabels(artifact backupcore.CopyArtifactResult) (string, string) {
	createdAt := artifact.SourceCreatedAt
	if createdAt.IsZero() {
		createdAt = artifact.VerifiedAt
	}
	created := "time unavailable"
	if !createdAt.IsZero() {
		created = createdAt.Local().Format("2006-01-02 15:04 MST")
	}
	verified := "not recorded"
	if !artifact.VerifiedAt.IsZero() {
		verified = artifact.VerifiedAt.Local().Format("2006-01-02 15:04 MST")
	}
	sizeBytes := artifact.SizeBytes
	if sizeBytes < 0 {
		sizeBytes = 0
	}
	main := fmt.Sprintf("  %s · %s · %s · ID %s", created, backupcore.FormatByteSize(uint64(sizeBytes)), artifact.Verification, shortCopyArtifactID(artifact.ArtifactID))
	destination := strings.TrimRight(strings.TrimSpace(artifact.Destination), "/\\")
	if separator := strings.LastIndexAny(destination, "/\\"); separator >= 0 {
		destination = destination[separator+1:]
	}
	secondary := fmt.Sprintf("  Verified %s · %s", verified, nonEmptyOr(destination, "destination not recorded"))
	return tview.Escape(main), tview.Escape(secondary)
}

func shortCopyArtifactID(value string) string {
	const prefixLength = 10
	const suffixLength = 6
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= prefixLength+suffixLength+1 {
		return string(runes)
	}
	return string(runes[:prefixLength]) + "…" + string(runes[len(runes)-suffixLength:])
}

func (a *App) showBackupCopyInspectOptions(job backupcore.CopyJob, artifact backupcore.CopyArtifactResult) {
	form := tview.NewForm()
	form.SetBorder(true).SetTitle(" Inspect Verified Recovery Copy ").SetTitleColor(mauve).SetBorderColor(surface1)
	form.SetBackgroundColor(bg)
	form.SetFieldBackgroundColor(mantle).SetFieldTextColor(text).SetLabelColor(text).
		SetButtonBackgroundColor(surface1).SetButtonTextColor(green)
	form.AddTextView("Copy", tview.Escape(job.Name), 0, 1, true, false)
	main, _ := backupCopyArtifactPickerLabels(artifact)
	form.AddTextView("Recovery point", main+"\n[#a6adc8]Exact ID: "+tview.Escape(artifact.ArtifactID)+"[-]", 0, 3, true, false)
	form.AddInputField("age Identity (optional)", "", 72, nil, nil)
	defaultDecodedGiB := (backupcore.DefaultMaxDecodedBytes + backupDecodedGiB - 1) / backupDecodedGiB
	if defaultDecodedGiB < 1 {
		defaultDecodedGiB = 1
	}
	form.AddInputField("Max Decoded GiB", strconv.FormatInt(defaultDecodedGiB, 10), 8, func(text string, _ rune) bool { return digitsOnly(text) }, nil)
	closeForm := func() {
		a.showBackupCopyInspectForm(job)
	}
	form.AddButton("Stage + Inspect", func() {
		maxDecodedBytes, err := parseBackupDecodedGiB(formInputValueByLabel(form, "Max Decoded GiB"))
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s Invalid decoded-size limit:\n\n%v", iconWarn, err), pageBackupCopyInspect)
			return
		}
		identity := formInputValueByLabel(form, "age Identity (optional)")
		a.pages.RemovePage(pageBackupCopyInspect)
		a.inspectBackupCopyAsync(job, artifact, identity, maxDecodedBytes)
	})
	form.AddButton("Back", closeForm)
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			closeForm()
			return nil
		}
		return event
	})
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(" Manifest identity + size + SHA-256 + format are reverified in private staging before inspection ")
	container := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(form, 0, 1, true).AddItem(footer, 1, 0, false)
	w, h := a.modalSize(74, 116, 14, 18)
	grid := tview.NewGrid().SetColumns(0, w, 0).SetRows(0, h, 0).AddItem(container, 1, 1, 1, 1, 0, 0, true)
	a.pages.RemovePage(pageBackupCopyInspect)
	a.pages.AddPage(pageBackupCopyInspect, grid, true, true)
	a.app.SetFocus(form)
}

func (a *App) inspectBackupCopyAsync(job backupcore.CopyJob, artifact backupcore.CopyArtifactResult, identity string, maxDecodedBytes int64) {
	ctx, cancel := context.WithCancel(context.Background())
	var canceled atomic.Bool
	token := a.showLoadingModal("Staging and verifying recovery copy "+shortCopyArtifactID(artifact.ArtifactID)+"...", withLoadingCancelOutcome("Press Esc to cancel copy inspection.", func() {
		canceled.Store(true)
		cancel()
	}))
	go func() {
		var staged *backupcore.StagedCopyArtifact
		var volumeWarnings []string
		staged, volumeWarnings, err := backupcore.StageCopyArtifactForInspectionWithVolume(ctx, a.backupStore, job, artifact, backupcore.CopyInspectionStageOptions{Location: backupcore.CopyInspectionDestination})
		var inspection *backupcore.Inspection
		if err == nil {
			inspection, err = staged.Inspect(ctx, backupcore.InspectOptions{AgeIdentityPath: identity, MaxDecodedBytes: maxDecodedBytes})
		}
		if err != nil && staged != nil {
			if cleanupErr := staged.Close(); cleanupErr != nil {
				err = fmt.Errorf("%w; private staging cleanup also failed: %v", err, cleanupErr)
			}
			staged = nil
		}
		cancel()
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				if staged != nil {
					_ = staged.Close()
				}
				return
			}
			if canceled.Load() {
				if staged != nil {
					_ = staged.Close()
				}
				a.ShowAlert(fmt.Sprintf("%s Recovery-copy inspection canceled.", iconWarn), pageBackupCopies)
				return
			}
			if err != nil {
				a.ShowAlert(fmt.Sprintf("%s Recovery-copy inspection failed:\n\n%v", iconFail, err), pageBackupCopies)
				return
			}
			inspection.Warnings = append([]string{fmt.Sprintf("Privately staged from verified recovery copy %s at %s", artifact.ArtifactID, artifact.Destination)}, inspection.Warnings...)
			inspection.Warnings = append(inspection.Warnings, volumeWarnings...)
			a.showBackupInspectionResultWithCleanup(inspection, identity, maxDecodedBytes, staged.Close, pageBackupCopies)
		})
	}()
}

func (a *App) testBackupCopyEndpoint(job backupcore.CopyJob) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	token := a.showLoadingModal("Testing copy endpoint without transferring a backup...", withLoadingCancelOutcome("Press Esc to cancel. No backup artifact is changed.", cancel))
	go func() {
		message, err := a.measureBackupCopyEndpoint(ctx, job)
		cancel()
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				return
			}
			a.backupCenterSelectedCopy = job.ID
			a.showBackupCopies("")
			if err != nil {
				a.ShowAlert(fmt.Sprintf("%s Copy endpoint test failed:\n\n%v\n\nNo backup artifact was changed.", iconFail, err), pageBackupCopies)
				return
			}
			a.ShowAlert(fmt.Sprintf("%s Copy endpoint test passed\n\n%s\n\nNo backup artifact was changed, so automatic scheduling is still locked. Run the copy once to prove end-to-end publication, throughput, and verification.", iconSuccess, tview.Escape(message)), pageBackupCopies)
		})
	}()
}

func (a *App) measureBackupCopyEndpoint(ctx context.Context, job backupcore.CopyJob) (string, error) {
	volumeProof := ""
	if job.DestinationVolume != nil {
		if err := backupcore.VerifyCopyDestinationVolumeIdentity(job); err != nil {
			return "", fmt.Errorf("verify configured destination volume identity: %w", err)
		}
		volumeProof = "\nDestination volume identity: verified (read-only; mount, unmount, sync, and spindown were not exercised)"
	}
	remote := job.Source
	if remote.Kind == backupcore.CopyEndpointLocal {
		remote = job.Destination
	}
	switch remote.Kind {
	case backupcore.CopyEndpointSSH, backupcore.CopyEndpointSFTP:
		measurement, err := backupcore.MeasureSFTPTransport(ctx, remote)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Pinned host key: %s\nConnect: %s\nList: %s\nEntries: %d\nRead/list access: verified\nWrite/create-only access: verified by the first real push, not this read-only test%s",
			measurement.HostKeyFingerprint, measurement.ConnectDuration.Round(time.Millisecond), measurement.ListDuration.Round(time.Millisecond), measurement.Entries, volumeProof), nil
	case backupcore.CopyEndpointRclone:
		measurement, err := backupcore.MeasureRcloneSource(ctx, remote)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("rclone list: %s\nObjects: %d\nCompleted manifests: %d\nAccess: read-only pull source verified%s", measurement.ListDuration.Round(time.Millisecond), measurement.Objects, measurement.CompletedManifests, volumeProof), nil
	case backupcore.CopyEndpointLocal:
		paths := []string{job.Source.Location, job.Destination.Location}
		if job.Source.Location == "" && job.SourceBackupJobID != "" {
			backupJob, err := a.backupStore.GetJob(ctx, job.SourceBackupJobID)
			if err != nil {
				return "", fmt.Errorf("load source backup plan: %w", err)
			}
			paths[0] = backupJob.Destination
		}
		for _, value := range paths {
			if strings.TrimSpace(value) == "" {
				continue
			}
			info, err := os.Lstat(value)
			if err != nil {
				return "", fmt.Errorf("inspect local endpoint %s: %w", value, err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return "", fmt.Errorf("local endpoint must be a real directory, not a symlink: %s", value)
			}
		}
		usage, err := backupcore.DestinationDiskUsage(job.Destination.Location)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Destination: %s\nVolume: %s\nAvailable: %s\nDirectory and volume access: verified%s", filepath.Clean(job.Destination.Location), usage.Volume, backupcore.FormatByteSize(usage.AvailableBytes), volumeProof), nil
	default:
		return "", fmt.Errorf("unsupported copy endpoint kind %q", remote.Kind)
	}
}

func (a *App) showBackupCopyHistory(job backupcore.CopyJob) {
	runs, err := a.backupStore.ListCopyRuns(context.Background(), job.ID, 100)
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not load copy history:\n\n%v", iconWarn, err), pageBackupCopies)
		return
	}
	var content strings.Builder
	if len(runs) == 0 {
		content.WriteString("No copy runs have been recorded.\n")
	}
	for _, run := range runs {
		fmt.Fprintf(&content, "%s  %s  %s\n", strings.ToUpper(string(run.Status)), run.StartedAt.Local().Format(time.RFC3339), copyRunSpeedLabel(run, true))
		fmt.Fprintf(&content, "run %s  discovered %d  copied %d  existing %d  bytes %d\n", run.ID, run.Discovered, len(run.Artifacts), run.AlreadyPresent, run.BytesCopied)
		for _, artifact := range run.Artifacts {
			fmt.Fprintf(&content, "  %s  %s  %d bytes  %s  %s\n", artifact.ArtifactID, artifact.PublicationState, artifact.SizeBytes, artifact.Verification, artifact.Destination)
		}
		for _, warning := range run.Warnings {
			fmt.Fprintf(&content, "  warning: %s\n", warning)
		}
		if run.RetentionError != "" {
			fmt.Fprintf(&content, "  retention: %s\n", run.RetentionError)
		}
		if run.NotificationError != "" {
			fmt.Fprintf(&content, "  notification: %s\n", run.NotificationError)
		}
		if run.Error != "" {
			fmt.Fprintf(&content, "  error: %s\n", run.Error)
		}
		content.WriteByte('\n')
	}
	view := tview.NewTextView().SetWrap(false).SetScrollable(true).SetText(content.String())
	view.SetBorder(true).SetTitle(" Copy History · " + job.Name + " ").SetTitleColor(mauve).SetBorderColor(surface1)
	view.SetBackgroundColor(bg)
	view.SetTextColor(text)
	closeHistory := func() {
		a.pages.RemovePage(pageBackupCopyHistory)
		a.showBackupCopies("")
	}
	view.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyBackspace || event.Key() == tcell.KeyBackspace2 {
			closeHistory()
			return nil
		}
		return event
	})
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter).SetText(" [yellow]Esc[-] Back · Arrow/Page keys scroll ")
	footer.SetBackgroundColor(crust)
	layout := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(view, 0, 1, true).AddItem(footer, 1, 0, false)
	a.pages.AddAndSwitchToPage(pageBackupCopyHistory, layout, true)
	a.app.SetFocus(view)
}

func (a *App) previewBackupCopyRetention(job backupcore.CopyJob) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(job.TimeoutMinutes)*time.Minute)
	token := a.showLoadingModal("Building copy retention preview...", withLoadingCancelOutcome("Press Esc to cancel. Nothing has been deleted.", cancel))
	go func() {
		candidates, volumeWarnings, err := backupcore.PreviewCopyRetentionWithVolume(ctx, a.backupStore, job, time.Now())
		cancel()
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				return
			}
			if err != nil {
				a.showBackupCopies("")
				warningText := ""
				if len(volumeWarnings) > 0 {
					warningText = "\n\nVolume warnings:\n• " + strings.Join(volumeWarnings, "\n• ")
				}
				a.ShowAlert(fmt.Sprintf("%s Could not preview copy retention:\n\n%v%s\n\nNothing was deleted.", iconFail, err, warningText), pageBackupCopies)
				return
			}
			if len(candidates) == 0 {
				a.showBackupCopies("")
				warningText := ""
				if len(volumeWarnings) > 0 {
					warningText = "\n\nVolume warnings:\n• " + strings.Join(volumeWarnings, "\n• ")
				}
				a.ShowAlert(fmt.Sprintf("%s Copy retention is already satisfied. No recovery point would be removed.%s", iconInfo, warningText), pageBackupCopies)
				return
			}
			lines := make([]string, 0, min(len(candidates), 10)+1)
			for index, candidate := range candidates {
				if index == 10 {
					lines = append(lines, fmt.Sprintf("...and %d more", len(candidates)-index))
					break
				}
				lines = append(lines, fmt.Sprintf("%s  %s  %s", candidate.ArtifactID, backupcore.FormatByteSize(uint64(candidate.SizeBytes)), candidate.Path))
			}
			warningText := ""
			if len(volumeWarnings) > 0 {
				warningText = "\n\nVolume warnings:\n• " + strings.Join(volumeWarnings, "\n• ")
			}
			modal := tview.NewModal().SetText(fmt.Sprintf("%s Copy retention preview for [yellow]%s[-]\n\n%d verified recovery point(s) would be removed:\n\n%s\n\nEach artifact and manifest is reverified immediately before deletion. The newest verified recovery point always stays.%s", iconWarn, tview.Escape(job.Name), len(candidates), tview.Escape(strings.Join(lines, "\n")), tview.Escape(warningText))).
				AddButtons([]string{" Apply exact policy ", " Cancel "}).SetDoneFunc(func(index int, _ string) {
				a.pages.RemovePage(pageBackupCopyRetention)
				if index == 0 {
					a.pruneBackupCopyNow(job, candidates)
					return
				}
				a.showBackupCopies("")
			})
			modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
			a.pages.AddPage(pageBackupCopyRetention, modal, true, true)
		})
	}()
}

func (a *App) pruneBackupCopyNow(job backupcore.CopyJob, candidates []backupcore.CopyRetentionCandidate) {
	ctx, cancel := context.WithCancel(context.Background())
	token := a.showLoadingModal("Reverifying and applying copy retention...", withLoadingCancelOutcome("Press Esc to stop before the next artifact; already-pruned files stay deleted.", cancel))
	go func() {
		removed, volumeWarnings, err := backupcore.ApplyCopyRetentionPlanWithVolume(ctx, a.backupStore, job, time.Now(), candidates)
		cancel()
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				return
			}
			a.showBackupCopies("")
			if err != nil {
				detail := ""
				if len(removed) > 0 {
					detail = fmt.Sprintf("\n\n%d verified artifact(s) were removed before the error.", len(removed))
				}
				if len(volumeWarnings) > 0 {
					detail += "\n\nVolume warnings:\n• " + strings.Join(volumeWarnings, "\n• ")
				}
				a.ShowAlert(fmt.Sprintf("%s Copy retention stopped:\n\n%v%s", iconFail, err, detail), pageBackupCopies)
				return
			}
			warningText := ""
			if len(volumeWarnings) > 0 {
				warningText = "\n\nVolume warnings:\n• " + strings.Join(volumeWarnings, "\n• ")
			}
			a.ShowAlert(fmt.Sprintf("%s Copy retention complete\n\nRemoved %d verified recovery point(s). The newest verified recovery point was retained.%s", iconSuccess, len(removed), warningText), pageBackupCopies)
		})
	}()
}

func (a *App) runBackupCopyNow(jobID string) {
	ctx, cancel := context.WithCancel(context.Background())
	var canceled atomic.Bool
	const cancelText = "Press Esc to cancel safely; completed copies remain immutable."
	title := fmt.Sprintf("%s Running copy job...", iconBackup)
	token := a.showLoadingModal(title, withLoadingCancelOutcome(cancelText, func() {
		canceled.Store(true)
		cancel()
	}))
	go func() {
		started := time.Now()
		var lastProgress atomic.Value
		lastProgress.Store(backupcore.ProgressEvent{Phase: "copy", Message: "preparing copy"})
		run, err := backupcore.RunCopyJobNowWithProgress(ctx, a.backupStore, jobID, func(event backupcore.ProgressEvent) {
			if event.Elapsed <= 0 {
				event.Elapsed = time.Since(started)
			}
			lastProgress.Store(event)
			a.updateBackupProgress(token, title, event, cancelText)
		})
		cancel()
		a.app.QueueUpdateDraw(func() {
			if !a.finishLoadingModal(token) {
				return
			}
			a.backupCenterSelectedCopy = jobID
			a.showBackupCopies("")
			if canceled.Load() && (run.Status == backupcore.RunCanceled || errors.Is(err, context.Canceled)) {
				a.ShowAlert(fmt.Sprintf("%s Copy canceled. Completed immutable artifacts, if any, remain recorded; no partial is recovery-ready.", iconWarn), pageBackupCopies)
				return
			}
			if err != nil {
				last := lastProgress.Load().(backupcore.ProgressEvent)
				preserved := ""
				if len(run.Artifacts) > 0 {
					preserved = fmt.Sprintf("\n\n%d completed immutable copy artifact(s) from this batch remain recorded.", len(run.Artifacts))
				}
				a.ShowAlert(fmt.Sprintf("%s Copy failed:\n\n%s\n\nLast phase: %s — %s%s", iconFail, tview.Escape(err.Error()), tview.Escape(nonEmptyOr(last.Phase, "unknown")), tview.Escape(nonEmptyOr(last.Message, "no progress detail")), preserved), pageBackupCopies)
				return
			}
			bytesCopied := run.BytesCopied
			if bytesCopied < 0 {
				bytesCopied = 0
			}
			result := fmt.Sprintf("Discovered: %d\nAlready present: %d\nNew verified copies: %d\nBytes copied: %s\nVerification: %s", run.Discovered, run.AlreadyPresent, len(run.Artifacts), backupByteSize(uint64(bytesCopied)), run.RequiredVerification)
			if len(run.Warnings) > 0 || strings.TrimSpace(run.RetentionError) != "" {
				result += "\nWarnings: " + copyWarningsLabel(backupcore.CopyJob{}, run, true, time.Now())
			}
			a.ShowAlert(fmt.Sprintf("%s Copy run complete\n\n%s", iconSuccess, tview.Escape(result)), pageBackupCopies)
		})
	}()
}

func (a *App) confirmDeleteBackupCopy(job backupcore.CopyJob) {
	modal := tview.NewModal().SetText(fmt.Sprintf("%s Delete copy job [yellow]%s[-]?\n\nRecorded history and copied backup files stay untouched.", iconWarn, tview.Escape(job.Name))).
		AddButtons([]string{" Delete job ", " Cancel "}).SetDoneFunc(func(index int, _ string) {
		a.pages.RemovePage("deleteBackupCopy")
		if index == 0 {
			if err := a.backupStore.DeleteCopyJob(context.Background(), job.ID); err != nil {
				a.ShowAlert(fmt.Sprintf("%s Could not delete copy job:\n\n%v", iconWarn, err), pageBackupCopies)
				return
			}
			if a.backupCenterSelectedCopy == job.ID {
				a.backupCenterSelectedCopy = ""
			}
		}
		a.showBackupCopies("")
	})
	modal.SetBackgroundColor(bg).SetButtonBackgroundColor(surface1).SetButtonTextColor(green).SetTextColor(text)
	a.pages.AddPage("deleteBackupCopy", modal, true, true)
}

func copyTopologyIndex(job backupcore.CopyJob) int {
	switch {
	case job.Source.Kind == backupcore.CopyEndpointLocal && job.Destination.Kind == backupcore.CopyEndpointLocal:
		return copyTopologyLocalLocal
	case job.Source.Kind == backupcore.CopyEndpointLocal && (job.Destination.Kind == backupcore.CopyEndpointSFTP || job.Destination.Kind == backupcore.CopyEndpointSSH) && job.SourceBackupJobID != "":
		return copyTopologyBackupSFTP
	case job.Source.Kind == backupcore.CopyEndpointLocal && (job.Destination.Kind == backupcore.CopyEndpointSFTP || job.Destination.Kind == backupcore.CopyEndpointSSH):
		return copyTopologyLocalSFTP
	case (job.Source.Kind == backupcore.CopyEndpointSFTP || job.Source.Kind == backupcore.CopyEndpointSSH) && job.Destination.Kind == backupcore.CopyEndpointLocal:
		return copyTopologySFTPLocal
	case job.Source.Kind == backupcore.CopyEndpointRclone && job.Destination.Kind == backupcore.CopyEndpointLocal:
		return copyTopologyRcloneLocal
	default:
		return -1
	}
}

func copyTriggerOptions(topology int) ([]string, []backupcore.CopyTrigger) {
	labels := []string{"Run manually", "At chosen times"}
	values := []backupcore.CopyTrigger{backupcore.CopyTriggerManual, backupcore.CopyTriggerTimed}
	if topology == copyTopologyBackupSFTP {
		labels = append(labels, "After each successful backup")
		values = append(values, backupcore.CopyTriggerAfterSuccess)
	}
	return labels, values
}

func copyTriggerIndex(values []backupcore.CopyTrigger, value backupcore.CopyTrigger) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}

func copyTimedScheduleIndex(kind backupcore.ScheduleKind) int {
	switch kind {
	case backupcore.ScheduleInterval:
		return 0
	case backupcore.ScheduleWeekly:
		return 2
	default:
		return 1
	}
}

func (a *App) showBackupCopyForm(existing *backupcore.CopyJob) *tview.Form {
	returnFocus := a.app.GetFocus()
	backupJobs, err := a.backupStore.ListJobs(context.Background())
	if err != nil {
		a.ShowAlert(fmt.Sprintf("%s Could not load backup plans:\n\n%v", iconWarn, err), pageBackupCopies)
		return nil
	}
	home, _ := os.UserHomeDir()
	job := backupcore.CopyJob{
		Name: "Local vault copy", Mode: backupcore.CopyModePush, Trigger: backupcore.CopyTriggerManual,
		Source:       backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(home, "dbterm-backups")},
		Destination:  backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(home, "dbterm-copy-vault")},
		Schedule:     backupcore.Schedule{Kind: backupcore.ScheduleManual, TimeOfDay: "02:30", TimesOfDay: []string{"02:30"}, Timezone: "Local", RunMissedOnWake: true},
		Verification: backupcore.CopyVerificationSHA256Format, Retention: backupcore.Retention{KeepLast: 14}, TimeoutMinutes: backupcore.DefaultTimeoutMinutes,
	}
	topology := copyTopologyLocalLocal
	if existing != nil {
		job = *existing
		topology = copyTopologyIndex(job)
		if topology < 0 {
			a.ShowAlert(fmt.Sprintf("%s This copy topology cannot be edited in Backup Center yet.\n\n%s\n\nNo settings were changed.", iconWarn, copyTransportLabel(job)), pageBackupCopies)
			return nil
		}
	}
	if job.Schedule.Timezone == "" {
		job.Schedule.Timezone = "Local"
	}
	if job.Schedule.TimeOfDay == "" && len(job.Schedule.TimesOfDay) == 0 {
		job.Schedule.TimeOfDay = "02:30"
		job.Schedule.TimesOfDay = []string{"02:30"}
	}
	if job.Verification == "" {
		job.Verification = backupcore.CopyVerificationSHA256Format
	}
	if job.TimeoutMinutes <= 0 {
		job.TimeoutMinutes = backupcore.DefaultTimeoutMinutes
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
	if len(job.Schedule.Weekdays) == 0 {
		job.Schedule.Weekdays = []int{int(time.Monday)}
	}
	draft := backupCopyFormDraft{
		job: job, topology: topology,
		producerFilter: job.ArtifactFilter.ProducerID, jobFilter: job.ArtifactFilter.JobID,
		everyMinutes: strconv.Itoa(max(5, job.Schedule.EveryMinutes)), wallClockTimes: backupScheduleTimesInput(job.Schedule),
		weekdays: weekdayText(job.Schedule.Weekdays), timezone: nonEmptyOr(job.Schedule.Timezone, "Local"),
		freshnessMinutes: strconv.Itoa(job.ExpectedFreshnessMinutes), keepLatest: strconv.Itoa(job.Retention.KeepLast), timeoutMinutes: strconv.Itoa(job.TimeoutMinutes),
		volumeSentinelFile: ".dbterm-volume-id", volumeWarmupSeconds: "0", volumeCooldownSeconds: "0",
		smtpPort: strconv.Itoa(job.Notification.SMTPPort), recipients: strings.Join(job.Notification.Recipients, ", "),
	}
	if job.DestinationVolume != nil {
		draft.volumeMode = job.DestinationVolume.Mode
		draft.volumeMountPoint = job.DestinationVolume.MountPoint
		draft.volumeSentinelFile = nonEmptyOr(job.DestinationVolume.SentinelFile, ".dbterm-volume-id")
		draft.volumeIdentity = job.DestinationVolume.SentinelValue
		draft.volumeUUID = job.DestinationVolume.FilesystemUUID
		draft.volumeFilesystem = job.DestinationVolume.ExpectedFilesystem
		draft.volumeLabel = job.DestinationVolume.ExpectedVolumeLabel
		draft.volumeMountOptions = strings.Join(job.DestinationVolume.MountOptions, ",")
		draft.volumeWarmupSeconds = strconv.Itoa(job.DestinationVolume.WarmupSeconds)
		draft.volumeCooldownSeconds = strconv.Itoa(job.DestinationVolume.CooldownSeconds)
		draft.volumeSpindown = job.DestinationVolume.Spindown
	}
	if job.Source.Kind == backupcore.CopyEndpointLocal {
		draft.localSource = job.Source.Location
	}
	if job.Destination.Kind == backupcore.CopyEndpointLocal {
		draft.localDestination = job.Destination.Location
	}
	if job.Source.Kind == backupcore.CopyEndpointSFTP || job.Source.Kind == backupcore.CopyEndpointSSH {
		draft.sftpLocation, draft.identityPath, draft.pinnedHostKey = job.Source.Location, job.Source.CredentialRef, job.Source.PinnedHostKey
		draft.sftpKind = job.Source.Kind
	}
	if job.Destination.Kind == backupcore.CopyEndpointSFTP || job.Destination.Kind == backupcore.CopyEndpointSSH {
		draft.sftpLocation, draft.identityPath, draft.pinnedHostKey = job.Destination.Location, job.Destination.CredentialRef, job.Destination.PinnedHostKey
		draft.sftpKind = job.Destination.Kind
	}
	if draft.sftpKind == "" {
		draft.sftpKind = backupcore.CopyEndpointSFTP
	}
	if job.Source.Kind == backupcore.CopyEndpointRclone {
		draft.rcloneSource = job.Source.Location
	}
	draft.backupJobID = job.SourceBackupJobID
	if draft.backupJobID == "" && len(backupJobs) > 0 {
		draft.backupJobID = backupJobs[0].ID
	}

	backupLabels := make([]string, len(backupJobs))
	for index, backupJob := range backupJobs {
		backupLabels[index] = backupJob.Name
	}
	backupIndex := func() int {
		for index, backupJob := range backupJobs {
			if backupJob.ID == draft.backupJobID {
				return index
			}
		}
		return 0
	}
	container := tview.NewFlex().SetDirection(tview.FlexRow)
	var renderForm func(string)
	var currentForm *tview.Form
	closeForm := func() {
		a.pages.RemovePage(pageBackupCopyForm)
		a.pages.ShowPage(pageBackupCopies)
		if returnFocus != nil {
			a.app.SetFocus(returnFocus)
		}
	}
	testEmail := func() {
		notification, notificationErr := backupCopyNotificationFromDraft(&draft)
		if notificationErr != nil {
			a.ShowAlert(fmt.Sprintf("%s Email settings are incomplete:\n\n%v", iconWarn, notificationErr), pageBackupCopyForm)
			return
		}
		ctx, cancel := context.WithCancel(context.Background())
		const cancelText = "Press Esc to cancel the SMTP test. No copy or backup is created or changed."
		title := fmt.Sprintf("Testing SMTP delivery through %s:%d...", notification.SMTPHost, notification.SMTPPort)
		token := a.showLoadingModal(title, withLoadingCancelOutcome(cancelText, cancel))
		go func() {
			testErr := backupcore.TestEmailNotification(ctx, notification)
			cancel()
			a.app.QueueUpdateDraw(func() {
				if !a.finishLoadingModal(token) {
					return
				}
				if testErr != nil {
					a.ShowAlert(fmt.Sprintf("%s Test email failed:\n\n%v\n\nThe password is never included in this diagnostic.", iconFail, testErr), pageBackupCopyForm)
					return
				}
				a.ShowAlert(fmt.Sprintf("%s Test email sent. Check the configured recipient inbox and spam folder. No copy or backup was run or modified.", iconSuccess), pageBackupCopyForm)
			})
		}()
	}

	buildCandidate := func() (backupcore.CopyJob, error) {
		candidate := draft.job
		candidate.Name = strings.TrimSpace(candidate.Name)
		candidate.SourceBackupJobID = ""
		candidate.ArtifactFilter = backupcore.CopyArtifactFilter{ProducerID: strings.TrimSpace(draft.producerFilter), JobID: strings.TrimSpace(draft.jobFilter)}
		candidate.Source = backupcore.CopyEndpoint{}
		candidate.Destination = backupcore.CopyEndpoint{}
		candidate.DestinationVolume = nil
		sftp := backupcore.CopyEndpoint{Kind: draft.sftpKind, Location: strings.TrimSpace(draft.sftpLocation), CredentialRef: strings.TrimSpace(draft.identityPath), PinnedHostKey: strings.TrimSpace(draft.pinnedHostKey)}
		switch draft.topology {
		case copyTopologyLocalLocal:
			candidate.Mode = backupcore.CopyModePush
			candidate.Source = backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: draft.localSource}
			candidate.Destination = backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: draft.localDestination}
		case copyTopologyLocalSFTP:
			candidate.Mode = backupcore.CopyModePush
			candidate.Source = backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: draft.localSource}
			candidate.Destination = sftp
		case copyTopologyBackupSFTP:
			if len(backupJobs) == 0 || strings.TrimSpace(draft.backupJobID) == "" {
				return backupcore.CopyJob{}, fmt.Errorf("create a backup plan before binding an after-success SFTP copy")
			}
			candidate.Mode = backupcore.CopyModePush
			candidate.Source = backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal}
			candidate.SourceBackupJobID = draft.backupJobID
			candidate.ArtifactFilter.JobID = draft.backupJobID
			candidate.Destination = sftp
		case copyTopologySFTPLocal:
			candidate.Mode = backupcore.CopyModePull
			candidate.Source = sftp
			candidate.Destination = backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: draft.localDestination}
		case copyTopologyRcloneLocal:
			candidate.Mode = backupcore.CopyModePull
			candidate.Source = backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointRclone, Location: strings.TrimSpace(draft.rcloneSource)}
			candidate.Destination = backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: draft.localDestination}
		default:
			return backupcore.CopyJob{}, fmt.Errorf("choose a supported copy topology")
		}

		candidate.ExpectedFreshnessMinutes, err = parseBackupFormInt("Expected freshness minutes", draft.freshnessMinutes, 0, 5*365*24*60)
		if err != nil {
			return backupcore.CopyJob{}, err
		}
		candidate.Retention.KeepLast, err = parseBackupFormInt("Keep latest", draft.keepLatest, 0, math.MaxInt)
		if err != nil {
			return backupcore.CopyJob{}, err
		}
		candidate.TimeoutMinutes, err = parseBackupFormInt("Timeout minutes", draft.timeoutMinutes, 1, 24*60)
		if err != nil {
			return backupcore.CopyJob{}, err
		}
		candidate.Notification, err = backupCopyNotificationFromDraft(&draft)
		if err != nil {
			return backupcore.CopyJob{}, err
		}
		if candidate.Trigger == backupcore.CopyTriggerManual {
			candidate.Enabled = false
			candidate.Schedule.Kind = backupcore.ScheduleManual
			candidate.NextRunAt = time.Time{}
		} else if candidate.Trigger == backupcore.CopyTriggerAfterSuccess {
			candidate.Schedule.Kind = backupcore.ScheduleManual
			candidate.NextRunAt = time.Time{}
		} else {
			candidate.Trigger = backupcore.CopyTriggerTimed
			candidate.Schedule.Timezone = strings.TrimSpace(draft.timezone)
			if candidate.Schedule.Kind == backupcore.ScheduleDaily || candidate.Schedule.Kind == backupcore.ScheduleWeekly {
				times, timesErr := parseBackupScheduleTimes(draft.wallClockTimes)
				if timesErr != nil {
					return backupcore.CopyJob{}, timesErr
				}
				candidate.Schedule.TimesOfDay = times
				candidate.Schedule.TimeOfDay = times[0]
			}
			if candidate.Schedule.Kind == backupcore.ScheduleInterval {
				candidate.Schedule.EveryMinutes, err = parseBackupFormInt("Every minutes", draft.everyMinutes, 1, math.MaxInt)
				if err != nil {
					return backupcore.CopyJob{}, err
				}
			}
			if candidate.Schedule.Kind == backupcore.ScheduleWeekly {
				candidate.Schedule.Weekdays, err = parseWeekdays(draft.weekdays)
				if err != nil {
					return backupcore.CopyJob{}, err
				}
			}
			candidate.NextRunAt = time.Time{}
		}
		for _, endpoint := range []*backupcore.CopyEndpoint{&candidate.Source, &candidate.Destination} {
			if endpoint.Kind == backupcore.CopyEndpointSFTP || endpoint.Kind == backupcore.CopyEndpointSSH {
				expandedIdentity, identityErr := expandHomePath(endpoint.CredentialRef)
				if identityErr != nil {
					return backupcore.CopyJob{}, fmt.Errorf("invalid private identity path: %w", identityErr)
				}
				endpoint.CredentialRef = filepath.Clean(expandedIdentity)
			}
			if endpoint.Kind != backupcore.CopyEndpointLocal || strings.TrimSpace(endpoint.Location) == "" {
				continue
			}
			expanded, pathErr := expandHomePath(endpoint.Location)
			if pathErr != nil {
				return backupcore.CopyJob{}, pathErr
			}
			absolute, pathErr := filepath.Abs(expanded)
			if pathErr != nil {
				return backupcore.CopyJob{}, pathErr
			}
			endpoint.Location = filepath.Clean(absolute)
		}
		if candidate.Destination.Kind == backupcore.CopyEndpointLocal && draft.volumeMode != "" {
			if strings.TrimSpace(draft.volumeMountPoint) == "" || strings.TrimSpace(draft.volumeIdentity) == "" {
				return backupcore.CopyJob{}, fmt.Errorf("destination volume mount point and volume identity are required")
			}
			expandedMountPoint, pathErr := expandHomePath(strings.TrimSpace(draft.volumeMountPoint))
			if pathErr != nil {
				return backupcore.CopyJob{}, fmt.Errorf("invalid destination volume mount point: %w", pathErr)
			}
			absoluteMountPoint, pathErr := filepath.Abs(expandedMountPoint)
			if pathErr != nil {
				return backupcore.CopyJob{}, fmt.Errorf("resolve destination volume mount point: %w", pathErr)
			}
			volume := backupcore.CopyDestinationVolume{
				Mode: draft.volumeMode, MountPoint: filepath.Clean(absoluteMountPoint),
				SentinelFile: strings.TrimSpace(draft.volumeSentinelFile), SentinelValue: strings.TrimSpace(draft.volumeIdentity),
			}
			if draft.volumeMode == backupcore.CopyVolumeManagedLinuxBlockDevice {
				volume.FilesystemUUID = strings.TrimSpace(draft.volumeUUID)
				volume.ExpectedFilesystem = strings.TrimSpace(draft.volumeFilesystem)
				volume.ExpectedVolumeLabel = strings.TrimSpace(draft.volumeLabel)
				volume.MountOptions = splitBackupCopyMountOptions(draft.volumeMountOptions)
				volume.WarmupSeconds, err = parseBackupFormInt("Volume warmup seconds", draft.volumeWarmupSeconds, 0, 3600)
				if err != nil {
					return backupcore.CopyJob{}, err
				}
				volume.CooldownSeconds, err = parseBackupFormInt("Volume cooldown seconds", draft.volumeCooldownSeconds, 0, 3600)
				if err != nil {
					return backupcore.CopyJob{}, err
				}
				volume.Spindown = draft.volumeSpindown
			}
			candidate.DestinationVolume = &volume
		}
		if candidate.Destination.Kind == backupcore.CopyEndpointLocal && candidate.DestinationVolume == nil {
			if err := os.MkdirAll(candidate.Destination.Location, 0o700); err != nil {
				return backupcore.CopyJob{}, fmt.Errorf("create local copy destination: %w", err)
			}
		}
		return candidate, nil
	}

	save := func() {
		candidate, err := buildCandidate()
		if err != nil {
			a.ShowAlert(fmt.Sprintf("%s Copy settings are incomplete:\n\n%v", iconWarn, err), pageBackupCopyForm)
			return
		}
		if err := a.backupStore.UpsertCopyJob(context.Background(), &candidate); err != nil {
			a.ShowAlert(fmt.Sprintf("%s Could not save copy job:\n\n%v", iconWarn, err), pageBackupCopyForm)
			return
		}
		a.backupCenterSelectedCopy = candidate.ID
		a.pages.RemovePage(pageBackupCopyForm)
		a.showBackupCopies("")
		if candidate.Enabled && candidate.Trigger != backupcore.CopyTriggerManual {
			a.offerBackupAgentStart()
		}
	}

	w, h := a.modalSize(84, 116, 24, 38)
	footer := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	footer.SetBackgroundColor(crust)
	footer.SetText(footerTextThatFits(w,
		" [yellow]Tab / Shift+Tab[-] Move · [yellow]Enter[-] Choose/save · [yellow]Esc[-] Cancel · Credentials stay as identity-file references ",
		" [yellow]Tab[-] Move · [yellow]Enter[-] Choose/save · [yellow]Esc[-] Cancel ",
		" [yellow]Tab[-] Move · [yellow]Esc[-] Cancel ",
	))
	renderForm = func(focusLabel string) {
		form := tview.NewForm()
		currentForm = form
		title := " Add Copy "
		if existing != nil {
			title = " Copy Settings "
		}
		form.SetBorder(true).SetTitle(title).SetTitleColor(mauve).SetBorderColor(surface1)
		form.SetBackgroundColor(bg)
		form.SetItemPadding(0)
		form.SetFieldBackgroundColor(mantle).SetFieldTextColor(text).SetLabelColor(text).SetButtonBackgroundColor(surface1).SetButtonTextColor(green)
		addBackupFormSection(form, "TOPOLOGY", "Moves completed artifacts; never creates a database dump")
		form.AddInputField("Copy Name", draft.job.Name, 36, nil, func(value string) { draft.job.Name = value })
		form.AddDropDown("Route", backupCopyTopologyOptions, draft.topology, func(_ string, index int) {
			if index < 0 || index >= len(backupCopyTopologyOptions) || index == draft.topology {
				return
			}
			draft.topology = index
			triggers, _ := copyTriggerOptions(index)
			_ = triggers
			if draft.job.Trigger == backupcore.CopyTriggerAfterSuccess && index != copyTopologyBackupSFTP {
				draft.job.Trigger = backupcore.CopyTriggerManual
			}
			renderForm("Route")
		})
		switch draft.topology {
		case copyTopologyLocalLocal:
			form.AddInputField("Source Folder", draft.localSource, 44, nil, func(value string) { draft.localSource = value })
			form.AddInputField("Destination Folder", draft.localDestination, 44, nil, func(value string) { draft.localDestination = value })
		case copyTopologyLocalSFTP:
			form.AddInputField("Source Folder", draft.localSource, 44, nil, func(value string) { draft.localSource = value })
			addBackupCopySFTPFields(form, &draft)
		case copyTopologyBackupSFTP:
			if len(backupJobs) == 0 {
				form.AddTextView("Backup Plan", "[#f9e2af]No backup plans exist. Create one before saving this route.[-]", 0, 1, true, false)
			} else {
				form.AddDropDown("Backup Plan", backupLabels, backupIndex(), func(_ string, index int) {
					if index >= 0 && index < len(backupJobs) {
						draft.backupJobID = backupJobs[index].ID
					}
				})
			}
			addBackupCopySFTPFields(form, &draft)
		case copyTopologySFTPLocal:
			addBackupCopySFTPFields(form, &draft)
			form.AddInputField("Destination Folder", draft.localDestination, 44, nil, func(value string) { draft.localDestination = value })
		case copyTopologyRcloneLocal:
			form.AddInputField("rclone Source", draft.rcloneSource, 44, nil, func(value string) { draft.rcloneSource = value })
			form.AddTextView("rclone Help", "[#a6adc8]Use rclone://remote/path. Pull is supported; rclone push is not offered.[-]", 0, 1, true, false)
			form.AddInputField("Destination Folder", draft.localDestination, 44, nil, func(value string) { draft.localDestination = value })
		}
		if backupCopyHasLocalDestination(draft.topology) {
			addBackupFormSection(form, "DESTINATION VOLUME", "Optional positive identity prevents writes to a missing mount")
			form.AddDropDown("Destination Volume", backupCopyVolumeModeLabels, backupCopyVolumeModeIndex(draft.volumeMode), func(_ string, index int) {
				if index < 0 || index >= len(backupCopyVolumeModeValues) || backupCopyVolumeModeValues[index] == draft.volumeMode {
					return
				}
				draft.volumeMode = backupCopyVolumeModeValues[index]
				renderForm("Destination Volume")
			})
			if draft.volumeMode == "" {
				form.AddTextView("Volume Safety", "[#a6adc8]dbterm treats the destination as an ordinary local folder and does not mount or unmount it.[-]", 0, 2, true, false)
			} else {
				form.AddInputField("Volume Mount Point", draft.volumeMountPoint, 44, nil, func(value string) { draft.volumeMountPoint = value })
				form.AddInputField("Sentinel File", draft.volumeSentinelFile, 30, nil, func(value string) { draft.volumeSentinelFile = value })
				form.AddInputField("Volume Identity", draft.volumeIdentity, 40, nil, func(value string) { draft.volumeIdentity = value })
				if draft.volumeMode == backupcore.CopyVolumeManagedLinuxBlockDevice {
					form.AddInputField("Filesystem UUID", draft.volumeUUID, 40, nil, func(value string) { draft.volumeUUID = value })
					form.AddInputField("Filesystem Type", draft.volumeFilesystem, 20, nil, func(value string) { draft.volumeFilesystem = value })
					form.AddInputField("Volume Label (optional)", draft.volumeLabel, 30, nil, func(value string) { draft.volumeLabel = value })
					form.AddInputField("Mount Options (comma-separated)", draft.volumeMountOptions, 44, nil, func(value string) { draft.volumeMountOptions = value })
					form.AddInputField("Warmup Seconds", draft.volumeWarmupSeconds, 8, func(value string, _ rune) bool { return digitsOnly(value) }, func(value string) { draft.volumeWarmupSeconds = value })
					form.AddInputField("Cooldown Seconds", draft.volumeCooldownSeconds, 8, func(value string, _ rune) bool { return digitsOnly(value) }, func(value string) { draft.volumeCooldownSeconds = value })
					form.AddCheckbox("Spin Down After Copy", draft.volumeSpindown, func(value bool) { draft.volumeSpindown = value })
					form.AddTextView("Managed Disk Safety", "[#f9e2af]Linux only. Requires UUID/type match and narrow host privileges. dbterm never formats or repairs disks; unmount happens only after an owned mount.[-]", 0, 3, true, false)
				} else {
					form.AddTextView("Mounted Volume Safety", "[#a6adc8]Verify-only: dbterm requires the exact sentinel and destination containment, and never mounts, unmounts, or spins down this volume.[-]", 0, 3, true, false)
				}
			}
		}

		addBackupFormSection(form, "WHEN & PROOF", "Test is read-only; one successful real transfer unlocks automation")
		triggerLabels, triggerValues := copyTriggerOptions(draft.topology)
		form.AddDropDown("Trigger", triggerLabels, copyTriggerIndex(triggerValues, draft.job.Trigger), func(_ string, index int) {
			if index < 0 || index >= len(triggerValues) || triggerValues[index] == draft.job.Trigger {
				return
			}
			draft.job.Trigger = triggerValues[index]
			if draft.job.Trigger == backupcore.CopyTriggerTimed && (draft.job.Schedule.Kind == "" || draft.job.Schedule.Kind == backupcore.ScheduleManual) {
				draft.job.Schedule.Kind = backupcore.ScheduleDaily
			}
			renderForm("Trigger")
		})
		if draft.job.Trigger == backupcore.CopyTriggerTimed {
			scheduleLabels := []string{"Every few minutes", "Every day", "Specific weekdays"}
			form.AddDropDown("Schedule", scheduleLabels, copyTimedScheduleIndex(draft.job.Schedule.Kind), func(_ string, index int) {
				if index < 0 || index >= 3 {
					return
				}
				kind := []backupcore.ScheduleKind{backupcore.ScheduleInterval, backupcore.ScheduleDaily, backupcore.ScheduleWeekly}[index]
				if kind != draft.job.Schedule.Kind {
					draft.job.Schedule.Kind = kind
					renderForm("Schedule")
				}
			})
			switch draft.job.Schedule.Kind {
			case backupcore.ScheduleInterval:
				form.AddInputField("Every Minutes", draft.everyMinutes, 8, func(value string, _ rune) bool { return digitsOnly(value) }, func(value string) { draft.everyMinutes = value })
			case backupcore.ScheduleWeekly:
				form.AddInputField("Weekdays", draft.weekdays, 34, nil, func(value string) { draft.weekdays = value })
				form.AddInputField("Run At (comma-separated HH:MM)", draft.wallClockTimes, 28, nil, func(value string) { draft.wallClockTimes = value })
			default:
				form.AddInputField("Run At (comma-separated HH:MM)", draft.wallClockTimes, 28, nil, func(value string) { draft.wallClockTimes = value })
			}
			form.AddInputField("Timezone", draft.timezone, 30, nil, func(value string) { draft.timezone = value })
			form.AddCheckbox("Enable Timed Copy", draft.job.Enabled, func(value bool) { draft.job.Enabled = value })
		} else if draft.job.Trigger == backupcore.CopyTriggerAfterSuccess {
			form.AddCheckbox("Enable After-success Copy", draft.job.Enabled, func(value bool) { draft.job.Enabled = value })
		}
		if draft.job.Trigger != backupcore.CopyTriggerManual {
			proofMessage := "[#f9e2af]No real transfer is proven yet. Save disabled, press R to transfer and verify an artifact, then enable. T only tests access and does not unlock automation.[-]"
			if draft.job.HasCurrentTransferProof() {
				proofMessage = fmt.Sprintf("[#a6e3a1]Real transfer proven %s.[-] [#a6adc8]Changing source, destination, volume, filters, or verification requires another manual run.[-]", tview.Escape(draft.job.TransferProofAt.Local().Format("Jan 02 15:04 MST")))
			}
			form.AddTextView("Automatic Safety", proofMessage, 0, 3, true, false)
		}
		verificationLabels := []string{"SHA-256 + format (recommended)", "SHA-256"}
		verificationValues := []backupcore.CopyVerificationStrength{backupcore.CopyVerificationSHA256Format, backupcore.CopyVerificationSHA256}
		verificationIndex := 0
		for index, value := range verificationValues {
			if value == draft.job.Verification {
				verificationIndex = index
			}
		}
		form.AddDropDown("Verification", verificationLabels, verificationIndex, func(_ string, index int) {
			if index >= 0 && index < len(verificationValues) {
				draft.job.Verification = verificationValues[index]
			}
		})
		form.AddInputField("Expected Freshness Minutes (0 = off)", draft.freshnessMinutes, 10, func(value string, _ rune) bool { return digitsOnly(value) }, func(value string) { draft.freshnessMinutes = value })

		addBackupFormSection(form, "FILTER & RETENTION", "Portable manifest identity decides what is complete")
		if draft.topology != copyTopologyBackupSFTP {
			form.AddInputField("Producer ID Filter (optional)", draft.producerFilter, 34, nil, func(value string) { draft.producerFilter = value })
			form.AddInputField("Source Job ID Filter (optional)", draft.jobFilter, 34, nil, func(value string) { draft.jobFilter = value })
		}
		if draft.topology == copyTopologyLocalLocal || draft.topology == copyTopologySFTPLocal || draft.topology == copyTopologyRcloneLocal || draft.topology == copyTopologyLocalSFTP || draft.topology == copyTopologyBackupSFTP {
			form.AddInputField("Keep Latest", draft.keepLatest, 8, func(value string, _ rune) bool { return digitsOnly(value) }, func(value string) { draft.keepLatest = value })
			if draft.topology == copyTopologyLocalSFTP || draft.topology == copyTopologyBackupSFTP {
				form.AddTextView("Remote Retention", "[#a6adc8]Only exact SFTP artifacts published, recorded, and reverified by this copy job are eligible.[-]", 0, 2, true, false)
			}
		}
		form.AddInputField("Timeout Minutes", draft.timeoutMinutes, 8, func(value string, _ rune) bool { return digitsOnly(value) }, func(value string) { draft.timeoutMinutes = value })
		form.AddTextView("Safety", "[#a6adc8]Sidecar manifest required · immutable final names · partial files never count as recovery-ready.[-]", 0, 2, true, false)

		addBackupFormSection(form, "EMAIL ALERTS", "Copy validity stays independent from SMTP delivery")
		notificationOptions := []string{"Never", "Failures only", "Success only", "Success and failure"}
		form.AddDropDown("Send Email", notificationOptions, backupNotificationIndex(draft.job.Notification.Policy), func(_ string, index int) {
			if index < 0 || index >= len(notificationOptions) {
				return
			}
			policy := []backupcore.NotificationPolicy{backupcore.NotificationNever, backupcore.NotificationFailure, backupcore.NotificationSuccess, backupcore.NotificationBoth}[index]
			if policy != draft.job.Notification.Policy {
				draft.job.Notification.Policy = policy
				renderForm("Send Email")
			}
		})
		if draft.job.Notification.Policy != backupcore.NotificationNever {
			form.AddInputField("SMTP Host", draft.job.Notification.SMTPHost, 34, nil, func(value string) { draft.job.Notification.SMTPHost = value })
			form.AddInputField("SMTP Port", draft.smtpPort, 8, func(value string, _ rune) bool { return digitsOnly(value) }, func(value string) { draft.smtpPort = value })
			tlsOptions := []string{"STARTTLS (recommended)", "Implicit TLS", "None (localhost only)"}
			form.AddDropDown("TLS", tlsOptions, backupTLSIndex(draft.job.Notification.TLSMode), func(_ string, index int) {
				if index >= 0 && index < len(tlsOptions) {
					draft.job.Notification.TLSMode = []backupcore.SMTPTLSMode{backupcore.SMTPTLSStartTLS, backupcore.SMTPTLSImplicit, backupcore.SMTPTLSNone}[index]
				}
			})
			form.AddInputField("Recipients (comma separated)", draft.recipients, 32, nil, func(value string) { draft.recipients = value })
			form.AddInputField("SMTP Username", draft.job.Notification.Username, 34, nil, func(value string) { draft.job.Notification.Username = value })
			form.AddPasswordField("SMTP App Password", draft.job.Notification.Password, 32, '•', func(value string) { draft.job.Notification.Password = value })
			form.AddInputField("From Address", draft.job.Notification.From, 34, nil, func(value string) { draft.job.Notification.From = value })
			form.AddTextView("Email Test", "[#a6adc8]Send a test before saving; no copy or backup is created or changed.[-]", 0, 1, true, false)
		}
		form.AddButton("Save Copy", save)
		if draft.job.Notification.Policy != backupcore.NotificationNever {
			form.AddButton("Send Test Email", testEmail)
		}
		form.AddButton("Cancel", closeForm)
		form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyEscape {
				closeForm()
				return nil
			}
			return event
		})
		container.Clear().AddItem(form, 0, 1, true).AddItem(footer, 1, 0, false)
		if focusLabel != "" {
			setBackupFormFocus(form, focusLabel)
		}
		a.app.SetFocus(form)
	}

	grid := backupModalGrid(container, w, h)
	a.pages.AddPage(pageBackupCopyForm, grid, true, true)
	renderForm("")
	return currentForm
}

func backupCopyNotificationFromDraft(draft *backupCopyFormDraft) (backupcore.EmailNotification, error) {
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

func splitBackupCopyMountOptions(value string) []string {
	parts := strings.Split(value, ",")
	options := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			options = append(options, part)
		}
	}
	return options
}

func addBackupCopySFTPFields(form *tview.Form, draft *backupCopyFormDraft) {
	form.AddInputField("SFTP Location", draft.sftpLocation, 44, nil, func(value string) { draft.sftpLocation = value })
	form.AddInputField("Private Identity File", draft.identityPath, 44, nil, func(value string) { draft.identityPath = value })
	form.AddInputField("Pinned Host Key (SHA256:...)", draft.pinnedHostKey, 44, nil, func(value string) { draft.pinnedHostKey = value })
	form.AddTextView("SFTP Help", "[#a6adc8]Use sftp://service-user@host/absolute/path. Password URLs and unpinned hosts are refused.[-]", 0, 2, true, false)
}
