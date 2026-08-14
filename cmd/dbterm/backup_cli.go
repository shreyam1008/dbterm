package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/shreyam1008/dbterm/internal/appdirs"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/osservice"
	"github.com/shreyam1008/dbterm/internal/processinfo"
)

func runBackupCommand(args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		printBackupHelp()
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "list", "jobs":
		return backupListCommand(args[1:])
	case "create":
		return backupCreateCommand(args[1:])
	case "run":
		return backupRunCommand(args[1:])
	case "prune":
		return backupPruneCommand(args[1:])
	case "run-due":
		return backupRunDueCommand(args[1:])
	case "agent":
		return backupAgentCommand(args[1:])
	case "status":
		return backupStatusCommand(args[1:])
	case "inspect":
		return backupInspectCommand(args[1:])
	case "restore":
		return backupRestoreCommand(args[1:])
	case "keygen":
		return backupKeygenCommand(args[1:])
	case "notify-test":
		return backupNotifyTestCommand(args[1:])
	case "service":
		return backupServiceCommand(args[1:])
	case "paths":
		return backupPathsCommand(args[1:])
	case "logs", "log":
		return backupLogsCommand(args[1:])
	default:
		return fmt.Errorf("unknown backup command %q (run `dbterm backup --help`)", args[0])
	}
}

func backupListCommand(args []string) error {
	fs := flag.NewFlagSet("backup list", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	jobs, err := store.ListJobs(context.Background())
	if err != nil {
		return err
	}
	if *jsonOutput {
		return printJSON(redactBackupJobsForOutput(jobs))
	}
	if len(jobs) == 0 {
		fmt.Println("No backup jobs. Open dbterm → Dashboard → B to create one.")
		return nil
	}
	for _, job := range jobs {
		state := "disabled"
		if job.Enabled {
			state = "enabled"
		}
		next := "manual"
		if !job.NextRunAt.IsZero() {
			next = job.NextRunAt.Local().Format(time.RFC3339)
		}
		fmt.Printf("%-30s  %-8s  %-10s  next %s\n  id %s\n  destination %s\n", job.Name, state, job.Compression, next, job.ID, job.Destination)
	}
	return nil
}

func backupCreateCommand(args []string) error {
	fs := flag.NewFlagSet("backup create", flag.ContinueOnError)
	connection := fs.String("connection", "", "saved connection ID or unique name")
	destination := fs.String("destination", "", "absolute folder or rclone://remote/path")
	name := fs.String("name", "instant", "artifact label")
	template := fs.String("filename", backupcore.DefaultFilenameTemplate, "filename template")
	compression := fs.String("compression", "zstd", "none, gzip, zip, or zstd")
	level := fs.Int("level", 3, "compression level")
	recipient := fs.String("age-recipient", "", "age X25519 public recipient")
	timeout := fs.Int("timeout", backupcore.DefaultTimeoutMinutes, "timeout in minutes")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if strings.TrimSpace(*connection) == "" || strings.TrimSpace(*destination) == "" {
		return fmt.Errorf("--connection and --destination are required")
	}
	connections, err := config.LoadStore()
	if err != nil {
		return err
	}
	cfg, err := findSavedConnection(connections, *connection)
	if err != nil {
		return err
	}
	algorithm, err := parseCompression(*compression)
	if err != nil {
		return err
	}
	output, err := resolveBackupCLIPath(*destination)
	if err != nil {
		return err
	}
	if !backupcore.IsRemoteBackupDestination(output) {
		if err := os.MkdirAll(output, 0o700); err != nil {
			return fmt.Errorf("create backup destination: %w", err)
		}
	}
	job := backupcore.Job{
		Name: *name, ConnectionID: cfg.ID, Destination: output,
		FilenameTemplate: *template, Compression: algorithm, CompressionLevel: *level,
		Encryption: backupcore.EncryptionNone,
		Schedule:   backupcore.Schedule{Kind: backupcore.ScheduleManual},
		Retention:  backupcore.Retention{KeepLast: 1}, TimeoutMinutes: *timeout,
	}
	if strings.TrimSpace(*recipient) != "" {
		job.Encryption = backupcore.EncryptionAge
		job.AgeRecipient = strings.TrimSpace(*recipient)
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		return err
	}
	runID, err := backupcore.NewID("run")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(job.TimeoutMinutes)*time.Minute)
	defer cancel()
	artifact, err := (backupcore.Runner{Progress: func(event backupcore.ProgressEvent) {
		fmt.Fprintf(os.Stderr, "[%s] %s\n", event.Phase, event.Message)
	}}).Run(ctx, job, cfg, runID)
	if err != nil {
		return err
	}
	fmt.Printf("Backup created\nPath: %s\nSize: %d bytes\nSHA-256: %s\n", artifact.Path, artifact.Size, artifact.SHA256)
	return nil
}

