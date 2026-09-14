package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
)

type backupCenterLayout struct {
	*tview.Flex
	header, detail, footer *tview.TextView
}

type backupAuxLayout struct {
	*tview.Flex
	footer *tview.TextView
	hints  []string
}

func (layout *backupAuxLayout) Draw(screen tcell.Screen) {
	_, _, width, _ := layout.GetRect()
	layout.footer.SetText(footerTextThatFits(width, layout.hints...))
	layout.Flex.Draw(screen)
}

func (layout *backupCenterLayout) Draw(screen tcell.Screen) {
	_, _, width, height := layout.GetRect()
	detailHeight := min(13, max(4, height-14))
	layout.ResizeItem(layout.detail, detailHeight, 0)
	layout.footer.SetText(backupCenterFooterText(width))
	layout.Flex.Draw(screen)
}

func backupOverviewText(jobs []backupcore.Job, latest map[string]backupcore.Run, verified map[string]backupcore.Run, copies []backupcore.CopyJob, latestCopies map[string]backupcore.CopyRun, status backupcore.AgentStatus, now time.Time) string {
	attention, scheduled, copyAttention := 0, 0, 0
	var newest time.Time
	var next time.Time
	nextName := ""
	considerNext := func(at time.Time, name string) {
		if !at.IsZero() && (next.IsZero() || at.Before(next)) {
			next, nextName = at, name
		}
	}
	for _, job := range jobs {
		if run, ok := verified[job.ID]; ok && run.FinishedAt.After(newest) {
			newest = run.FinishedAt
		}
		if run, ok := latest[job.ID]; ok && (run.Status == backupcore.RunFailed || run.Status == backupcore.RunCanceled || run.RetentionError != "" || run.NotificationError != "") {
			attention++
		}
		if job.Enabled && job.Schedule.Kind != backupcore.ScheduleManual {
			scheduled++
			considerNext(job.NextRunAt, job.Name)
		}
	}
	for _, job := range copies {
		run, found := latestCopies[job.ID]
		if !found || copyWarningsLabel(job, run, found, now) != "none recorded" {
			copyAttention++
		}
		if job.Enabled && job.Trigger == backupcore.CopyTriggerTimed {
			scheduled++
			considerNext(job.NextRunAt, job.Name+" (copy)")
		}
	}
	last := "none recorded"
	if !newest.IsZero() {
		last = newest.Local().Format("Jan 02 15:04 MST")
	}
	nextLabel := "no timed runs queued"
	if !next.IsZero() {
		nextLabel = next.Local().Format("Jan 02 15:04 MST") + " · " + nextName
		if !next.After(now) {
			nextLabel = "due · " + nextName
		}
	}
	agent := "agent off"
	if status.Healthy {
		agent = "agent ready"
	} else if scheduled > 0 {
		agent = "agent off · scheduled work needs attention"
	}
	active := "idle"
	if status.Healthy && status.Activity != nil {
		active = status.Activity.JobName + " · " + status.Activity.Phase
	}
	return fmt.Sprintf(" [::b][#cba6f7]BACKUP CENTER[-][-]  %s\n [#89b4fa]BACKUPS[-] %d plans · %d need attention · last verified %s\n [#89b4fa]COPIES[-]  %s · %d need attention (see C)\n [#89b4fa]NOW[-]     %s\n [#89b4fa]NEXT[-]    %s", tview.Escape(agent), len(jobs), attention, tview.Escape(last), copyJobCountLabel(len(copies)), copyAttention, tview.Escape(active), tview.Escape(nextLabel))
}

func filterBackupLogText(content, query string) string {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return content
	}
	var matching []string
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(strings.ToLower(line), query) {
			matching = append(matching, line)
		}
	}
	if len(matching) == 0 {
		return "No matching lines in the bounded log tail."
	}
	return strings.Join(matching, "\n")
}
