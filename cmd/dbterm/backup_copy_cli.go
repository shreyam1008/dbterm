package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	backupcore "github.com/shreyam1008/dbterm/internal/backup"
)

type repeatStringFlag []string

func (values *repeatStringFlag) String() string { return strings.Join(*values, ",") }
func (values *repeatStringFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("value cannot be empty")
	}
	*values = append(*values, value)
	return nil
}

func backupCopyCommand(args []string) error {
	if len(args) == 0 || isHelpArg(args[0]) {
		printBackupCopyHelp()
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "list":
		return backupCopyListCommand(args[1:])
	case "create":
		return backupCopyCreateCommand(args[1:])
	case "run":
		return backupCopyRunCommand(args[1:])
	case "test":
		return backupCopyTestCommand(args[1:])
	case "status":
		return backupCopyStatusCommand(args[1:])
	case "inspect":
		return backupCopyInspectCommand(args[1:])
	case "enable":
		return backupCopyEnableCommand(args[1:], true)
	case "disable":
		return backupCopyEnableCommand(args[1:], false)
	case "prune":
		return backupCopyPruneCommand(args[1:])
	case "delete":
		return backupCopyDeleteCommand(args[1:])
	default:
		return fmt.Errorf("unknown backup copy command %q (run `dbterm backup copy --help`)", args[0])
	}
}

type backupCopyInspectionOutput struct {
	CopyJobID       string                `json:"copy_job_id"`
	CopyJobName     string                `json:"copy_job_name"`
	ArtifactID      string                `json:"artifact_id"`
	CopySource      string                `json:"copy_source"`
	CopyDestination string                `json:"copy_destination"`
	Inspection      backupcore.Inspection `json:"inspection"`
	VolumeWarnings  []string              `json:"volume_warnings,omitempty"`
}

func backupCopyInspectCommand(args []string) (returnErr error) {
	fs := flag.NewFlagSet("backup copy inspect", flag.ContinueOnError)
	artifactID := fs.String("artifact", "", "exact artifact ID; defaults to the newest completed unpruned copy")
	identity := fs.String("identity", "", "age identity file for an encrypted artifact")
	maxDecodedGiB := fs.Uint64("max-decoded-gib", 1, "maximum decoded size in GiB for each compression or encryption layer")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	jobReference := ""
	parseArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		jobReference = strings.TrimSpace(args[0])
		parseArgs = args[1:]
	}
	if err := fs.Parse(parseArgs); err != nil {
		return ignoreFlagHelp(err)
	}
	if jobReference == "" {
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: dbterm backup copy inspect <copy-id-or-name> [--artifact ARTIFACT_ID] [--identity PATH] [--max-decoded-gib N] [--json]")
		}
		jobReference = strings.TrimSpace(fs.Arg(0))
	} else if fs.NArg() != 0 {
		return fmt.Errorf("usage: dbterm backup copy inspect <copy-id-or-name> [--artifact ARTIFACT_ID] [--identity PATH] [--max-decoded-gib N] [--json]")
	}
	if jobReference == "" {
		return fmt.Errorf("copy job ID or name is required")
	}
	maxDecodedBytes, err := maxDecodedBytesFromGiB(*maxDecodedGiB)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	job, err := store.GetCopyJob(ctx, jobReference)
	if err != nil {
		return err
	}
	runs, err := store.ListCopyRuns(ctx, job.ID, 1000)
	if err != nil {
		return err
	}
	artifact, err := selectCopyArtifactForInspection(runs, strings.TrimSpace(*artifactID))
	if err != nil {
		return fmt.Errorf("select copy artifact for %q: %w", job.Name, err)
	}
	staged, volumeWarnings, err := backupcore.StageCopyArtifactForInspectionWithVolume(ctx, store, job, artifact, backupcore.CopyInspectionStageOptions{
		Location: backupcore.CopyInspectionDestination,
	})
	if err != nil {
		if len(volumeWarnings) > 0 {
			return fmt.Errorf("stage copied artifact %q for inspection: %w; destination volume release warnings: %s", artifact.ArtifactID, err, strings.Join(volumeWarnings, "; "))
		}
		return fmt.Errorf("stage copied artifact %q for inspection: %w", artifact.ArtifactID, err)
	}
	defer func() {
		if cleanupErr := staged.Close(); cleanupErr != nil {
			if returnErr == nil {
				returnErr = cleanupErr
			} else {
				returnErr = fmt.Errorf("%w; private inspection staging cleanup also failed: %v", returnErr, cleanupErr)
			}
		}
	}()
	inspection, err := staged.Inspect(ctx, backupcore.InspectOptions{
		AgeIdentityPath: strings.TrimSpace(*identity), MaxDecodedBytes: maxDecodedBytes,
	})
	if err != nil {
		return fmt.Errorf("inspect verified copied artifact %q: %w", artifact.ArtifactID, err)
	}

	// The private staged path disappears when this command returns. Report the
	// durable recovery-copy location as Path so both text and JSON remain useful.
	displayInspection := *inspection
	displayInspection.Path = artifact.Destination
	if *jsonOutput {
		return printJSON(backupCopyInspectionOutput{
			CopyJobID: job.ID, CopyJobName: job.Name, ArtifactID: artifact.ArtifactID,
			CopySource: artifact.Source, CopyDestination: artifact.Destination, Inspection: displayInspection, VolumeWarnings: volumeWarnings,
		})
	}
	printCopyInspection(job, artifact, &displayInspection)
	printCopyVolumeWarnings(volumeWarnings)
	return nil
}