func backupRunCommand(args []string) error {
	if len(args) != 1 || isHelpArg(args[0]) {
		return fmt.Errorf("usage: dbterm backup run <job-id-or-name>")
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	run, err := backupcore.RunJobNow(context.Background(), store, args[0], func(line string) { fmt.Println(line) })
	if err != nil {
		return err
	}
	fmt.Printf("Run %s: %s\n", run.ID, run.Status)
	if run.NotificationAttempted {
		notification := "attempted"
		switch {
		case run.NotificationSent:
			notification = "sent"
		case strings.TrimSpace(run.NotificationError) != "":
			notification = "failed: " + run.NotificationError
		}
		fmt.Printf("Notification: %s\n", notification)
	}
	return nil
}

func backupRunDueCommand(args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: dbterm backup run-due")
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	owner, err := backupcore.NewID("once")
	if err != nil {
		return err
	}
	return backupcore.RunDue(context.Background(), store, owner, time.Now(), func(line string) { fmt.Println(line) })
}

func backupAgentCommand(args []string) error {
	fs := flag.NewFlagSet("backup agent", flag.ContinueOnError)
	poll := fs.Duration("poll", 30*time.Second, "catalog check interval")
	configOverride := fs.String("config-dir", "", "internal: explicit config directory")
	stateOverride := fs.String("state-dir", "", "internal: explicit state directory")
	logOverride := fs.String("log-dir", "", "internal: explicit log directory")
	serviceMode := fs.Bool("service-mode", false, "internal: keep routine output in the bounded agent log")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: dbterm backup agent [--poll 30s]")
	}
	for name, value := range map[string]string{
		"DBTERM_CONFIG_DIR": *configOverride,
		"DBTERM_STATE_DIR":  *stateOverride,
		"DBTERM_LOG_DIR":    *logOverride,
	} {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if !filepath.IsAbs(value) {
			return fmt.Errorf("%s must be an absolute path", name)
		}
		if err := os.Setenv(name, filepath.Clean(value)); err != nil {
			return fmt.Errorf("set %s: %w", name, err)
		}
	}
	emit, closeLog, err := openBackupAgentLogger(*serviceMode)
	if err != nil {
		return err
	}
	defer closeLog()
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		emit("backup agent startup failed: " + err.Error())
		return err
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = backupcore.RunAgent(ctx, store, *poll, emit)
	if err != nil {
		emit("backup agent failed: " + err.Error())
	}
	return err
}

func openBackupAgentLogger(serviceMode bool) (func(string), func(), error) {
	logDir, err := appdirs.LogDir()
	if err != nil {
		return nil, func() {}, err
	}
	if err := prepareBackupAgentLogDirectory(logDir); err != nil {
		return nil, func() {}, err
	}
	path := filepath.Join(logDir, backupAgentLogFilename)
	file, err := openRollingLogWriter(path, backupAgentLogMaxBytes)
	if err != nil {
		return nil, func() {}, err
	}
	var mirror io.Writer
	if !serviceMode {
		mirror = os.Stdout
	}
	emit, closeLog := newBackupAgentLogCallbacks(file, mirror, time.Now, os.Stderr)
	return emit, closeLog, nil
}

