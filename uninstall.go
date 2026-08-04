package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/appdirs"
	backupcore "github.com/shreyam1008/dbterm/internal/backup"
)

// ── CLI: --uninstall ──

func runUninstall(purge bool, assumeYes bool) error {
	paths := uninstallDataPaths{Config: configDir()}
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not locate executable path: %w", err)
	}
	exePath = filepath.Clean(exePath)

	if err := validateUninstallTarget(exePath); err != nil {
		return err
	}
	var purgeRoots []string
	if purge {
		paths, err = resolveUninstallDataPaths()
		if err != nil {
			return err
		}
		if err := validateOverridePurgeOwnership(paths); err != nil {
			return err
		}
		purgeRoots, err = validateAndCompactPurgeTargets(paths, exePath)
		if err != nil {
			return err
		}
	}
	if err := confirmUninstall(exePath, paths, purge, assumeYes); err != nil {
		return err
	}

	fmt.Print("\n  \033[1;38;2;203;166;247mdbterm\033[0m — Uninstall\n")
	agent, registrationRemoved, err := unregisterBackupAgentForUninstall()
	if err != nil {
		return err
	}
	if registrationRemoved {
		fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Removed backup agent registration (%s)\n", agent.status.Manager)
	}
	if purge {
		if err := protectConfiguredBackupArtifacts(paths, purgeRoots); err != nil {
			if restoreErr := restoreBackupAgentRegistration(agent, registrationRemoved); restoreErr != nil {
				return fmt.Errorf("%w; additionally could not restore the backup agent registration: %v", err, restoreErr)
			}
			return err
		}
	}

	if runtime.GOOS == "windows" {
		if err := runWindowsUninstall(exePath, purgeRoots, purge); err != nil {
			if restoreErr := restoreBackupAgentRegistration(agent, registrationRemoved); restoreErr != nil {
				return fmt.Errorf("%w; additionally could not restore the backup agent registration: %v", err, restoreErr)
			}
			return err
		}
		return nil
	}

	if err := os.Remove(exePath); err != nil {
		if restoreErr := restoreBackupAgentRegistration(agent, registrationRemoved); restoreErr != nil {
			return fmt.Errorf("could not remove binary %s: %w; additionally could not restore the backup agent registration: %v", exePath, err, restoreErr)
		}
		if os.IsPermission(err) {
			return fmt.Errorf("permission denied removing %s. Ask the installer, package manager, or an administrator to remove only that binary; keep backup-service and data-cleanup operations in your normal user account and do not rerun the whole uninstall with sudo", exePath)
		}
		return fmt.Errorf("could not remove binary %s: %w", exePath, err)
	}
	fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Removed binary: %s\n", exePath)

	if purge {
		for _, target := range purgeRoots {
			if err := os.RemoveAll(target); err != nil {
				return fmt.Errorf("removed binary but failed to remove dbterm data %s: %w", target, err)
			}
			fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Removed dbterm data: %s\n", target)
		}
		fmt.Println("  \033[33mInfo:\033[0m Backup destination folders and artifacts were not removed.")
	} else {
		fmt.Printf("  \033[33mInfo:\033[0m Kept config and backup jobs: %s\n", paths.Config)
		fmt.Println("  Use dbterm --uninstall --purge to remove dbterm-owned config, state, and logs.")
	}

	fmt.Println("  \033[38;2;166;227;161m✓\033[0m Uninstall complete.")
	fmt.Println()
	return nil
}

type uninstallDataPaths struct {
	Config           string
	State            string
	Logs             string
	ConfigOverridden bool
	StateOverridden  bool
	LogsOverridden   bool
}

func resolveUninstallDataPaths() (uninstallDataPaths, error) {
	configDir, err := appdirs.ConfigDir()
	if err != nil {
		return uninstallDataPaths{}, fmt.Errorf("resolve config directory for purge: %w", err)
	}
	stateDir, err := appdirs.StateDir()
	if err != nil {
		return uninstallDataPaths{}, fmt.Errorf("resolve state directory for purge: %w", err)
	}
	logDir, err := appdirs.LogDir()
	if err != nil {
		return uninstallDataPaths{}, fmt.Errorf("resolve log directory for purge: %w", err)
	}
	return uninstallDataPaths{
		Config:           configDir,
		State:            stateDir,
		Logs:             logDir,
		ConfigOverridden: directoryOverrideActive("DBTERM_CONFIG_DIR"),
		StateOverridden:  directoryOverrideActive("DBTERM_STATE_DIR"),
		LogsOverridden:   directoryOverrideActive("DBTERM_LOG_DIR"),
	}, nil
}