func selectCopyArtifactForInspection(runs []backupcore.CopyRun, requestedID string) (backupcore.CopyArtifactResult, error) {
	return backupcore.SelectCopyArtifactForInspection(runs, requestedID)
}

func printCopyInspection(job backupcore.CopyJob, artifact backupcore.CopyArtifactResult, inspection *backupcore.Inspection) {
	if inspection == nil {
		return
	}
	fmt.Printf("Copy job: %s (%s)\nArtifact ID: %s\nCopy source: %s\nRecovery copy: %s\n", job.Name, job.ID, artifact.ArtifactID, artifact.Source, artifact.Destination)
	fmt.Printf("Path: %s\nSize: %d bytes\nSHA-256: %s\nFormat: %s\nEngine: %s\nConfidence: %s\n", inspection.Path, inspection.Size, inspection.SHA256, inspection.Format, inspection.Engine, inspection.Confidence)
	if inspection.Format == backupcore.FormatDBTermBundle {
		fmt.Printf("Database format: %s\n", inspection.DatabaseFormat)
		if len(inspection.FileSets) == 0 {
			fmt.Println("Included file sets: none")
		} else {
			fmt.Printf("Included file sets: %d\n", len(inspection.FileSets))
			for _, set := range inspection.FileSets {
				fmt.Printf("  %s: %d files, %d bytes\n", set.Label, set.FileCount, set.SizeBytes)
			}
		}
	}
	if inspection.Manifest != nil {
		fmt.Printf("Completion manifest: schema %d, artifact %s, producer %s, verification %s\n", inspection.Manifest.SchemaVersion, inspection.Manifest.ArtifactID, inspection.Manifest.ProducerID, inspection.Manifest.VerificationLevel)
	}
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
}