func backupStatusCommand(args []string) error {
	fs := flag.NewFlagSet("backup status", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	jobs, err := store.ListJobs(context.Background())
	if err != nil {
		return err
	}
	health, err := backupcore.AgentHealth(context.Background(), store, time.Now())
	if err != nil {
		return err
	}
	var process *processinfo.Metrics
	var processWarning string
	if health.PID > 0 {
		metrics, metricsErr := processinfo.Read(health.PID)
		if metricsErr != nil {
			processWarning = metricsErr.Error()
		} else if metrics.Alive {
			process = &metrics
		}
	}
	if *jsonOutput {
		return printJSON(struct {
			Agent          backupcore.AgentStatus `json:"agent"`
			Process        *processinfo.Metrics   `json:"process,omitempty"`
			ProcessWarning string                 `json:"process_warning,omitempty"`
			Jobs           []backupcore.Job       `json:"jobs"`
		}{health, process, processWarning, redactBackupJobsForOutput(jobs)})
	}
	state := "offline"
	if health.Healthy {
		state = "active"
	}
	fmt.Printf("Agent: %s", state)
	if health.PID != 0 {
		fmt.Printf(" (pid %d)", health.PID)
	}
	fmt.Printf("\nJobs: %d\n", len(jobs))
	if !health.Heartbeat.IsZero() {
		fmt.Printf("Heartbeat: %s\n", health.Heartbeat.Local().Format(time.RFC3339))
	}
	if process != nil {
		fmt.Printf("Process: %s\nUptime: %s\nResident memory: %s\n",
			nonEmptyBackupStatusValue(process.Name, "dbterm"), formatBackupStatusDuration(process.Uptime), backupcore.FormatByteSize(process.RSSBytes))
	} else if processWarning != "" {
		fmt.Printf("Process metrics: unavailable (%s)\n", processWarning)
	}
	if health.Activity != nil {
		activity := health.Activity
		fmt.Printf("Active backup: %s\nPhase: %s\nProgress: %s\n",
			activity.JobName, nonEmptyBackupStatusValue(activity.Phase, "working"), formatBackupActivityBytes(activity.CurrentBytes, activity.TotalBytes))
		if strings.TrimSpace(activity.Message) != "" {
			fmt.Printf("Activity: %s\n", activity.Message)
		}
	}
	return nil
}

func formatBackupStatusDuration(duration time.Duration) string {
	if duration < time.Second {
		return "<1s"
	}
	return duration.Round(time.Second).String()
}

func formatBackupActivityBytes(current, total int64) string {
	if current <= 0 {
		return "streaming; final size is not known yet"
	}
	currentText := backupcore.FormatByteSize(uint64(current))
	if total <= 0 {
		return currentText + " written; final size is not known yet"
	}
	return currentText + " / " + backupcore.FormatByteSize(uint64(total))
}

func nonEmptyBackupStatusValue(value, fallback string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return fallback
}

func backupInspectCommand(args []string) error {
	fs := flag.NewFlagSet("backup inspect", flag.ContinueOnError)
	identity := fs.String("identity", "", "age identity file")
	maxDecodedGiB := fs.Uint64("max-decoded-gib", 1, "maximum decoded size in GiB for each compression or encryption layer")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: dbterm backup inspect [--identity key.txt] [--max-decoded-gib N] <backup-file>")
	}
	maxDecodedBytes, err := maxDecodedBytesFromGiB(*maxDecodedGiB)
	if err != nil {
		return err
	}
	inspection, err := backupcore.Inspect(context.Background(), fs.Arg(0), backupcore.InspectOptions{
		AgeIdentityPath: strings.TrimSpace(*identity),
		MaxDecodedBytes: maxDecodedBytes,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		return printJSON(inspection)
	}
	fmt.Printf("Path: %s\nSize: %d bytes\nSHA-256: %s\nFormat: %s\nEngine: %s\nConfidence: %s\n", inspection.Path, inspection.Size, inspection.SHA256, inspection.Format, inspection.Engine, inspection.Confidence)
	if len(inspection.Wrappers) > 0 {
		fmt.Printf("Wrappers: %v\n", inspection.Wrappers)
	}
	if inspection.Locked {
		fmt.Println("Restore tools: unknown until the age backup is unlocked")
	} else if len(inspection.RequiredTools) > 0 {
		fmt.Printf("Restore tools: %s\n", strings.Join(inspection.RequiredTools, ", "))
	} else {
		fmt.Println("Restore tools: built into dbterm")
	}
	for _, warning := range inspection.Warnings {
		fmt.Printf("Warning: %s\n", warning)
	}
	return nil
}

func backupRestoreCommand(args []string) error {
	fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
	connection := fs.String("connection", "", "saved target connection ID or unique name")
	identity := fs.String("identity", "", "age identity file for an encrypted backup")
	modeValue := fs.String("mode", string(backupcore.RestoreModeMerge), "restore mode: merge or clean")
	stopOnError := fs.Bool("stop-on-error", true, "stop the database client after the first error")
	singleTransaction := fs.Bool("single-transaction", true, "use one transaction where the database client supports it")
	maxDecodedGiB := fs.Uint64("max-decoded-gib", 1, "maximum decoded size in GiB for each compression or encryption layer")
	timeout := fs.Duration("timeout", 0, "optional restore timeout, for example 45m (0 disables it)")
	yes := fs.Bool("yes", false, "confirm that the restore may modify the target")
	confirmClean := fs.String("confirm-clean", "", "exact database name or SQLite path required with --mode clean")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if fs.NArg() != 1 || strings.TrimSpace(*connection) == "" {
		return fmt.Errorf("usage: dbterm backup restore --connection <id|name> [options] --yes <backup-file>")
	}
	if *timeout < 0 {
		return fmt.Errorf("--timeout cannot be negative")
	}
	maxDecodedBytes, err := maxDecodedBytesFromGiB(*maxDecodedGiB)
	if err != nil {
		return err
	}
	mode, err := parseRestoreMode(*modeValue)
	if err != nil {
		return err
	}

	connections, err := config.LoadStore()
	if err != nil {
		return fmt.Errorf("load saved restore targets: %w", err)
	}
	target, err := findSavedConnection(connections, *connection)
	if err != nil {
		return err
	}

	operationCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	inspection, err := backupcore.Inspect(operationCtx, fs.Arg(0), backupcore.InspectOptions{
		AgeIdentityPath: strings.TrimSpace(*identity),
		MaxDecodedBytes: maxDecodedBytes,
	})
	if err != nil {
		return fmt.Errorf("inspect backup before restore: %w", err)
	}
	plan, err := backupcore.BuildRestorePlan(inspection, target, backupcore.RestoreOptions{
		Mode:              mode,
		StopOnError:       *stopOnError,
		SingleTransaction: *singleTransaction,
		AgeIdentityPath:   strings.TrimSpace(*identity),
		MaxDecodedBytes:   maxDecodedBytes,
	})
	if err != nil {
		return fmt.Errorf("build restore plan: %w", err)
	}

	printRestorePlan(plan)
	if err := validateRestoreConsent(plan, *yes, *confirmClean); err != nil {
		return err
	}

	ctx := operationCtx
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}

	fmt.Fprintln(os.Stderr, "Starting verified restore. Press Ctrl+C to cancel.")
	err = backupcore.ExecuteRestore(ctx, plan, func(message string) {
		fmt.Fprintln(os.Stderr, "[restore] "+message)
	})
	if err != nil {
		return err
	}
	fmt.Printf("Restore complete\nTarget: %s\nSource SHA-256: %s\n", restoreTargetSummary(&plan.Target), plan.Inspection.SHA256)
	return nil
}