func directoryOverrideActive(name string) bool {
	value, present := os.LookupEnv(name)
	return present && strings.TrimSpace(value) != ""
}

func validateOverridePurgeOwnership(paths uninstallDataPaths) error {
	targets := []struct {
		label      string
		path       string
		overridden bool
	}{
		{label: "config", path: paths.Config, overridden: paths.ConfigOverridden},
		{label: "state", path: paths.State, overridden: paths.StateOverridden},
		{label: "logs", path: paths.Logs, overridden: paths.LogsOverridden},
	}
	checked := make(map[string]bool, len(targets))
	for _, target := range targets {
		if !target.overridden {
			continue
		}
		clean := filepath.Clean(target.path)
		key := clean
		if runtime.GOOS == "windows" {
			key = strings.ToLower(key)
		}
		if checked[key] {
			continue
		}
		checked[key] = true
		owned, err := appdirs.IsOwnedDirectory(clean)
		if err != nil {
			return fmt.Errorf("verify overridden dbterm %s directory before purge: %w", target.label, err)
		}
		if !owned {
			return fmt.Errorf("refusing purge of overridden dbterm %s directory %s because it has no dbterm ownership marker; the directory remains usable, but review and remove it manually if it contains only dbterm data", target.label, clean)
		}
	}
	return nil
}