func backupCopyListCommand(args []string) error {
	fs := flag.NewFlagSet("backup copy list", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: dbterm backup copy list [--json]")
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	jobs, err := store.ListCopyJobs(context.Background())
	if err != nil {
		return err
	}
	if *jsonOutput {
		for index := range jobs {
			jobs[index].Notification.Password = ""
		}
		return printJSON(jobs)
	}
	if len(jobs) == 0 {
		fmt.Println("No copy jobs. Create one with `dbterm backup copy create` or press C in Backup Center.")
		return nil
	}
	for _, job := range jobs {
		state := "disabled"
		if job.Enabled {
			state = "enabled"
		}
		next := "on demand"
		if job.Trigger == backupcore.CopyTriggerAfterSuccess {
			next = "after backup success"
		} else if !job.NextRunAt.IsZero() {
			next = job.NextRunAt.Local().Format(time.RFC3339)
		}
		fmt.Printf("%-28s  %-4s  %-8s  %-14s  %s\n  id %s\n  %s -> %s\n",
			job.Name, job.Mode, state, job.Verification, next, job.ID,
			copyEndpointDisplay(job.Source, job.SourceBackupJobID), copyEndpointDisplay(job.Destination, ""))
		if job.DestinationVolume != nil {
			fmt.Printf("  volume %s\n", copyCLIVolumeDisplay(*job.DestinationVolume))
		}
		if job.Trigger != backupcore.CopyTriggerManual {
			proof := "pending; run this copy manually before enabling automation"
			if job.HasCurrentTransferProof() {
				proof = "verified by a real transfer at " + job.TransferProofAt.Local().Format(time.RFC3339)
			}
			fmt.Printf("  automation proof %s\n", proof)
		}
	}
	return nil
}

func backupCopyCreateCommand(args []string) error {
	fs := flag.NewFlagSet("backup copy create", flag.ContinueOnError)
	name := fs.String("name", "", "copy job name")
	modeValue := fs.String("mode", "", "push or pull (required)")
	sourceValue := fs.String("source", "", "absolute local path, sftp://user@host/path, ssh://user@host/path, or rclone://remote/path")
	sourceJobValue := fs.String("source-job", "", "local backup job ID or unique name")
	destinationValue := fs.String("destination", "", "absolute local path or sftp://user@host/path")
	identityValue := fs.String("identity", "", "absolute Ed25519 private identity path for SSH/SFTP")
	hostKeyValue := fs.String("host-key", "", "pinned SSH host-key fingerprint in SHA256:<base64> form")
	triggerValue := fs.String("trigger", "", "manual, after-success, or timed")
	every := fs.Duration("every", 0, "timed interval, for example 30m or 12h")
	timezone := fs.String("timezone", "Local", "IANA timezone for --at times")
	freshness := fs.Duration("expected-freshness", 0, "alerting threshold recorded for producer freshness")
	keepLast := fs.Int("keep-last", 14, "verified local vault recovery points to retain")
	maxAgeDays := fs.Int("max-age-days", 0, "maximum local vault recovery-point age; 0 disables")
	maxTotalGiB := fs.Int64("max-total-gib", 0, "maximum local vault bytes; 0 disables")
	timeout := fs.Int("timeout", backupcore.DefaultTimeoutMinutes, "copy timeout in minutes")
	attempts := fs.Int("attempts", 3, "maximum attempts for transient transfer failures")
	retryInitial := fs.Duration("retry-initial", 2*time.Second, "initial retry delay before jitter")
	retryMax := fs.Duration("retry-max", time.Minute, "maximum retry delay before jitter")
	enable := fs.Bool("enable", false, "request immediate automatic enablement (rejected until a real transfer is recorded)")
	volumeMode := fs.String("volume-mode", "", "already-mounted, os-managed, or managed-linux-block-device")
	volumeMountPoint := fs.String("volume-mount-point", "", "absolute root of the intended destination volume")
	volumeSentinelFile := fs.String("volume-sentinel-file", "", "identity filename at the volume root (default .dbterm-volume-id)")
	volumeID := fs.String("volume-id", "", "exact non-secret identity token stored in the sentinel file")
	volumeUUID := fs.String("volume-uuid", "", "filesystem UUID for a managed Linux block device")
	volumeFilesystem := fs.String("volume-filesystem", "", "expected filesystem type for a managed Linux block device")
	volumeLabel := fs.String("volume-label", "", "optional expected filesystem label for a managed Linux block device")
	volumeWarmup := fs.Duration("volume-warmup", 0, "optional managed-volume warmup duration")
	volumeCooldown := fs.Duration("volume-cooldown", 0, "optional managed-volume cooldown duration")
	volumeSpindown := fs.Bool("volume-spindown", false, "unmount and request device power-off after an owned managed mount")
	var atValues repeatStringFlag
	var formats repeatStringFlag
	var volumeMountOptions repeatStringFlag
	fs.Var(&atValues, "at", "daily HH:MM wall-clock time; repeat for multiple times")
	fs.Var(&formats, "format", "accepted manifest format; repeat to allow several")
	fs.Var(&volumeMountOptions, "volume-mount-option", "managed Linux mount option; repeat for several (safe defaults are added)")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}
	if *enable {
		return fmt.Errorf("--enable cannot be used while creating a copy job: create it disabled, run `dbterm backup copy run <copy>` to prove a real transfer, then run `dbterm backup copy enable <copy>`; `copy test` is read-only and does not count")
	}
	if strings.TrimSpace(*name) == "" || strings.TrimSpace(*modeValue) == "" || strings.TrimSpace(*destinationValue) == "" {
		return fmt.Errorf("--name, --mode, and --destination are required")
	}
	if strings.TrimSpace(*sourceValue) == "" && strings.TrimSpace(*sourceJobValue) == "" {
		return fmt.Errorf("--source or --source-job is required")
	}
	mode := backupcore.CopyMode(strings.ToLower(strings.TrimSpace(*modeValue)))
	if mode != backupcore.CopyModePush && mode != backupcore.CopyModePull {
		return fmt.Errorf("--mode must be push or pull")
	}
	if *every > 0 && len(atValues) > 0 {
		return fmt.Errorf("use either --every or one or more --at values, not both")
	}

	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	sourceJobID := ""
	if strings.TrimSpace(*sourceJobValue) != "" {
		job, err := store.GetJob(context.Background(), strings.TrimSpace(*sourceJobValue))
		if err != nil {
			return err
		}
		sourceJobID = job.ID
	}
	source, err := parseCopyCLIEndpoint(*sourceValue, *identityValue, *hostKeyValue, sourceJobID != "")
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	destination, err := parseCopyCLIEndpoint(*destinationValue, *identityValue, *hostKeyValue, false)
	if err != nil {
		return fmt.Errorf("destination: %w", err)
	}
	if destination.Kind == backupcore.CopyEndpointRclone {
		return fmt.Errorf("rclone push is disabled because dbterm cannot yet prove create-only finalization on every backend; use rclone as a pull source")
	}
	trigger, schedule, err := copyCLISchedule(*triggerValue, mode, sourceJobID, *every, atValues, *timezone)
	if err != nil {
		return err
	}
	maxTotalBytes, err := gibibytesToBytes(*maxTotalGiB)
	if err != nil {
		return err
	}
	destinationVolume, err := copyCLIDestinationVolume(
		*volumeMode, *volumeMountPoint, *volumeSentinelFile, *volumeID,
		*volumeUUID, *volumeFilesystem, *volumeLabel, volumeMountOptions,
		*volumeWarmup, *volumeCooldown, *volumeSpindown,
	)
	if err != nil {
		return err
	}
	job := backupcore.CopyJob{
		Name: *name, Enabled: false, Mode: mode,
		Source: source, SourceBackupJobID: sourceJobID, Destination: destination,
		DestinationVolume: destinationVolume,
		ArtifactFilter:    backupcore.CopyArtifactFilter{Formats: append([]string(nil), formats...)},
		Trigger:           trigger, Schedule: schedule,
		ExpectedFreshnessMinutes: durationMinutesCeil(*freshness),
		Verification:             backupcore.CopyVerificationSHA256Format,
		Retention: backupcore.Retention{
			KeepLast: *keepLast, MaxAgeDays: *maxAgeDays, MaxTotalBytes: maxTotalBytes,
		},
		TimeoutMinutes: *timeout,
		MaxAttempts:    *attempts, RetryInitialSeconds: durationSecondsCeil(*retryInitial), RetryMaxSeconds: durationSecondsCeil(*retryMax),
	}
	if err := store.UpsertCopyJob(context.Background(), &job); err != nil {
		return err
	}
	state := "disabled; run it once successfully before enabling automation"
	fmt.Printf("Copy job created: %s\nID: %s\nMode: %s\nTrigger: %s\nSource: %s\nDestination: %s\nVerification: %s\nState: %s\n",
		job.Name, job.ID, job.Mode, job.Trigger, copyEndpointDisplay(job.Source, job.SourceBackupJobID), copyEndpointDisplay(job.Destination, ""), job.Verification, state)
	if job.DestinationVolume != nil {
		fmt.Printf("Destination volume: %s\n", copyCLIVolumeDisplay(*job.DestinationVolume))
	}
	return nil
}