func maxDecodedBytesFromGiB(value uint64) (int64, error) {
	const bytesPerGiB = uint64(1 << 30)
	if value == 0 {
		return 0, fmt.Errorf("--max-decoded-gib must be at least 1")
	}
	if value > uint64(math.MaxInt64)/bytesPerGiB {
		return 0, fmt.Errorf("--max-decoded-gib is too large")
	}
	return int64(value * bytesPerGiB), nil
}

func parseRestoreMode(value string) (backupcore.RestoreMode, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(backupcore.RestoreModeMerge):
		return backupcore.RestoreModeMerge, nil
	case string(backupcore.RestoreModeClean):
		return backupcore.RestoreModeClean, nil
	default:
		return "", fmt.Errorf("unknown restore mode %q (use merge or clean)", value)
	}
}

func validateRestoreConsent(plan *backupcore.RestorePlan, yes bool, cleanConfirmation string) error {
	if plan == nil {
		return fmt.Errorf("restore plan is required before confirmation")
	}
	if !yes {
		return fmt.Errorf("restore stopped before writing: review the plan, then rerun with --yes")
	}
	if plan.Options.Mode != backupcore.RestoreModeClean {
		return nil
	}
	expected := cleanRestoreConfirmation(&plan.Target)
	if cleanConfirmation != expected {
		return fmt.Errorf("clean restore requires --confirm-clean %q exactly; no restore was started", expected)
	}
	return nil
}

func cleanRestoreConfirmation(target *config.ConnectionConfig) string {
	if target == nil {
		return ""
	}
	if target.Type == config.SQLite {
		return filepath.Clean(target.FilePath)
	}
	return strings.TrimSpace(target.Database)
}

func printRestorePlan(plan *backupcore.RestorePlan) {
	if plan == nil || plan.Inspection == nil {
		return
	}
	fmt.Printf("Restore plan\nSource: %q\nFormat: %s (%s confidence)\nTarget: %s\nMode: %s\nStop on error: %t\nSingle transaction: %t\n",
		plan.Inspection.Path,
		plan.Inspection.Format,
		plan.Inspection.Confidence,
		restoreTargetSummary(&plan.Target),
		plan.Options.Mode,
		plan.Options.StopOnError,
		plan.Options.SingleTransaction,
	)
	for _, warning := range plan.Warnings {
		fmt.Printf("Warning: %s\n", warning)
	}
	if plan.Options.Mode == backupcore.RestoreModeClean {
		fmt.Printf("Clean confirmation required: %q\n", cleanRestoreConfirmation(&plan.Target))
	}
}

