package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/config"
	"github.com/shreyam1008/dbterm/internal/appdirs"
)

const (
	defaultAgentPollInterval  = 30 * time.Second
	agentHeartbeatKey         = "agent_heartbeat"
	agentPIDKey               = "agent_pid"
	agentActivityKey          = "agent_activity"
	agentFutureClockTolerance = 5 * time.Second
	agentHealthWindow         = 2*defaultAgentPollInterval + 15*time.Second
)

type AgentActivity struct {
	JobID        string    `json:"job_id"`
	JobName      string    `json:"job_name"`
	RunID        string    `json:"run_id"`
	Phase        string    `json:"phase"`
	Message      string    `json:"message"`
	CurrentBytes int64     `json:"current_bytes,omitempty"`
	TotalBytes   int64     `json:"total_bytes,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type AgentStatus struct {
	Heartbeat time.Time      `json:"heartbeat"`
	PID       int            `json:"pid"`
	Healthy   bool           `json:"healthy"`
	Activity  *AgentActivity `json:"activity,omitempty"`
}

func DefaultStorePath() (string, error) {
	stateDir, err := appdirs.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "backup", "backups.db"), nil
}

func OpenDefaultStore() (*Store, error) {
	path, err := DefaultStorePath()
	if err != nil {
		return nil, err
	}
	return OpenStore(path)
}

func AgentHealth(ctx context.Context, store *Store, now time.Time) (AgentStatus, error) {
	if store == nil {
		return AgentStatus{}, fmt.Errorf("backup store is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	status := AgentStatus{}
	rawHeartbeat, ok, err := store.GetMeta(ctx, agentHeartbeatKey)
	if err != nil {
		return status, err
	}
	if ok {
		status.Heartbeat, _ = time.Parse(time.RFC3339Nano, rawHeartbeat)
	}
	rawPID, ok, err := store.GetMeta(ctx, agentPIDKey)
	if err != nil {
		return status, err
	}
	if ok {
		status.PID, _ = strconv.Atoi(rawPID)
	}
	heartbeatAge := now.Sub(status.Heartbeat)
	status.Healthy = !status.Heartbeat.IsZero() && heartbeatAge >= -agentFutureClockTolerance && heartbeatAge < agentHealthWindow
	if status.Healthy {
		rawActivity, found, activityErr := store.GetMeta(ctx, agentActivityKey)
		if activityErr != nil {
			return status, activityErr
		}
		if found && strings.TrimSpace(rawActivity) != "" {
			var activity AgentActivity
			if json.Unmarshal([]byte(rawActivity), &activity) == nil && activity.RunID != "" {
				activityAge := now.Sub(activity.UpdatedAt)
				if activityAge >= -agentFutureClockTolerance && activityAge < agentHealthWindow {
					status.Activity = &activity
				}
			}
		}
	}
	return status, nil
}

// RunAgent executes scheduled jobs sequentially and relies on the durable
// store lease to prevent overlap with manual invocations or a second agent.
func RunAgent(ctx context.Context, store *Store, pollInterval time.Duration, emit func(string)) error {
	if store == nil {
		return fmt.Errorf("backup store is required")
	}
	agentLock, err := acquireAgentLock(store)
	if err != nil {
		return err
	}
	defer agentLock.release()
	if pollInterval <= 0 {
		pollInterval = defaultAgentPollInterval
	}
	if pollInterval < time.Second {
		return fmt.Errorf("backup agent poll interval must be at least 1 second")
	}
	if err := enableAgentProcessContainment(); err != nil {
		return err
	}
	owner, err := NewID("agent")
	if err != nil {
		return err
	}
	if emit == nil {
		emit = func(string) {}
	}
	clearAgentActivity(store)
	emit(fmt.Sprintf("backup agent started (pid %d, poll %s)", os.Getpid(), pollInterval))
	heartbeatInterval := pollInterval
	if heartbeatInterval > defaultAgentPollInterval {
		heartbeatInterval = defaultAgentPollInterval
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		writeHeartbeat := func() {
			now := time.Now().UTC()
			_ = store.SetMeta(heartbeatCtx, agentHeartbeatKey, now.Format(time.RFC3339Nano))
			_ = store.SetMeta(heartbeatCtx, agentPIDKey, strconv.Itoa(os.Getpid()))
		}
		writeHeartbeat()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				writeHeartbeat()
			}
		}
	}()
	defer func() {
		stopHeartbeat()
		<-heartbeatDone
		// A graceful stop should be visible immediately instead of leaving a
		// healthy-looking PID until the heartbeat timeout elapses. A hard kill
		// still ages out naturally because these writes cannot run.
		_ = store.SetMeta(context.Background(), agentHeartbeatKey, "")
		_ = store.SetMeta(context.Background(), agentPIDKey, "")
		clearAgentActivity(store)
	}()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		now := time.Now().UTC()
		if err := RunDue(ctx, store, owner, now, emit); err != nil && !errors.Is(err, context.Canceled) {
			emit("scheduled backup check failed: " + err.Error())
		}
		select {
		case <-ctx.Done():
			emit("backup agent stopped: " + ctx.Err().Error())
			return nil
		case <-ticker.C:
		}
	}
}

func RunDue(ctx context.Context, store *Store, owner string, now time.Time, emit func(string)) error {
	if emit == nil {
		emit = func(string) {}
	}
	// Keep one fixed reference for this drain. Advancing it after each serial
	// backup would make a job that was due alongside a long-running predecessor
	// look like a missed-on-wake run and could incorrectly skip it.
	cycleNow := now.UTC()
	reconciled, err := store.ReconcileStaleRuns(ctx, cycleNow)
	if err != nil {
		return fmt.Errorf("recover interrupted backup runs: %w", err)
	}
	if reconciled > 0 {
		emit(fmt.Sprintf("recovered %d interrupted backup run(s); review history and rerun them if needed", reconciled))
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		jobs, claimErr := store.claimDueJobsAt(ctx, cycleNow, time.Now().UTC(), owner, 1)
		if claimErr != nil {
			return claimErr
		}
		if len(jobs) == 0 {
			return nil
		}
		job := jobs[0]
		// Reload before every execution. Another long-running job may have
		// occupied the agent for hours, and a connection can be edited or
		// deleted while it waits without a lease.
		connections, loadErr := config.LoadStore()
		if loadErr != nil {
			_ = store.ReleaseJob(ctx, job.ID, owner)
			return fmt.Errorf("load saved connections: %w", loadErr)
		}
		if _, err := executeClaimedJob(ctx, store, connections, job, owner, TriggerScheduled, emit); err != nil {
			emit(fmt.Sprintf("backup %q failed: %v", job.Name, err))
		}
	}
}

func RunJobNow(ctx context.Context, store *Store, idOrName string, emit func(string)) (Run, error) {
	return runJobNow(ctx, store, idOrName, progressFromEmitter(emit))
}

// RunJobNowWithProgress runs a claimed job and reports structured progress for
// clients that want byte counts and elapsed time instead of formatted lines.
func RunJobNowWithProgress(ctx context.Context, store *Store, idOrName string, progress ProgressFunc) (Run, error) {
	return runJobNow(ctx, store, idOrName, progress)
}

func runJobNow(ctx context.Context, store *Store, idOrName string, progress ProgressFunc) (Run, error) {
	if _, err := store.ReconcileStaleRuns(ctx, time.Now().UTC()); err != nil {
		return Run{}, fmt.Errorf("recover interrupted backup runs: %w", err)
	}
	owner, err := NewID("manual")
	if err != nil {
		return Run{}, err
	}
	job, err := store.ClaimJob(ctx, idOrName, owner, time.Now().UTC())
	if err != nil {
		return Run{}, err
	}
	connections, err := config.LoadStore()
	if err != nil {
		_ = store.ReleaseJob(ctx, job.ID, owner)
		return Run{}, err
	}
	return executeClaimedJobWithProgressAndNotifier(ctx, store, connections, job, owner, TriggerManual, progress, SendRunNotification)
}

func executeClaimedJob(parent context.Context, store *Store, connections *config.Store, job Job, owner string, trigger Trigger, emit func(string)) (Run, error) {
	return executeClaimedJobWithNotifier(parent, store, connections, job, owner, trigger, emit, SendRunNotification)
}

type runNotifier func(context.Context, Job, Run) error

func executeClaimedJobWithNotifier(parent context.Context, store *Store, connections *config.Store, job Job, owner string, trigger Trigger, emit func(string), notify runNotifier) (Run, error) {
	return executeClaimedJobWithProgressAndNotifier(parent, store, connections, job, owner, trigger, progressFromEmitter(emit), notify)
}

func executeClaimedJobWithProgressAndNotifier(parent context.Context, store *Store, connections *config.Store, job Job, owner string, trigger Trigger, progress ProgressFunc, notify runNotifier) (Run, error) {
	now := time.Now().UTC()
	run, err := store.StartRun(parent, job.ID, trigger, now)
	if err != nil {
		_ = store.ReleaseJob(parent, job.ID, owner)
		return Run{}, err
	}
	report := progress
	var activity *agentActivityRecorder
	if trigger == TriggerScheduled {
		activity = newAgentActivityRecorder(store, job, run)
		defer activity.Clear()
		report = combineProgress(progress, activity.Report)
	}
	reportEvent := func(event ProgressEvent) {
		if event.Elapsed == 0 {
			event.Elapsed = time.Since(run.StartedAt)
		}
		if report != nil {
			report(event)
		}
	}
	cfg := connectionByID(connections, job.ConnectionID)
	var artifact Artifact
	var runErr error
	if cfg == nil {
		runErr = fmt.Errorf("saved connection %q no longer exists; edit or disable this backup job", job.ConnectionID)
	} else {
		timeout := time.Duration(job.TimeoutMinutes) * time.Minute
		ctx, cancel := context.WithTimeout(parent, timeout)
		reportEvent(ProgressEvent{Phase: "preflight", Message: fmt.Sprintf("starting %q for %s", job.Name, cfg.Name)})
		artifact, runErr = (Runner{Progress: reportEvent}).Run(ctx, job, cfg, run.ID)
		cancel()
	}
	run.FinishedAt = time.Now().UTC()
	if runErr != nil {
		run.Error = runErr.Error()
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) || errors.Is(parent.Err(), context.Canceled) {
			run.Status = RunCanceled
		} else {
			run.Status = RunFailed
		}
	} else {
		run.Status = RunSucceeded
		run.Artifact = artifact
	}
	if finishErr := store.FinishRun(context.Background(), &run, owner); finishErr != nil {
		_ = store.ReleaseJob(context.Background(), job.ID, owner)
		if runErr != nil {
			return run, fmt.Errorf("%v; recording run failed: %w", runErr, finishErr)
		}
		return run, finishErr
	}
	if runErr == nil {
		reportEvent(ProgressEvent{Phase: "publish", Message: fmt.Sprintf("backup complete: %s (%d bytes, sha256 %s)", artifact.Path, artifact.Size, artifact.SHA256), CurrentBytes: artifact.Size, TotalBytes: artifact.Size})
	}
	// FinishRun commits the terminal run and releases/advances the durable job
	// lease before any external email is attempted. Notification availability
	// can therefore never decide whether a backup itself succeeded.
	if notify != nil && job.Notification.ShouldNotify(run.Status) {
		reportEvent(ProgressEvent{Phase: "notification", Message: "sending email notification"})
		run.NotificationAttempted = true
		attemptContext, cancelAttempt := context.WithTimeout(context.Background(), 5*time.Second)
		attemptErr := store.recordRunNotification(attemptContext, run.ID, true, false, "")
		cancelAttempt()
		if attemptErr != nil {
			reportEvent(ProgressEvent{Phase: "notification", Message: "email delivery is continuing, but the attempt marker could not be added to backup history: " + attemptErr.Error()})
		}
		notificationCtx, cancelNotification := context.WithTimeout(context.Background(), smtpOperationTimeout)
		notificationErr := notify(notificationCtx, job, run)
		cancelNotification()
		if notificationErr != nil {
			safeNotificationErr := redactSMTPError(notificationErr, job.Notification)
			run.NotificationError = safeNotificationErr.Error()
			reportEvent(ProgressEvent{Phase: "notification", Message: "backup run was recorded, but email notification failed: " + safeNotificationErr.Error()})
		} else {
			run.NotificationSent = true
			reportEvent(ProgressEvent{Phase: "notification", Message: "email notification sent"})
		}
		outcomeContext, cancelOutcome := context.WithTimeout(context.Background(), 5*time.Second)
		outcomeErr := store.recordRunNotification(outcomeContext, run.ID, run.NotificationAttempted, run.NotificationSent, run.NotificationError)
		cancelOutcome()
		if outcomeErr != nil {
			reportEvent(ProgressEvent{Phase: "notification", Message: "email was handled, but its outcome could not be added to backup history: " + outcomeErr.Error()})
		}
	}
	if runErr != nil {
		return run, runErr
	}
	reportEvent(ProgressEvent{Phase: "retention", Message: "applying retention policy"})
	if removed, retentionErr := ApplyRetention(context.Background(), store, job, run.FinishedAt); retentionErr != nil {
		reportEvent(ProgressEvent{Phase: "retention", Message: "backup succeeded, but retention cleanup failed: " + retentionErr.Error()})
	} else if len(removed) > 0 {
		reportEvent(ProgressEvent{Phase: "retention", Message: fmt.Sprintf("retention removed %d expired backup(s)", len(removed))})
	} else {
		reportEvent(ProgressEvent{Phase: "retention", Message: "retention policy complete; no backups removed"})
	}
	return run, nil
}

func progressFromEmitter(emit func(string)) ProgressFunc {
	if emit == nil {
		return nil
	}
	return func(event ProgressEvent) { emit(formatProgressEvent(event)) }
}

func combineProgress(first, second ProgressFunc) ProgressFunc {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	return func(event ProgressEvent) {
		first(event)
		second(event)
	}
}

func formatProgressEvent(event ProgressEvent) string {
	message := strings.TrimSpace(event.Message)
	if message == "" {
		message = "backup in progress"
	}
	if event.TotalBytes > 0 {
		message += fmt.Sprintf(" (%d/%d bytes)", event.CurrentBytes, event.TotalBytes)
	} else if event.CurrentBytes > 0 {
		message += fmt.Sprintf(" (%d bytes)", event.CurrentBytes)
	}
	if strings.TrimSpace(event.Phase) == "" {
		return message
	}
	return fmt.Sprintf("[%s] %s", event.Phase, message)
}

func connectionByID(store *config.Store, id string) *config.ConnectionConfig {
	if store == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	for i := range store.Connections {
		if store.Connections[i].ID == id {
			copy := store.Connections[i]
			return &copy
		}
	}
	return nil
}