func copyCLIDestinationVolume(modeValue, mountPoint, sentinelFile, sentinelValue, filesystemUUID, filesystem, label string, mountOptions []string, warmup, cooldown time.Duration, spindown bool) (*backupcore.CopyDestinationVolume, error) {
	modeValue = strings.ToLower(strings.TrimSpace(modeValue))
	mountPoint = strings.TrimSpace(mountPoint)
	sentinelFile = strings.TrimSpace(sentinelFile)
	sentinelValue = strings.TrimSpace(sentinelValue)
	filesystemUUID = strings.TrimSpace(filesystemUUID)
	filesystem = strings.TrimSpace(filesystem)
	label = strings.TrimSpace(label)
	configured := modeValue != "" || mountPoint != "" || sentinelFile != "" || sentinelValue != "" || filesystemUUID != "" || filesystem != "" || label != "" || len(mountOptions) > 0 || warmup != 0 || cooldown != 0 || spindown
	if !configured {
		return nil, nil
	}
	if modeValue == "" {
		return nil, fmt.Errorf("--volume-mode is required when destination-volume settings are supplied")
	}
	if mountPoint == "" || sentinelValue == "" {
		return nil, fmt.Errorf("--volume-mount-point and --volume-id are required with --volume-mode")
	}
	if warmup < 0 || cooldown < 0 {
		return nil, fmt.Errorf("--volume-warmup and --volume-cooldown cannot be negative")
	}
	resolvedMountPoint, err := resolveBackupCLIPath(mountPoint)
	if err != nil {
		return nil, fmt.Errorf("resolve destination volume mount point: %w", err)
	}
	mode := backupcore.CopyVolumeMode(strings.ReplaceAll(modeValue, "-", "_"))
	switch mode {
	case backupcore.CopyVolumeAlreadyMounted, backupcore.CopyVolumeOSManaged, backupcore.CopyVolumeManagedLinuxBlockDevice:
	default:
		return nil, fmt.Errorf("--volume-mode must be already-mounted, os-managed, or managed-linux-block-device")
	}
	return &backupcore.CopyDestinationVolume{
		Mode: mode, MountPoint: resolvedMountPoint,
		SentinelFile: sentinelFile, SentinelValue: sentinelValue,
		FilesystemUUID: filesystemUUID, ExpectedFilesystem: filesystem, ExpectedVolumeLabel: label,
		MountOptions:  append([]string(nil), mountOptions...),
		WarmupSeconds: durationSecondsCeil(warmup), CooldownSeconds: durationSecondsCeil(cooldown), Spindown: spindown,
	}, nil
}