func restoreTargetSummary(target *config.ConnectionConfig) string {
	if target == nil {
		return "unknown target"
	}
	name := strings.TrimSpace(target.Name)
	if name == "" {
		name = strings.TrimSpace(target.ID)
	}
	if name == "" {
		name = "saved connection"
	}
	if target.Type == config.SQLite {
		return fmt.Sprintf("%q (SQLite %q)", name, filepath.Clean(target.FilePath))
	}
	return fmt.Sprintf("%q (%s %s@%s:%s/%s)", name, target.TypeLabel(), target.User, target.Host, target.Port, target.Database)
}

func backupKeygenCommand(args []string) error {
	fs := flag.NewFlagSet("backup keygen", flag.ContinueOnError)
	output := fs.String("output", "", "private identity output path")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if strings.TrimSpace(*output) == "" {
		configDir, err := appdirs.ConfigDir()
		if err != nil {
			return err
		}
		*output = filepath.Join(configDir, "backup", "age-identity.txt")
	}
	abs, err := filepath.Abs(filepath.Clean(*output))
	if err != nil {
		return err
	}
	recipient, err := backupcore.GenerateAgeIdentity(abs)
	if err != nil {
		return err
	}
	fmt.Printf("Private identity: %s\nPublic recipient: %s\n\nStore the private identity separately from off-site backups. Scheduled jobs save only the public recipient.\n", abs, recipient)
	return nil
}

const (
	backupServiceLockWait     = 8 * time.Second
	backupServiceLockPoll     = 100 * time.Millisecond
	backupServiceCommandLimit = 30 * time.Second
)

type agentLockProbe func() (bool, error)
type agentLockWaiter func(context.Context, bool) error

func backupServiceCommand(args []string) error {
	request, err := parseBackupServiceRequest(args)
	if err != nil {
		return err
	}
	if request.AllScopes {
		for index, scope := range []osservice.Scope{osservice.ScopeUser, osservice.ScopeSystem} {
			manager, managerErr := newBackupServiceManagerForRequest(request.withScope(scope))
			if managerErr != nil {
				return managerErr
			}
			ctx, cancel := context.WithTimeout(context.Background(), backupServiceCommandLimit)
			message, actionErr := runBackupServiceAction(ctx, manager, "status", backupcore.AgentProcessRunning, func(ctx context.Context, wantHeld bool) error {
				return waitForAgentLockState(ctx, wantHeld, backupServiceLockWait, backupServiceLockPoll, backupcore.AgentProcessRunning)
			})
			cancel()
			if actionErr != nil {
				return actionErr
			}
			if index > 0 {
				fmt.Println()
			}
			fmt.Printf("[%s service]\n%s\n", scope, message)
		}
		return nil
	}
	manager, err := newBackupServiceManagerForRequest(request)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), backupServiceCommandLimit)
	defer cancel()
	probe := agentLockProbe(backupcore.AgentProcessRunning)
	waiter := func(ctx context.Context, wantHeld bool) error {
		return waitForAgentLockState(ctx, wantHeld, backupServiceLockWait, backupServiceLockPoll, probe)
	}
	message, err := runBackupServiceAction(ctx, manager, request.Action, probe, waiter)
	if err != nil {
		return err
	}
	if message != "" {
		fmt.Println(message)
	}
	return nil
}