func unregisterBackupAgentForUninstall() (backupAgentLifecycle, bool, error) {
	state, err := inspectBackupAgentLifecycle(15 * time.Second)
	if err != nil {
		// Linux builds also run on distributions without systemd. Status has
		// already checked the managed unit path, so absence of that definition
		// is enough to continue a normal uninstall there. Other supported OSes
		// fail closed because a running task could keep the binary in use.
		if (runtime.GOOS == "linux" && !state.status.Installed) || (runtime.GOOS != "linux" && runtime.GOOS != "darwin" && runtime.GOOS != "windows") {
			if processErr := ensureBackupAgentProcessStopped("uninstall dbterm"); processErr != nil {
				return state, false, processErr
			}
			fmt.Printf("  \033[33mWarning:\033[0m could not inspect a native backup agent: %v\n", err)
			return state, false, nil
		}
		return state, false, fmt.Errorf("could not inspect the backup agent before uninstall: %w", err)
	}
	if !state.status.Running {
		if err := ensureBackupAgentProcessStopped("uninstall dbterm"); err != nil {
			return state, false, err
		}
	}
	if !state.status.Installed && !state.status.Running {
		return state, false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := state.manager.Uninstall(ctx); err != nil {
		return state, false, fmt.Errorf("could not stop and unregister the backup agent: %w", err)
	}
	if err := waitForBackupAgentProcessExit(backupAgentExitTimeout); err != nil {
		if restoreErr := restoreBackupAgentRegistration(state, true); restoreErr != nil {
			return state, false, fmt.Errorf("could not drain the backup agent before uninstall: %w; additionally could not restore its native registration: %v", err, restoreErr)
		}
		return state, false, fmt.Errorf("could not drain the backup agent before uninstall: %w; its native registration was restored", err)
	}
	return state, true, nil
}

func restoreBackupAgentRegistration(state backupAgentLifecycle, registrationRemoved bool) error {
	if !registrationRemoved || state.manager == nil {
		return nil
	}
	installCtx, cancelInstall := context.WithTimeout(context.Background(), 30*time.Second)
	err := state.manager.Install(installCtx)
	cancelInstall()
	if err != nil {
		return err
	}
	if state.status.Running {
		return nil
	}
	// Install intentionally starts a newly registered agent. Preserve an
	// originally stopped registration when rolling back a failed uninstall.
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelStop()
	return state.manager.Stop(stopCtx)
}

type protectedBackupPath struct {
	label string
	path  string
}

// protectConfiguredBackupArtifacts refuses a purge if any configured backup
// destination or catalog-recorded artifact lives inside a dbterm-owned data
// root. This keeps --purge scoped to configuration, state, and logs even when
// a user deliberately chose an unusual destination.
func protectConfiguredBackupArtifacts(paths uninstallDataPaths, purgeRoots []string) error {
	catalogPath := filepath.Join(paths.State, "backup", "backups.db")
	_, catalogErr := os.Stat(catalogPath)
	if catalogErr != nil {
		if !os.IsNotExist(catalogErr) {
			return fmt.Errorf("inspect backup catalog before purge: %w", catalogErr)
		}
		return protectUncataloguedBackupArtifacts(purgeRoots, catalogPath)
	}

	db, err := sql.Open("sqlite", catalogPath)
	if err != nil {
		return fmt.Errorf("open backup catalog read-only before purge: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return fmt.Errorf("protect backup catalog from writes during purge check: %w", err)
	}

	protected := make([]protectedBackupPath, 0)
	jobRows, err := db.QueryContext(ctx, "SELECT job_json FROM backup_jobs")
	if err != nil {
		return fmt.Errorf("read backup jobs before purge: %w", err)
	}
	for jobRows.Next() {
		var payload []byte
		if err := jobRows.Scan(&payload); err != nil {
			_ = jobRows.Close()
			return fmt.Errorf("read backup job before purge: %w", err)
		}
		var job backupcore.Job
		if err := json.Unmarshal(payload, &job); err != nil {
			_ = jobRows.Close()
			return fmt.Errorf("decode backup job before purge: %w", err)
		}
		if strings.TrimSpace(job.Destination) != "" {
			protected = append(protected, protectedBackupPath{label: fmt.Sprintf("backup job %q destination", job.Name), path: job.Destination})
		}
	}
	if err := jobRows.Err(); err != nil {
		_ = jobRows.Close()
		return fmt.Errorf("read backup jobs before purge: %w", err)
	}
	_ = jobRows.Close()

	runRows, err := db.QueryContext(ctx, "SELECT run_json FROM backup_runs")
	if err != nil {
		return fmt.Errorf("read backup history before purge: %w", err)
	}
	for runRows.Next() {
		var payload []byte
		if err := runRows.Scan(&payload); err != nil {
			_ = runRows.Close()
			return fmt.Errorf("read backup run before purge: %w", err)
		}
		var run backupcore.Run
		if err := json.Unmarshal(payload, &run); err != nil {
			_ = runRows.Close()
			return fmt.Errorf("decode backup run before purge: %w", err)
		}
		if strings.TrimSpace(run.Artifact.Path) != "" {
			protected = append(protected, protectedBackupPath{label: fmt.Sprintf("backup artifact from run %s", run.ID), path: run.Artifact.Path})
		}
	}
	if err := runRows.Err(); err != nil {
		_ = runRows.Close()
		return fmt.Errorf("read backup history before purge: %w", err)
	}
	_ = runRows.Close()

	for _, candidate := range protected {
		resolved, exists, err := resolveExistingProtectedPath(candidate.path)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", candidate.label, err)
		}
		if !exists {
			continue
		}
		checks := []string{resolved}
		if evaluated, err := filepath.EvalSymlinks(resolved); err == nil && !samePath(evaluated, resolved) {
			checks = append(checks, evaluated)
		}
		for _, check := range checks {
			for _, root := range purgeRoots {
				if pathWithin(check, root) {
					return fmt.Errorf("refusing purge: %s (%s) is inside %s; move that destination/artifact outside dbterm's data directories or uninstall without --purge", candidate.label, check, root)
				}
			}
		}
	}
	return protectUncataloguedBackupArtifacts(purgeRoots, catalogPath)
}

func protectUncataloguedBackupArtifacts(purgeRoots []string, catalogPath string) error {
	privateStagingRoot := filepath.Join(filepath.Dir(catalogPath), "staging")
	for _, root := range purgeRoots {
		if _, err := os.Lstat(root); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("inspect dbterm data directory before purge: %w", err)
		}
		walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				// This tree contains dbterm-owned plaintext crash staging, not
				// completed user artifacts. An explicit purge may remove it; treating
				// native.sql/native.dump as an uncatalogued backup would otherwise
				// make recovery from a crashed run impossible to uninstall cleanly.
				if samePath(path, privateStagingRoot) {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if samePath(path, catalogPath) || samePath(path, catalogPath+"-wal") || samePath(path, catalogPath+"-shm") {
				return nil
			}
			privateIdentity, err := looksLikePrivateAgeIdentity(path)
			if err != nil {
				return err
			}
			if privateIdentity {
				return fmt.Errorf("refusing purge: private age identity found at %s; move this recovery key outside dbterm's data directories or remove it explicitly", path)
			}
			looksLikeBackup, err := looksLikeBackupArtifact(path)
			if err != nil {
				return err
			}
			if looksLikeBackup {
				return fmt.Errorf("refusing purge: possible uncatalogued backup artifact found at %s; move it outside dbterm's data directories or remove it explicitly", path)
			}
			return nil
		})
		if walkErr != nil {
			return walkErr
		}
	}
	return nil
}