func copyCLIVolumeDisplay(volume backupcore.CopyDestinationVolume) string {
	label := fmt.Sprintf("%s at %s; sentinel %s", volume.Mode, volume.MountPoint, volume.SentinelFile)
	if volume.Mode == backupcore.CopyVolumeManagedLinuxBlockDevice {
		label += fmt.Sprintf("; UUID %s; filesystem %s", volume.FilesystemUUID, volume.ExpectedFilesystem)
		if volume.ExpectedVolumeLabel != "" {
			label += "; label " + volume.ExpectedVolumeLabel
		}
		if volume.Spindown {
			label += "; spindown enabled"
		}
	}
	return label
}

func backupCopyRunCommand(args []string) error {
	if len(args) != 1 || isHelpArg(args[0]) {
		return fmt.Errorf("usage: dbterm backup copy run <copy-id-or-name>")
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	run, runErr := backupcore.RunCopyJobNow(ctx, store, args[0], func(line string) { fmt.Println(line) })
	if strings.TrimSpace(run.ID) == "" {
		return runErr
	}
	printCopyRun(run)
	if job, jobErr := store.GetCopyJob(context.Background(), args[0]); runErr == nil && jobErr == nil &&
		job.Trigger != backupcore.CopyTriggerManual && !job.Enabled && job.HasCurrentTransferProof() && job.TransferProofAt.Equal(run.FinishedAt) {
		fmt.Println("Automatic enablement is now available for this configuration. Run `dbterm backup copy enable <copy>` when ready.")
	}
	return runErr
}

func backupCopyStatusCommand(args []string) error {
	fs := flag.NewFlagSet("backup copy status", flag.ContinueOnError)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("usage: dbterm backup copy status [--json] [copy-id-or-name]")
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	jobID := ""
	if fs.NArg() == 1 {
		job, err := store.GetCopyJob(context.Background(), fs.Arg(0))
		if err != nil {
			return err
		}
		jobID = job.ID
	}
	runs, err := store.ListCopyRuns(context.Background(), jobID, 100)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return printJSON(runs)
	}
	if len(runs) == 0 {
		fmt.Println("No copy activity has been recorded.")
		return nil
	}
	for _, run := range runs {
		printCopyRun(run)
	}
	return nil
}