func runBackupServiceAction(ctx context.Context, manager osservice.Manager, action string, probe agentLockProbe, wait agentLockWaiter) (string, error) {
	if manager == nil {
		return "", fmt.Errorf("backup service manager is unavailable")
	}
	if probe == nil || wait == nil {
		return "", fmt.Errorf("backup agent lock checks are unavailable")
	}
	status, lockHeld, err := backupServiceRuntime(ctx, manager, probe)
	if err != nil {
		return "", err
	}
	action = strings.ToLower(strings.TrimSpace(action))
	managerName := strings.TrimSpace(status.Manager)
	if managerName == "" {
		managerName = "native service manager"
	}

	switch action {
	case "install":
		if err := refuseUnmanagedServiceStart(action, managerName, status, lockHeld); err != nil {
			return "", err
		}
		// Install updates an existing definition and starts it. Drain a currently
		// managed agent first so the replacement process cannot lose the global
		// lock race to the old process and then exit.
		if status.Running {
			if err := manager.Stop(ctx); err != nil {
				return "", fmt.Errorf("stop existing managed backup agent before install: %w", err)
			}
			if err := wait(ctx, false); err != nil {
				return "", fmt.Errorf("the scheduler lock remained held after stopping the existing managed service; install was not started, and a foreground agent may still be running: %w", err)
			}
		}
		if err := manager.Install(ctx); err != nil {
			return "", err
		}
		if err := verifyManagedAgentStarted(ctx, manager, managerName, wait); err != nil {
			return "", fmt.Errorf("backup service definition was installed, but its agent is not ready: %w", err)
		}
		return "Backup agent service is installed and its scheduler process is running.", nil
	case "uninstall":
		if err := manager.Uninstall(ctx); err != nil {
			return "", err
		}
		if err := wait(ctx, false); err != nil {
			if lockHeld && !status.Running {
				return "", fmt.Errorf("backup service registration was removed, but an agent outside %s still owns the scheduler lock; stop the foreground `dbterm backup agent` process separately. Jobs and backup files were not deleted: %w", managerName, err)
			}
			return "", fmt.Errorf("backup service registration was removed, but the scheduler lock remained held; a foreground agent may still be running: %w", err)
		}
		return "Backup agent registration removed and its scheduler process stopped. Jobs and backup files were not deleted.", nil
	case "start":
		if err := refuseUnmanagedServiceStart(action, managerName, status, lockHeld); err != nil {
			return "", err
		}
		if status.Running && lockHeld {
			return "Backup agent service is already running and owns the scheduler lock.", nil
		}
		if err := manager.Start(ctx); err != nil {
			return "", err
		}
		if err := verifyManagedAgentStarted(ctx, manager, managerName, wait); err != nil {
			return "", fmt.Errorf("native service start returned, but its agent is not ready: %w", err)
		}
		return "Backup agent service is running and owns the scheduler lock.", nil
	case "stop":
		if err := manager.Stop(ctx); err != nil {
			return "", err
		}
		if err := wait(ctx, false); err != nil {
			if lockHeld && !status.Running {
				return "", fmt.Errorf("native backup service was stopped or disabled, but an agent outside %s still owns the scheduler lock; stop the foreground `dbterm backup agent` process separately. Jobs remain configured: %w", managerName, err)
			}
			return "", fmt.Errorf("native service stop returned, but the scheduler lock remained held; a foreground agent may still be running: %w", err)
		}
		return "Backup agent service is stopped and the scheduler lock is released. Jobs remain configured.", nil
	case "restart":
		if err := refuseUnmanagedServiceStart(action, managerName, status, lockHeld); err != nil {
			return "", err
		}
		if err := manager.Stop(ctx); err != nil {
			return "", err
		}
		if err := wait(ctx, false); err != nil {
			return "", fmt.Errorf("the scheduler lock remained held after native service stop; restart was stopped before relaunch, and a foreground agent may still be running: %w", err)
		}
		if err := manager.Start(ctx); err != nil {
			return "", err
		}
		if err := verifyManagedAgentStarted(ctx, manager, managerName, wait); err != nil {
			return "", fmt.Errorf("native service restarted, but its agent is not ready: %w", err)
		}
		return "Backup agent service restarted after the previous scheduler process fully stopped.", nil
	case "enable":
		if !status.Installed {
			return "", fmt.Errorf("backup agent service is not installed; install it before enabling startup")
		}
		if status.StartupEnabled {
			return "Backup agent service is already enabled at startup. Its current running state was not changed.", nil
		}
		if err := osservice.SetStartupEnabled(ctx, manager, true); err != nil {
			return "", err
		}
		return "Backup agent service is enabled at startup. Its current running state was not changed.", nil
	case "disable":
		if !status.Installed {
			return "", fmt.Errorf("backup agent service is not installed; there is no startup registration to disable")
		}
		if !status.StartupEnabled {
			return "Backup agent service is already disabled at startup. Its current running state was not changed.", nil
		}
		if err := osservice.SetStartupEnabled(ctx, manager, false); err != nil {
			return "", err
		}
		return "Backup agent service is disabled at startup. Its current running state was not changed.", nil
	case "status":
		return formatBackupServiceStatus(status, lockHeld), nil
	default:
		return "", fmt.Errorf("unknown service action %q", action)
	}
}

func backupServiceRuntime(ctx context.Context, manager osservice.Manager, probe agentLockProbe) (osservice.Status, bool, error) {
	status, err := manager.Status(ctx)
	if err != nil {
		return status, false, err
	}
	lockHeld, err := probe()
	if err != nil {
		return status, false, fmt.Errorf("check global backup agent scheduler lock: %w", err)
	}
	return status, lockHeld, nil
}