func looksLikePrivateAgeIdentity(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("inspect possible age identity %s: %w", path, err)
	}
	defer file.Close()
	buffer := make([]byte, 64*1024)
	count, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("inspect possible age identity %s: %w", path, err)
	}
	return bytes.Contains(buffer[:count], []byte("AGE-SECRET-KEY-1")), nil
}

func looksLikeBackupArtifact(path string) (bool, error) {
	extension := strings.ToLower(filepath.Ext(path))
	switch extension {
	case ".sql", ".dump", ".backup", ".bak", ".db", ".sqlite", ".sqlite3", ".db3", ".gz", ".zip", ".zst", ".zstd", ".age", ".enc", ".tar":
		return true, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return false, fmt.Errorf("inspect possible backup artifact %s: %w", path, err)
	}
	defer file.Close()
	buffer := make([]byte, 64*1024)
	count, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("inspect possible backup artifact %s: %w", path, err)
	}
	peek := buffer[:count]
	if bytes.HasPrefix(peek, []byte("PGDMP")) ||
		bytes.HasPrefix(peek, []byte("SQLite format 3\x00")) ||
		bytes.HasPrefix(peek, []byte{0x1f, 0x8b}) ||
		bytes.HasPrefix(peek, []byte{0x28, 0xb5, 0x2f, 0xfd}) ||
		bytes.HasPrefix(peek, []byte("PK\x03\x04")) ||
		bytes.HasPrefix(peek, []byte("age-encryption.org/v1")) ||
		bytes.HasPrefix(peek, []byte("-----BEGIN AGE ENCRYPTED FILE-----")) ||
		(len(peek) > 262 && bytes.Equal(peek[257:262], []byte("ustar"))) {
		return true, nil
	}
	text := strings.ToLower(strings.TrimSpace(string(peek)))
	for _, prefix := range []string{
		"-- postgresql database dump", "-- mysql dump", "pragma foreign_keys", "begin transaction",
		"create table", "create database", "insert into", "copy ",
	} {
		if strings.HasPrefix(text, prefix) {
			return true, nil
		}
	}
	return false, nil
}

func resolveExistingProtectedPath(raw string) (string, bool, error) {
	value := strings.TrimSpace(raw)
	if value == "~" || strings.HasPrefix(value, "~/") || strings.HasPrefix(value, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false, err
		}
		if value == "~" {
			value = home
		} else {
			value = filepath.Join(home, value[2:])
		}
	}
	resolved, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", false, err
	}
	if _, err := os.Lstat(resolved); err != nil {
		if os.IsNotExist(err) {
			return resolved, false, nil
		}
		return "", false, err
	}
	return resolved, true, nil
}

// ── Validation helpers ──

func validateUninstallTarget(exePath string) error {
	base := strings.ToLower(filepath.Base(exePath))
	if base != "dbterm" && base != "dbterm.exe" {
		return fmt.Errorf("refusing to remove unexpected executable path: %s", exePath)
	}

	info, err := os.Stat(exePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("binary not found: %s", exePath)
		}
		return fmt.Errorf("could not access binary %s: %w", exePath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("refusing to remove directory: %s", exePath)
	}
	return nil
}

func validatePurgeTarget(cfgDir string) error {
	if strings.TrimSpace(cfgDir) == "" {
		return fmt.Errorf("dbterm data path is empty; refusing purge")
	}
	if strings.HasPrefix(cfgDir, "~") {
		return fmt.Errorf("could not resolve dbterm data path (%s); refusing purge", cfgDir)
	}
	clean := filepath.Clean(cfgDir)
	if !filepath.IsAbs(clean) || clean == "." || filepath.Dir(clean) == clean {
		return fmt.Errorf("unsafe dbterm data path for purge: %s", clean)
	}
	base := strings.ToLower(filepath.Base(clean))
	parentBase := strings.ToLower(filepath.Base(filepath.Dir(clean)))
	if base != "dbterm" && !(base == "logs" && parentBase == "dbterm") {
		return fmt.Errorf("unsafe dbterm data path for purge: %s (the directory must end in dbterm, or be dbterm/logs)", clean)
	}
	return nil
}