func backupCopyEnableCommand(args []string, enabled bool) error {
	if len(args) != 1 || isHelpArg(args[0]) {
		verb := "enable"
		if !enabled {
			verb = "disable"
		}
		return fmt.Errorf("usage: dbterm backup copy %s <copy-id-or-name>", verb)
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.SetCopyJobEnabled(context.Background(), args[0], enabled); err != nil {
		return err
	}
	if enabled {
		fmt.Println("Copy job enabled. Keep the dbterm backup agent service running for automatic execution.")
	} else {
		fmt.Println("Copy job disabled. Existing backup and vault files were not changed.")
	}
	return nil
}

func backupCopyPruneCommand(args []string) error {
	fs := flag.NewFlagSet("backup copy prune", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "apply the displayed retention plan")
	dryRun := fs.Bool("dry-run", false, "preview only (the default)")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if fs.NArg() != 1 || (*yes && *dryRun) {
		return fmt.Errorf("usage: dbterm backup copy prune [--dry-run|--yes] <copy-id-or-name>")
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	job, err := store.GetCopyJob(context.Background(), fs.Arg(0))
	if err != nil {
		return err
	}
	candidates, previewWarnings, err := backupcore.PreviewCopyRetentionWithVolume(context.Background(), store, job, time.Now())
	if err != nil {
		if len(previewWarnings) > 0 {
			return fmt.Errorf("preview copy retention: %w; destination volume release warnings: %s", err, strings.Join(previewWarnings, "; "))
		}
		return err
	}
	if len(candidates) == 0 {
		fmt.Println("Copy retention preview: no verified recovery points would be removed.")
		printCopyVolumeWarnings(previewWarnings)
		return nil
	}
	fmt.Printf("Copy retention preview for %s (%d recovery points):\n", job.Name, len(candidates))
	for _, candidate := range candidates {
		fmt.Printf("  %s  %d bytes  %s\n", candidate.Path, candidate.SizeBytes, candidate.ArtifactID)
	}
	if !*yes {
		fmt.Println("Preview only. Re-run with --yes to verify each file again and apply this exact policy.")
		printCopyVolumeWarnings(previewWarnings)
		return nil
	}
	printCopyVolumeWarnings(previewWarnings)
	removed, applyWarnings, err := backupcore.ApplyCopyRetentionPlanWithVolume(context.Background(), store, job, time.Now(), candidates)
	if err != nil {
		if len(applyWarnings) > 0 {
			return fmt.Errorf("apply copy retention: %w; destination volume release warnings: %s", err, strings.Join(applyWarnings, "; "))
		}
		return err
	}
	fmt.Printf("Removed %d verified copied recovery point(s).\n", len(removed))
	printCopyVolumeWarnings(applyWarnings)
	return nil
}

func printCopyVolumeWarnings(warnings []string) {
	for _, warning := range warnings {
		if warning = strings.TrimSpace(warning); warning != "" {
			fmt.Printf("Destination volume warning: %s\n", warning)
		}
	}
}

func backupCopyDeleteCommand(args []string) error {
	fs := flag.NewFlagSet("backup copy delete", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "confirm deletion of the policy and its catalog linkage")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if fs.NArg() != 1 || !*yes {
		return fmt.Errorf("usage: dbterm backup copy delete --yes <copy-id-or-name> (backup files are never deleted)")
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.DeleteCopyJob(context.Background(), fs.Arg(0)); err != nil {
		return err
	}
	fmt.Println("Copy job deleted. Existing source and destination files were not changed.")
	return nil
}

func backupCopyTestCommand(args []string) error {
	if len(args) != 1 || isHelpArg(args[0]) {
		return fmt.Errorf("usage: dbterm backup copy test <copy-id-or-name>")
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	job, err := store.GetCopyJob(context.Background(), args[0])
	if err != nil {
		return err
	}
	remote := job.Source
	if remote.Kind == backupcore.CopyEndpointLocal {
		remote = job.Destination
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	volumeProof := ""
	if job.DestinationVolume != nil {
		if err := backupcore.VerifyCopyDestinationVolumeIdentity(job); err != nil {
			return fmt.Errorf("verify configured destination volume identity: %w", err)
		}
		volumeProof = "Destination volume identity: verified (read-only; mount, unmount, sync, and spindown were not exercised)"
	}
	switch remote.Kind {
	case backupcore.CopyEndpointSSH, backupcore.CopyEndpointSFTP:
		measurement, err := backupcore.MeasureSFTPTransport(ctx, remote)
		if err != nil {
			return err
		}
		createOnlyCapability := "no"
		if measurement.CreateOnlyPublish {
			createOnlyCapability = "yes (hardlink@openssh.com)"
		}
		stableSyncCapability := "no"
		if measurement.StableStorageSync {
			stableSyncCapability = "yes (fsync@openssh.com)"
		}
		fmt.Printf("SFTP read-only transport test passed\nHost key: %s\nConnect: %s\nList: %s\nEntries: %d\nRead access: yes\nAtomic create-only capability: %s\nStable-storage sync capability: %s\nWrite permission: checked on the first push; this read-only test changed no remote files\n",
			measurement.HostKeyFingerprint, measurement.ConnectDuration.Round(time.Millisecond), measurement.ListDuration.Round(time.Millisecond), measurement.Entries,
			createOnlyCapability, stableSyncCapability)
		if volumeProof != "" {
			fmt.Println(volumeProof)
		}
		if job.Mode == backupcore.CopyModePush && (!measurement.CreateOnlyPublish || !measurement.StableStorageSync) {
			return fmt.Errorf("SFTP push readiness failed: the server must advertise hardlink@openssh.com and fsync@openssh.com version 1; no remote file was changed")
		}
		return nil
	case backupcore.CopyEndpointRclone:
		measurement, err := backupcore.MeasureRcloneSource(ctx, remote)
		if err != nil {
			return err
		}
		fmt.Printf("rclone source test passed\nList: %s\nObjects: %d\nCompleted manifests: %d\nThis read-only test changed no remote objects.\n",
			measurement.ListDuration.Round(time.Millisecond), measurement.Objects, measurement.CompletedManifests)
		if volumeProof != "" {
			fmt.Println(volumeProof)
		}
		return nil
	case backupcore.CopyEndpointLocal:
		if err := testLocalCopyEndpoints(job); err != nil {
			return err
		}
		if volumeProof != "" {
			fmt.Println(volumeProof)
		}
		return nil
	default:
		return fmt.Errorf("unsupported copy endpoint kind %q", remote.Kind)
	}
}

func parseCopyCLIEndpoint(value, identity, hostKey string, allowEmptyLocal bool) (backupcore.CopyEndpoint, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if allowEmptyLocal {
			return backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal}, nil
		}
		return backupcore.CopyEndpoint{}, fmt.Errorf("endpoint is required")
	}
	lower := strings.ToLower(value)
	switch {
	case strings.HasPrefix(lower, "rclone://"):
		return backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointRclone, Location: value}, nil
	case strings.HasPrefix(lower, "sftp://"), strings.HasPrefix(lower, "ssh://"):
		if strings.TrimSpace(identity) == "" {
			return backupcore.CopyEndpoint{}, fmt.Errorf("SSH/SFTP requires --identity with an absolute dedicated private-key path")
		}
		if strings.TrimSpace(hostKey) == "" {
			return backupcore.CopyEndpoint{}, fmt.Errorf("SSH/SFTP requires --host-key with a pinned SHA256 fingerprint")
		}
		kind := backupcore.CopyEndpointSFTP
		if strings.HasPrefix(lower, "ssh://") {
			kind = backupcore.CopyEndpointSSH
		}
		identityPath, err := resolveBackupCLIPath(identity)
		if err != nil {
			return backupcore.CopyEndpoint{}, fmt.Errorf("resolve SSH identity: %w", err)
		}
		return backupcore.CopyEndpoint{Kind: kind, Location: value, CredentialRef: identityPath, PinnedHostKey: strings.TrimSpace(hostKey)}, nil
	default:
		path, err := resolveBackupCLIPath(value)
		if err != nil {
			return backupcore.CopyEndpoint{}, err
		}
		return backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: path}, nil
	}
}

func copyCLISchedule(value string, mode backupcore.CopyMode, sourceJobID string, every time.Duration, at []string, timezone string) (backupcore.CopyTrigger, backupcore.Schedule, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		switch {
		case every > 0 || len(at) > 0:
			value = string(backupcore.CopyTriggerTimed)
		case mode == backupcore.CopyModePush && sourceJobID != "":
			value = string(backupcore.CopyTriggerAfterSuccess)
		default:
			value = string(backupcore.CopyTriggerManual)
		}
	}
	switch strings.ReplaceAll(value, "-", "_") {
	case "manual":
		if every > 0 || len(at) > 0 {
			return "", backupcore.Schedule{}, fmt.Errorf("manual trigger cannot use --every or --at")
		}
		return backupcore.CopyTriggerManual, backupcore.Schedule{Kind: backupcore.ScheduleManual}, nil
	case "after_success":
		if every > 0 || len(at) > 0 {
			return "", backupcore.Schedule{}, fmt.Errorf("after-success trigger cannot use --every or --at")
		}
		return backupcore.CopyTriggerAfterSuccess, backupcore.Schedule{Kind: backupcore.ScheduleManual}, nil
	case "timed":
		if every > 0 {
			minutes := durationMinutesCeil(every)
			return backupcore.CopyTriggerTimed, backupcore.Schedule{Kind: backupcore.ScheduleInterval, EveryMinutes: minutes, RunMissedOnWake: true}, nil
		}
		if len(at) == 0 {
			return "", backupcore.Schedule{}, fmt.Errorf("timed trigger requires --every or at least one --at HH:MM")
		}
		return backupcore.CopyTriggerTimed, backupcore.Schedule{Kind: backupcore.ScheduleDaily, TimesOfDay: append([]string(nil), at...), Timezone: strings.TrimSpace(timezone), RunMissedOnWake: true}, nil
	default:
		return "", backupcore.Schedule{}, fmt.Errorf("--trigger must be manual, after-success, or timed")
	}
}