func refuseUnmanagedServiceStart(action, managerName string, status osservice.Status, lockHeld bool) error {
	if !lockHeld || status.Running {
		return nil
	}
	return fmt.Errorf("cannot %s the native backup service: an agent outside %s already owns the scheduler lock; stop the foreground `dbterm backup agent` process, then retry", action, managerName)
}

func verifyManagedAgentStarted(ctx context.Context, manager osservice.Manager, managerName string, wait agentLockWaiter) error {
	if err := wait(ctx, true); err != nil {
		return fmt.Errorf("the scheduler lock was not acquired: %w; check `dbterm backup service status` and the backup agent logs", err)
	}
	status, err := manager.Status(ctx)
	if err != nil {
		return fmt.Errorf("verify %s state after scheduler lock acquisition: %w", managerName, err)
	}
	if !status.Running {
		return fmt.Errorf("the scheduler lock is held, but %s does not report the managed agent as running; another foreground agent may own the lock", managerName)
	}
	return nil
}

func waitForAgentLockState(ctx context.Context, wantHeld bool, timeout, pollInterval time.Duration, probe agentLockProbe) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if probe == nil {
		return fmt.Errorf("backup agent lock probe is unavailable")
	}
	if timeout <= 0 || pollInterval <= 0 {
		return fmt.Errorf("backup agent lock wait requires positive timeout and polling interval")
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	wantedState := "acquired"
	if !wantHeld {
		wantedState = "released"
	}
	for {
		held, err := probe()
		if err != nil {
			return fmt.Errorf("probe scheduler lock while waiting for it to be %s: %w", wantedState, err)
		}
		if held == wantHeld {
			return nil
		}
		timer := time.NewTimer(pollInterval)
		select {
		case <-waitCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("stopped waiting for the scheduler lock to be %s: %w", wantedState, err)
			}
			return fmt.Errorf("timed out waiting for the scheduler lock to be %s: %w", wantedState, waitCtx.Err())
		case <-timer.C:
		}
	}
}

func formatBackupServiceStatus(status osservice.Status, lockHeld bool) string {
	lockState := "free"
	if lockHeld {
		lockState = "held"
	}
	runtimeState := "stopped; no scheduler process owns the global lock"
	switch {
	case status.Running && lockHeld:
		runtimeState = "managed service is running and owns the scheduler lock"
	case status.Running:
		runtimeState = "service manager reports running, but the scheduler lock is free (the agent may be starting or unhealthy)"
	case lockHeld:
		runtimeState = "an agent outside this native service registration owns the scheduler lock (likely a foreground `dbterm backup agent`)"
	}
	return fmt.Sprintf("Manager: %s\nName: %s\nScope: %s\nInstalled: %t\nStartup enabled: %t\nManager running: %t\nAgent lock: %s\nRuntime: %s\nDetail: %s",
		status.Manager, status.Name, status.Scope, status.Installed, status.StartupEnabled, status.Running, lockState, runtimeState, status.Detail)
}

