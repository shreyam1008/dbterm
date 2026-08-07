package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shreyam1008/dbterm/internal/appdirs"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
)

func TestValidateAndCompactPurgeTargets(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "config", "dbterm")
	stateDir := filepath.Join(root, "state", "dbterm")
	paths := uninstallDataPaths{
		Config: configDir,
		State:  stateDir,
		Logs:   filepath.Join(stateDir, "logs"),
	}

	roots, err := validateAndCompactPurgeTargets(paths, filepath.Join(root, "bin", "dbterm"))
	if err != nil {
		t.Fatalf("validate purge targets: %v", err)
	}
	if len(roots) != 2 {
		t.Fatalf("expected nested logs to be compacted into two roots, got %v", roots)
	}
	if !samePath(roots[0], configDir) && !samePath(roots[1], configDir) {
		t.Fatalf("config root missing from %v", roots)
	}
	if !samePath(roots[0], stateDir) && !samePath(roots[1], stateDir) {
		t.Fatalf("state root missing from %v", roots)
	}
}

func TestValidatePurgeTargetRejectsBroadOrCustomLeaf(t *testing.T) {
	for _, target := range []string{string(os.PathSeparator), t.TempDir(), filepath.Join(t.TempDir(), "settings")} {
		if err := validatePurgeTarget(target); err == nil {
			t.Fatalf("expected unsafe target %q to be rejected", target)
		}
	}
}