func validateAndCompactPurgeTargets(paths uninstallDataPaths, exePath string) ([]string, error) {
	candidates := []string{paths.Config, paths.State, paths.Logs}
	for _, candidate := range candidates {
		if err := validatePurgeTarget(candidate); err != nil {
			return nil, err
		}
		clean := filepath.Clean(candidate)
		if home, err := os.UserHomeDir(); err == nil && samePath(clean, home) {
			return nil, fmt.Errorf("refusing to purge the user home directory: %s", clean)
		}
		if cwd, err := os.Getwd(); err == nil && pathWithin(cwd, clean) {
			return nil, fmt.Errorf("refusing to purge %s because it contains the current working directory %s", clean, cwd)
		}
		if pathWithin(exePath, clean) {
			return nil, fmt.Errorf("refusing to purge %s because it contains the dbterm executable %s", clean, exePath)
		}
	}

	unique := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		duplicate := false
		for _, existing := range unique {
			if samePath(candidate, existing) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			unique = append(unique, candidate)
		}
	}
	sort.Slice(unique, func(i, j int) bool { return len(unique[i]) < len(unique[j]) })
	roots := make([]string, 0, len(unique))
	for _, candidate := range unique {
		nested := false
		for _, root := range roots {
			if pathWithin(candidate, root) {
				nested = true
				break
			}
		}
		if !nested {
			roots = append(roots, candidate)
		}
	}
	return roots, nil
}

func samePath(first, second string) bool {
	first = filepath.Clean(first)
	second = filepath.Clean(second)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(first, second)
	}
	return first == second
}

// pathWithin reports whether candidate is root itself or lies below root.
func pathWithin(candidate, root string) bool {
	candidate = filepath.Clean(candidate)
	root = filepath.Clean(root)
	if runtime.GOOS == "windows" {
		candidate = strings.ToLower(candidate)
		root = strings.ToLower(root)
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)))
}

// ── Confirmation prompt ──

func confirmUninstall(exePath string, paths uninstallDataPaths, purge, assumeYes bool) error {
	if assumeYes {
		return nil
	}

	stdinInfo, err := os.Stdin.Stat()
	if err != nil {
		return fmt.Errorf("could not access stdin for confirmation: %w", err)
	}
	if stdinInfo.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("non-interactive input; re-run with --yes to confirm uninstall")
	}

	fmt.Print("\n  \033[1;38;2;203;166;247mdbterm\033[0m — Confirm Uninstall\n")
	fmt.Printf("  Binary        %s\n", exePath)
	if purge {
		fmt.Printf("  Config        %s (will be deleted)\n", paths.Config)
		fmt.Printf("  State         %s (will be deleted)\n", paths.State)
		fmt.Printf("  Logs          %s (will be deleted)\n", paths.Logs)
		fmt.Println("  Backups       Chosen destination folders and artifacts will be kept")
	} else {
		fmt.Printf("  Config        %s (will be kept)\n", paths.Config)
		fmt.Println("  State / logs  Will be kept")
	}
	fmt.Print("  Continue? [y/N]: ")

	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("could not read confirmation: %w", err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("uninstall cancelled")
	}
	return nil
}

// ── Windows-specific uninstall ──

func runWindowsUninstall(exePath string, purgeRoots []string, purge bool) error {
	cmdLine := windowsUninstallCommand(exePath, purgeRoots, purge)
	if err := startDetachedProcess("cmd.exe", "/D", "/S", "/C", cmdLine); err != nil {
		return fmt.Errorf("could not schedule uninstall: %w", err)
	}

	fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Scheduled binary removal: %s\n", exePath)
	if purge {
		for _, target := range purgeRoots {
			fmt.Printf("  \033[38;2;166;227;161m✓\033[0m Scheduled dbterm data removal: %s\n", target)
		}
		fmt.Println("  \033[33mInfo:\033[0m Backup destination folders and artifacts were not scheduled for removal.")
	} else {
		fmt.Println("  \033[33mInfo:\033[0m Kept dbterm config, state, and logs.")
	}
	fmt.Println("  Removal is pending. Open a new terminal afterward to verify that dbterm is gone.")
	fmt.Println()
	return nil
}

func windowsUninstallCommand(exePath string, purgeRoots []string, purge bool) string {
	parts := []string{
		"ping 127.0.0.1 -n 3 > nul",
		fmt.Sprintf(`del /f /q "%s"`, exePath),
	}
	if purge {
		for _, target := range purgeRoots {
			parts = append(parts, fmt.Sprintf(`if exist "%s" rmdir /s /q "%s"`, target, target))
		}
	}

	return strings.Join(parts, " && ")
}