func backupPathsCommand(args []string) error {
	fs := flag.NewFlagSet("backup paths", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	configDir, err := appdirs.ConfigDir()
	if err != nil {
		return err
	}
	stateDir, err := appdirs.StateDir()
	if err != nil {
		return err
	}
	logDir, err := appdirs.LogDir()
	if err != nil {
		return err
	}
	storePath, err := backupcore.DefaultStorePath()
	if err != nil {
		return err
	}
	stagingPath, err := backupcore.DefaultStagingPath()
	if err != nil {
		return err
	}
	agentLog := filepath.Join(logDir, backupAgentLogFilename)
	paths := map[string]string{"config": configDir, "state": stateDir, "logs": logDir, "agent_log": agentLog, "backup_catalog": storePath, "backup_staging": stagingPath}
	if *jsonOutput {
		return printJSON(paths)
	}
	for _, key := range []string{"config", "state", "logs", "agent_log", "backup_catalog", "backup_staging"} {
		fmt.Printf("%-16s %s\n", key+":", paths[key])
	}
	return nil
}

func newBackupServiceManager() (osservice.Manager, error) {
	return newBackupServiceManagerForRequest(backupServiceRequest{Scope: osservice.ScopeUser})
}

func newBackupServiceManagerForRequest(request backupServiceRequest) (osservice.Manager, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve dbterm executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	logDir := strings.TrimSpace(request.LogDir)
	if logDir == "" {
		logDir, err = appdirs.LogDir()
		if err != nil {
			return nil, err
		}
	}
	configDir := strings.TrimSpace(request.ConfigDir)
	if configDir == "" {
		configDir, err = appdirs.ConfigDir()
		if err != nil {
			return nil, err
		}
	}
	stateDir := strings.TrimSpace(request.StateDir)
	if stateDir == "" {
		stateDir, err = appdirs.StateDir()
		if err != nil {
			return nil, err
		}
	}
	return osservice.New(osservice.Options{
		Executable: executable,
		ConfigDir:  configDir,
		StateDir:   stateDir,
		LogDir:     logDir,
		Scope:      request.Scope,
		RunAsUser:  request.RunAsUser,
	})
}

func findSavedConnection(store *config.Store, idOrName string) (*config.ConnectionConfig, error) {
	if store == nil {
		return nil, fmt.Errorf("saved connections are unavailable")
	}
	needle := strings.TrimSpace(idOrName)
	for index := range store.Connections {
		if store.Connections[index].ID == needle {
			copy := store.Connections[index]
			return &copy, nil
		}
	}
	var found *config.ConnectionConfig
	for index := range store.Connections {
		if strings.EqualFold(store.Connections[index].Name, needle) {
			if found != nil {
				return nil, fmt.Errorf("connection name %q is ambiguous; use its ID from the Dashboard/job catalog", needle)
			}
			copy := store.Connections[index]
			found = &copy
		}
	}
	if found == nil {
		return nil, fmt.Errorf("saved connection %q was not found", needle)
	}
	return found, nil
}

func parseCompression(value string) (backupcore.Compression, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none":
		return backupcore.CompressionNone, nil
	case "gzip", "gz":
		return backupcore.CompressionGzip, nil
	case "zip":
		return backupcore.CompressionZip, nil
	case "zstd", "zst":
		return backupcore.CompressionZstd, nil
	default:
		return "", fmt.Errorf("unknown compression %q (use none, gzip, zip, or zstd)", value)
	}
}

func resolveBackupCLIPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if backupcore.IsRemoteBackupDestination(value) {
		return backupcore.NormalizeBackupDestination(value)
	}
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if value == "~" {
			value = home
		} else {
			value = filepath.Join(home, value[2:])
		}
	}
	abs, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", value, err)
	}
	return backupcore.NormalizeBackupDestination(abs)
}

func printJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func redactBackupJobsForOutput(jobs []backupcore.Job) []backupcore.Job {
	redacted := make([]backupcore.Job, len(jobs))
	copy(redacted, jobs)
	for index := range redacted {
		redacted[index].Notification.Password = ""
	}
	return redacted
}

func ignoreFlagHelp(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}

func isHelpArg(arg string) bool {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "help", "-h", "--help":
		return true
	default:
		return false
	}
}

func printBackupHelp() {
	fmt.Print(`
  dbterm backup — instant and scheduled database protection

  USAGE
    dbterm backup list [--json]
    dbterm backup create --connection <id|name> --destination <folder> [options]
    dbterm backup run <job-id|name>
    dbterm backup prune --yes <job-id|name>
    dbterm backup inspect [--identity key.txt] [--max-decoded-gib N] <backup-file>
    dbterm backup restore --connection <id|name> [--identity key.txt] [--max-decoded-gib N] [--mode merge|clean] --yes <backup-file>
    dbterm backup status [--json]
    dbterm backup service <install|uninstall|start|stop|restart|enable|disable|status> [--user|--system]
    dbterm backup service install --system [--run-as USER] --config-dir PATH --state-dir PATH --log-dir PATH
    dbterm backup service status --all
    dbterm backup keygen [--output identity.txt]
    dbterm backup notify-test <job-id|name>
    dbterm backup paths [--json]
    dbterm backup logs [--lines 200] [--previous]

  INTERNAL / HEADLESS
    dbterm backup agent [--poll 30s]
    dbterm backup run-due

  Scheduled jobs are configured in the TUI: Dashboard → Ctrl+B on a saved
  connection, or Dashboard → B → N.
  Sources may be local or remote PostgreSQL/MySQL plus SQLite, Turso, and D1.
  Destinations may be absolute local/mounted folders or rclone://remote/path.
  Configure remote storage with "rclone config"; credentials remain in rclone.
  Encryption uses age X25519.
  Restore always inspects content first and requires --yes. Clean mode also requires
  --confirm-clean with the exact target database name (or absolute SQLite path).
  Inspection and restore unwrap at most three compression/encryption layers into
  private OS temporary files. Each layer defaults to a 1 GiB decoded limit; raise
  it explicitly for larger trusted backups with --max-decoded-gib N.

`)
}
