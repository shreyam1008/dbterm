package main

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	backupcore "github.com/shreyam1008/dbterm/internal/backup"
)

func TestCopyCLIScheduleSupportsSeveralDailyTimes(t *testing.T) {
	trigger, schedule, err := copyCLISchedule("timed", backupcore.CopyModePull, "", 0,
		[]string{"13:00", "01:00", "13:00"}, "Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	if trigger != backupcore.CopyTriggerTimed || schedule.Kind != backupcore.ScheduleDaily || !schedule.RunMissedOnWake {
		t.Fatalf("schedule = %+v, trigger = %s", schedule, trigger)
	}
	times, err := schedule.WallClockTimes()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(times, []string{"01:00", "13:00"}) {
		t.Fatalf("wall-clock times = %v", times)
	}
}

func TestCopyCLIScheduleInfersAfterSuccessForBoundPush(t *testing.T) {
	trigger, schedule, err := copyCLISchedule("", backupcore.CopyModePush, "backup_job", 0, nil, "Local")
	if err != nil {
		t.Fatal(err)
	}
	if trigger != backupcore.CopyTriggerAfterSuccess || schedule.Kind != backupcore.ScheduleManual {
		t.Fatalf("trigger = %s, schedule = %+v", trigger, schedule)
	}
}

func TestCopyCLIScheduleRejectsAmbiguousTiming(t *testing.T) {
	_, _, err := copyCLISchedule("manual", backupcore.CopyModePull, "", 0, []string{"02:30"}, "UTC")
	if err == nil || !strings.Contains(err.Error(), "manual trigger") {
		t.Fatalf("error = %v", err)
	}
	_, _, err = copyCLISchedule("timed", backupcore.CopyModePull, "", 0, nil, "UTC")
	if err == nil || !strings.Contains(err.Error(), "requires") {
		t.Fatalf("error = %v", err)
	}
}

func TestCopyCLICreateRejectsImmediateEnableBeforeOpeningCatalog(t *testing.T) {
	err := backupCopyCreateCommand([]string{"--enable"})
	if err == nil || !strings.Contains(err.Error(), "real transfer") || !strings.Contains(err.Error(), "copy test") {
		t.Fatalf("copy create --enable error = %v", err)
	}
}

func TestParseCopyCLIEndpointRequiresPinnedKeyMaterial(t *testing.T) {
	identity := filepath.Join(t.TempDir(), "id_ed25519")
	if _, err := parseCopyCLIEndpoint("sftp://backup@example.test/srv/vault", "", "SHA256:any", false); err == nil || !strings.Contains(err.Error(), "--identity") {
		t.Fatalf("missing identity error = %v", err)
	}
	if _, err := parseCopyCLIEndpoint("sftp://backup@example.test/srv/vault", identity, "", false); err == nil || !strings.Contains(err.Error(), "--host-key") {
		t.Fatalf("missing host-key error = %v", err)
	}
}

func TestDurationMinutesCeil(t *testing.T) {
	for _, test := range []struct {
		input time.Duration
		want  int
	}{{0, 0}, {time.Second, 1}, {time.Minute, 1}, {time.Minute + time.Second, 2}, {12 * time.Hour, 720}} {
		if got := durationMinutesCeil(test.input); got != test.want {
			t.Fatalf("durationMinutesCeil(%s) = %d, want %d", test.input, got, test.want)
		}
	}
}

func TestCopyCLIDestinationVolumeMapsManagedLinuxSettings(t *testing.T) {
	mountPoint := filepath.Join(t.TempDir(), "vault")
	volume, err := copyCLIDestinationVolume(
		"managed-linux-block-device", mountPoint, ".vault-id", "ct400-vault-identity",
		"ABCD-1234", "EXT4", "BACKUP", []string{"errors=remount-ro"},
		1500*time.Millisecond, 2500*time.Millisecond, true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if volume == nil || volume.Mode != backupcore.CopyVolumeManagedLinuxBlockDevice || volume.MountPoint != filepath.Clean(mountPoint) ||
		volume.WarmupSeconds != 2 || volume.CooldownSeconds != 3 || !volume.Spindown {
		t.Fatalf("CLI volume = %#v", volume)
	}
	job := backupcore.CopyJob{
		Name: "managed vault", Mode: backupcore.CopyModePull,
		Source:            backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: t.TempDir()},
		Destination:       backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(mountPoint, "copies")},
		DestinationVolume: volume, Trigger: backupcore.CopyTriggerManual,
	}
	if err := job.ApplyDefaults(time.Now()); err != nil {
		t.Fatalf("CLI-produced managed volume did not pass core validation: %v", err)
	}
	for _, required := range []string{"rw", "nodev", "nosuid", "noexec"} {
		if !containsString(job.DestinationVolume.MountOptions, required) {
			t.Fatalf("normalized CLI mount options %v omit %q", job.DestinationVolume.MountOptions, required)
		}
	}
	display := copyCLIVolumeDisplay(*job.DestinationVolume)
	for _, want := range []string{"managed_linux_block_device", filepath.Clean(mountPoint), "ABCD-1234", "ext4", "BACKUP", "spindown"} {
		if !strings.Contains(strings.ToLower(display), strings.ToLower(want)) {
			t.Fatalf("volume display %q omits %q", display, want)
		}
	}
	if strings.Contains(display, "ct400-vault-identity") {
		t.Fatalf("volume display exposes full sentinel identity: %q", display)
	}
}