func testLocalCopyEndpoints(job backupcore.CopyJob) error {
	paths := []string{job.Source.Location, job.Destination.Location}
	for _, value := range paths {
		if strings.TrimSpace(value) == "" {
			continue
		}
		info, err := os.Lstat(value)
		if err != nil {
			return fmt.Errorf("inspect local copy endpoint %s: %w", value, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("local copy endpoint must be a real directory, not a symlink: %s", value)
		}
	}
	usage, err := backupcore.DestinationDiskUsage(job.Destination.Location)
	if err != nil {
		return err
	}
	fmt.Printf("Local endpoint test passed\nDestination: %s\nVolume: %s\nAvailable: %d bytes\nNo backup files were changed.\n", filepath.Clean(job.Destination.Location), usage.Volume, usage.AvailableBytes)
	return nil
}

func printCopyRun(run backupcore.CopyRun) {
	duration := run.FinishedAt.Sub(run.StartedAt)
	throughput := "n/a"
	if duration > 0 && run.BytesCopied > 0 {
		throughput = formatCopyThroughput(run.BytesCopied, duration)
	}
	fmt.Printf("Copy run %s: %s\n  started %s  duration %s  discovered %d  copied %d  existing %d  bytes %d  throughput %s\n",
		run.ID, run.Status, run.StartedAt.Local().Format(time.RFC3339), duration.Round(time.Millisecond), run.Discovered, len(run.Artifacts), run.AlreadyPresent, run.BytesCopied, throughput)
	if run.Error != "" {
		fmt.Printf("  error %s\n", run.Error)
	}
	if run.RetentionError != "" {
		fmt.Printf("  retention warning %s\n", run.RetentionError)
	}
	for _, warning := range run.Warnings {
		fmt.Printf("  warning %s\n", warning)
	}
}

func formatCopyThroughput(bytes int64, duration time.Duration) string {
	if bytes <= 0 || duration <= 0 {
		return "n/a"
	}
	rate := float64(bytes) / duration.Seconds()
	switch {
	case rate >= 1024*1024:
		return fmt.Sprintf("%.2f MiB/s", rate/(1024*1024))
	case rate >= 1024:
		return fmt.Sprintf("%.2f KiB/s", rate/1024)
	default:
		return fmt.Sprintf("%.0f B/s", rate)
	}
}

func copyEndpointDisplay(endpoint backupcore.CopyEndpoint, sourceJobID string) string {
	if endpoint.Kind == backupcore.CopyEndpointLocal && strings.TrimSpace(endpoint.Location) == "" && strings.TrimSpace(sourceJobID) != "" {
		return "backup job " + sourceJobID
	}
	return endpoint.Location
}

func durationMinutesCeil(value time.Duration) int {
	if value <= 0 {
		return 0
	}
	return int((value + time.Minute - 1) / time.Minute)
}

func durationSecondsCeil(value time.Duration) int {
	if value <= 0 {
		return 0
	}
	return int((value + time.Second - 1) / time.Second)
}

func gibibytesToBytes(value int64) (int64, error) {
	const maxInt64GiB = int64(8589934591)
	if value < 0 || value > maxInt64GiB {
		return 0, fmt.Errorf("--max-total-gib is outside the supported range")
	}
	return value * (1 << 30), nil
}

func printBackupCopyHelp() {
	fmt.Print(`
  dbterm backup copy — independently verified local, push, and pull copies

  USAGE
    dbterm backup copy list [--json]
    dbterm backup copy create --name NAME --mode push|pull --source LOCATION --destination LOCATION [options]
    dbterm backup copy run <copy-id|name>
    dbterm backup copy test <copy-id|name>
    dbterm backup copy status [--json] [copy-id|name]
    dbterm backup copy inspect <copy-id|name> [--artifact ARTIFACT_ID]
      [--identity PATH] [--max-decoded-gib N] [--json]
    dbterm backup copy enable|disable <copy-id|name>
    dbterm backup copy prune [--dry-run|--yes] <copy-id|name>
    dbterm backup copy delete --yes <copy-id|name>

  LOCATIONS
    Local:   absolute path or OS-mounted volume
    SFTP:    sftp://user@host/absolute/path (ssh:// is an alias for SFTP)
    rclone:  rclone://remote/path (pull source only)

  SSH/SFTP requires --identity with a dedicated unencrypted private key and
  --host-key with a pinned SHA256 fingerprint. Password authentication and
  trust-on-first-use are not accepted. SCP shell execution is not used.

  INSPECTION
    copy inspect selects the newest completed, unpruned recovery point unless
    --artifact supplies an exact artifact ID. It downloads remote copies into
    private temporary storage, re-verifies manifest identity, SHA-256, and
    format, then removes the staged files when inspection finishes.

  TIMING
    --source-job ID --trigger after-success
    --trigger timed --at 01:00 --at 13:00 --timezone Asia/Kolkata
    --trigger timed --every 12h

  DESTINATION VOLUME SAFETY (local destinations only)
    --volume-mode already-mounted|os-managed \
      --volume-mount-point /mnt/backup_hdd \
      --volume-id ct400-registration-vault

    --volume-mode managed-linux-block-device \
      --volume-mount-point /mnt/backup_hdd \
      --volume-id ct400-registration-vault \
      --volume-uuid UUID --volume-filesystem ext4 \
      --volume-mount-option errors=remount-ro --volume-spindown

  Every configured volume mode requires an exact sentinel identity at the
  mount root. Managed Linux mode adds rw,nodev,nosuid,noexec and never formats
  or repairs a disk. Mount, unmount, sync, and power-off require separately
  configured narrow host privileges. A failed identity check stops before copy.

  Jobs are disabled by default. Run the read-only endpoint test, then a
  supervised real copy. Timed and after-success triggers stay blocked until
  that real transfer verifies an artifact under the current transport settings.
  A copy never recreates the database dump. Missing-artifact transfer is
  incremental network work, not an incremental database backup or point-in-time
  recovery.

`)
}