func TestValidateOverridePurgeOwnershipRequiresMarker(t *testing.T) {
	override := filepath.Join(t.TempDir(), "shared", "dbterm")
	if err := os.MkdirAll(override, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(override, "unrelated.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := validateOverridePurgeOwnership(uninstallDataPaths{Config: override, ConfigOverridden: true})
	if err == nil || !strings.Contains(err.Error(), "no dbterm ownership marker") {
		t.Fatalf("validateOverridePurgeOwnership() error = %v, want missing-marker refusal", err)
	}
}

func TestValidateOverridePurgeOwnershipAcceptsMarkedOverride(t *testing.T) {
	override := filepath.Join(t.TempDir(), "custom", "dbterm")
	t.Setenv("DBTERM_CONFIG_DIR", override)
	resolved, err := appdirs.ConfigDir()
	if err != nil {
		t.Fatalf("ConfigDir() error = %v", err)
	}

	err = validateOverridePurgeOwnership(uninstallDataPaths{Config: resolved, ConfigOverridden: true})
	if err != nil {
		t.Fatalf("validateOverridePurgeOwnership() error = %v", err)
	}
}

func TestValidateOverridePurgeOwnershipDoesNotRequireMarkerForNativePath(t *testing.T) {
	native := filepath.Join(t.TempDir(), "native", "dbterm")
	if err := os.MkdirAll(native, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateOverridePurgeOwnership(uninstallDataPaths{Config: native}); err != nil {
		t.Fatalf("native directory should preserve backward-compatible purge behavior: %v", err)
	}
}

func TestProtectConfiguredBackupArtifactsRefusesDestinationInsidePurgeRoot(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state", "dbterm")
	destination := filepath.Join(stateDir, "user-backups")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(stateDir, "backup", "backups.db")
	store, err := backupcore.OpenStore(catalogPath)
	if err != nil {
		t.Fatal(err)
	}
	job := backupcore.Job{
		Name:         "inside state",
		ConnectionID: "connection-1",
		Destination:  destination,
		Compression:  backupcore.CompressionNone,
		Schedule:     backupcore.Schedule{Kind: backupcore.ScheduleManual},
		Retention:    backupcore.Retention{KeepLast: 1},
	}
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	err = protectConfiguredBackupArtifacts(
		uninstallDataPaths{State: stateDir},
		[]string{stateDir},
	)
	if err == nil || !strings.Contains(err.Error(), "refusing purge") {
		t.Fatalf("expected protected destination refusal, got %v", err)
	}
}

func TestProtectConfiguredBackupArtifactsAllowsExternalDestination(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state", "dbterm")
	destination := filepath.Join(root, "external-backups")
	if err := os.MkdirAll(destination, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := backupcore.OpenStore(filepath.Join(stateDir, "backup", "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	job := backupcore.Job{
		Name:         "external",
		ConnectionID: "connection-1",
		Destination:  destination,
		Compression:  backupcore.CompressionNone,
		Schedule:     backupcore.Schedule{Kind: backupcore.ScheduleManual},
		Retention:    backupcore.Retention{KeepLast: 1},
	}
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if err := protectConfiguredBackupArtifacts(uninstallDataPaths{State: stateDir}, []string{stateDir}); err != nil {
		t.Fatalf("external destination should be preserved without blocking purge: %v", err)
	}
}

func TestProtectConfiguredBackupArtifactsFindsHistoryAfterJobDeletion(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state", "dbterm")
	externalDestination := filepath.Join(root, "external-backups")
	artifactPath := filepath.Join(stateDir, "preserve", "database.sql.zst")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifactPath, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := backupcore.OpenStore(filepath.Join(stateDir, "backup", "backups.db"))
	if err != nil {
		t.Fatal(err)
	}
	job := backupcore.Job{
		Name:         "deleted later",
		ConnectionID: "connection-1",
		Destination:  externalDestination,
		Compression:  backupcore.CompressionNone,
		Schedule:     backupcore.Schedule{Kind: backupcore.ScheduleManual},
		Retention:    backupcore.Retention{KeepLast: 1},
	}
	if err := store.UpsertJob(context.Background(), &job); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	const owner = "uninstall-test"
	if _, err := store.ClaimJob(context.Background(), job.ID, owner, time.Now()); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	run, err := store.StartRun(context.Background(), job.ID, backupcore.TriggerManual, time.Now())
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	run.Artifact = backupcore.Artifact{Path: artifactPath, Verified: true, CreatedAt: time.Now()}
	if err := store.FinishRun(context.Background(), &run, owner); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.DeleteJob(context.Background(), job.ID); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	err = protectConfiguredBackupArtifacts(uninstallDataPaths{State: stateDir}, []string{stateDir})
	if err == nil || !strings.Contains(err.Error(), "backup artifact") {
		t.Fatalf("expected catalog history to protect the artifact after job deletion, got %v", err)
	}
}

func TestProtectConfiguredBackupArtifactsFindsUncataloguedInstantBackup(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state", "dbterm")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(stateDir, "instant-backup.sql")
	if err := os.WriteFile(backupPath, []byte("CREATE TABLE users (id INTEGER);\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := protectConfiguredBackupArtifacts(uninstallDataPaths{State: stateDir}, []string{stateDir})
	if err == nil || !strings.Contains(err.Error(), "uncatalogued backup artifact") {
		t.Fatalf("expected uncatalogued instant backup to block purge, got %v", err)
	}
}

func TestProtectConfiguredBackupArtifactsAllowsPrivateCrashStage(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state", "dbterm")
	stageDir := filepath.Join(stateDir, "backup", "staging", "stage-crashed")
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "native.dump"), []byte("PGDMP internal stage"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := protectConfiguredBackupArtifacts(uninstallDataPaths{State: stateDir}, []string{stateDir}); err != nil {
		t.Fatalf("dbterm-owned crash staging should not block explicit purge: %v", err)
	}
}

func TestProtectConfiguredBackupArtifactsRefusesPrivateAgeIdentity(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config", "dbterm")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(configDir, "age-identity.txt")
	contents := []byte("# recovery key\nAGE-SECRET-KEY-1EXAMPLE\n")
	if err := os.WriteFile(identityPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	err := protectConfiguredBackupArtifacts(
		uninstallDataPaths{State: filepath.Join(t.TempDir(), "state", "dbterm")},
		[]string{configDir},
	)
	if err == nil || !strings.Contains(err.Error(), "private age identity") {
		t.Fatalf("expected private age identity to block purge, got %v", err)
	}
}

func TestProtectConfiguredBackupArtifactsAllowsOwnedJSONWithoutCatalog(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config", "dbterm")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	connections := []byte("{\"name\":\"CREATE TABLE is text in a connection label\"}\n")
	if err := os.WriteFile(filepath.Join(configDir, "connections.json"), connections, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := protectConfiguredBackupArtifacts(uninstallDataPaths{State: filepath.Join(t.TempDir(), "state", "dbterm")}, []string{configDir}); err != nil {
		t.Fatalf("ordinary dbterm JSON should not block purge: %v", err)
	}
}