func TestCopyCLIDestinationVolumeIsOptionalButRejectsPartialOrIrrelevantSettings(t *testing.T) {
	volume, err := copyCLIDestinationVolume("", "", "", "", "", "", "", nil, 0, 0, false)
	if err != nil || volume != nil {
		t.Fatalf("empty optional volume = %#v, %v", volume, err)
	}
	tests := []struct {
		name       string
		mode       string
		mountPoint string
		identity   string
		warmup     time.Duration
		want       string
	}{
		{name: "settings without mode", mountPoint: t.TempDir(), identity: "vault-identity", want: "--volume-mode is required"},
		{name: "mode without identity", mode: "os-managed", mountPoint: t.TempDir(), want: "--volume-id"},
		{name: "unknown mode", mode: "auto-format", mountPoint: t.TempDir(), identity: "vault-identity", want: "must be already-mounted"},
		{name: "negative warmup", mode: "already-mounted", mountPoint: t.TempDir(), identity: "vault-identity", warmup: -time.Second, want: "cannot be negative"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := copyCLIDestinationVolume(test.mode, test.mountPoint, "", test.identity, "", "", "", nil, test.warmup, 0, false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("copyCLIDestinationVolume() error = %v, want %q", err, test.want)
			}
		})
	}

	verifyOnly, err := copyCLIDestinationVolume("already-mounted", t.TempDir(), "", "vault-identity", "uuid-not-allowed", "", "", nil, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	job := backupcore.CopyJob{
		Name: "verify only", Mode: backupcore.CopyModePull,
		Source:            backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: t.TempDir()},
		Destination:       backupcore.CopyEndpoint{Kind: backupcore.CopyEndpointLocal, Location: filepath.Join(verifyOnly.MountPoint, "copies")},
		DestinationVolume: verifyOnly, Trigger: backupcore.CopyTriggerManual,
	}
	if err := job.ApplyDefaults(time.Now()); err == nil || !strings.Contains(err.Error(), "verify-only") {
		t.Fatalf("irrelevant verify-only Linux setting error = %v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestSelectCopyArtifactForInspectionIsDeterministic(t *testing.T) {
	base := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	old := copyCLIInspectionArtifact("artifact_old", base, backupcore.ArtifactPublicationComplete)
	tiedZ := copyCLIInspectionArtifact("artifact_z", base.Add(time.Hour), backupcore.ArtifactPublicationComplete)
	tiedA := copyCLIInspectionArtifact("artifact_a", base.Add(time.Hour), backupcore.ArtifactPublicationComplete)
	pruned := copyCLIInspectionArtifact("artifact_pruned", base.Add(3*time.Hour), backupcore.ArtifactPublicationComplete)
	pruned.PrunedAt = base.Add(4 * time.Hour)
	pruned.PruneReason = "retention"
	incomplete := copyCLIInspectionArtifact("artifact_incomplete", base.Add(5*time.Hour), backupcore.ArtifactPublicationArtifactOnly)

	orders := [][]backupcore.CopyRun{
		{{Artifacts: []backupcore.CopyArtifactResult{old, tiedZ}}, {Artifacts: []backupcore.CopyArtifactResult{pruned, incomplete, tiedA}}},
		{{Artifacts: []backupcore.CopyArtifactResult{tiedA, incomplete}}, {Artifacts: []backupcore.CopyArtifactResult{tiedZ, old, pruned}}},
	}
	for index, runs := range orders {
		selected, err := selectCopyArtifactForInspection(runs, "")
		if err != nil {
			t.Fatalf("order %d: %v", index, err)
		}
		if selected.ArtifactID != "artifact_a" {
			t.Fatalf("order %d selected %q, want deterministic tie-break artifact_a", index, selected.ArtifactID)
		}
	}

	selected, err := selectCopyArtifactForInspection(orders[0], "artifact_old")
	if err != nil {
		t.Fatal(err)
	}
	if selected.ArtifactID != "artifact_old" {
		t.Fatalf("exact selection = %q", selected.ArtifactID)
	}

	// An interrupted artifact-only publication can be reconciled by a later
	// run. The one completed record is authoritative, not ambiguous with its
	// earlier incomplete history.
	reconciled := copyCLIInspectionArtifact("artifact_reconciled", base.Add(2*time.Hour), backupcore.ArtifactPublicationComplete)
	orphan := reconciled
	orphan.PublicationState = backupcore.ArtifactPublicationArtifactOnly
	selected, err = selectCopyArtifactForInspection([]backupcore.CopyRun{{Artifacts: []backupcore.CopyArtifactResult{orphan}}, {Artifacts: []backupcore.CopyArtifactResult{reconciled}}}, "artifact_reconciled")
	if err != nil || selected.PublicationState != backupcore.ArtifactPublicationComplete {
		t.Fatalf("reconciled selection = %+v, %v", selected, err)
	}
}

func TestSelectCopyArtifactForInspectionRejectsUnavailableAndAmbiguousArtifacts(t *testing.T) {
	base := time.Date(2026, 9, 3, 1, 0, 0, 0, time.UTC)
	pruned := copyCLIInspectionArtifact("artifact_pruned", base, backupcore.ArtifactPublicationComplete)
	pruned.PrunedAt = base.Add(time.Hour)
	pruned.PruneReason = "retention"
	incomplete := copyCLIInspectionArtifact("artifact_incomplete", base, backupcore.ArtifactPublicationArtifactOnly)
	runs := []backupcore.CopyRun{{Artifacts: []backupcore.CopyArtifactResult{pruned, incomplete}}}

	if _, err := selectCopyArtifactForInspection(runs, ""); err == nil || !strings.Contains(err.Error(), "no unpruned completed") {
		t.Fatalf("no-completed error = %v", err)
	}
	if _, err := selectCopyArtifactForInspection(runs, "artifact_pruned"); err == nil || !strings.Contains(err.Error(), "pruned") {
		t.Fatalf("pruned error = %v", err)
	}
	if _, err := selectCopyArtifactForInspection(runs, "artifact_incomplete"); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete error = %v", err)
	}
	if _, err := selectCopyArtifactForInspection(runs, "artifact_missing"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing error = %v", err)
	}

	duplicate := copyCLIInspectionArtifact("artifact_duplicate", base, backupcore.ArtifactPublicationComplete)
	duplicateRuns := []backupcore.CopyRun{{Artifacts: []backupcore.CopyArtifactResult{duplicate}}, {Artifacts: []backupcore.CopyArtifactResult{duplicate}}}
	if _, err := selectCopyArtifactForInspection(duplicateRuns, "artifact_duplicate"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("explicit ambiguity error = %v", err)
	}
	if _, err := selectCopyArtifactForInspection(duplicateRuns, ""); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("default ambiguity error = %v", err)
	}
}

func copyCLIInspectionArtifact(id string, created time.Time, state backupcore.ArtifactPublicationState) backupcore.CopyArtifactResult {
	return backupcore.CopyArtifactResult{
		ArtifactID: id, SourceCreatedAt: created, VerifiedAt: created.Add(time.Minute),
		Source: "/producer/" + id, Destination: "/vault/" + id, PublicationState: state,
	}
}

func TestFormatCopyThroughputUsesReadableUnits(t *testing.T) {
	for _, test := range []struct {
		name     string
		bytes    int64
		duration time.Duration
		want     string
	}{
		{name: "bytes", bytes: 600, duration: 2 * time.Second, want: "300 B/s"},
		{name: "kibibytes", bytes: 4 * 1024, duration: 2 * time.Second, want: "2.00 KiB/s"},
		{name: "mebibytes", bytes: 4 * 1024 * 1024, duration: 2 * time.Second, want: "2.00 MiB/s"},
		{name: "missing", bytes: 0, duration: time.Second, want: "n/a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := formatCopyThroughput(test.bytes, test.duration); got != test.want {
				t.Fatalf("formatCopyThroughput() = %q, want %q", got, test.want)
			}
		})
	}
}
