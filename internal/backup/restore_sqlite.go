package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

func executeSQLiteDatabaseRestore(ctx context.Context, plan *RestorePlan, payload *payloadSource, emit func(string)) error {
	if plan.Options.Mode != RestoreModeClean {
		return fmt.Errorf("SQLite database-file restore requires explicit clean mode")
	}
	if err := validateSQLiteDatabaseFile(ctx, payload.path); err != nil {
		return fmt.Errorf("validate staged SQLite backup: %w", err)
	}

	targetPath := plan.Target.FilePath
	targetDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return fmt.Errorf("create SQLite restore target directory: %w", err)
	}
	initialInfo, targetExists, err := inspectSQLiteRestoreTarget(targetPath)
	if err != nil {
		return err
	}
	if targetExists {
		if err := ensureNoSQLiteSidecars(targetPath); err != nil {
			return err
		}
		snapshotPath, err := createSQLitePreRestoreSnapshot(ctx, targetPath)
		if err != nil {
			return err
		}
		emitRestore(emit, "Verified pre-restore SQLite snapshot: "+snapshotPath)
	} else {
		emitRestore(emit, "SQLite target is new; no pre-restore snapshot was needed")
	}

	stage, err := os.CreateTemp(targetDir, "."+filepath.Base(targetPath)+".restore-*")
	if err != nil {
		return fmt.Errorf("create SQLite restore staging file: %w", err)
	}
	stagePath := stage.Name()
	stageIdentity, err := stage.Stat()
	if err != nil {
		_ = stage.Close()
		_ = os.Remove(stagePath)
		return fmt.Errorf("inspect SQLite SQL restore staging file: %w", err)
	}
	stagePublished := false
	defer func() {
		_ = stage.Close()
		if !stagePublished {
			_ = os.Remove(stagePath)
		}
	}()
	if err := stage.Chmod(0o600); err != nil {
		return fmt.Errorf("protect SQLite restore staging file: %w", err)
	}
	if _, err := payload.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind verified SQLite backup: %w", err)
	}
	written, err := io.Copy(stage, &contextReader{ctx: ctx, reader: payload.file})
	if err != nil {
		return fmt.Errorf("stage SQLite restore: %w", err)
	}
	if written != payload.size {
		return fmt.Errorf("staged SQLite restore size mismatch: copied %d bytes, expected %d", written, payload.size)
	}
	if err := stage.Sync(); err != nil {
		return fmt.Errorf("sync SQLite restore staging file: %w", err)
	}
	if err := stage.Close(); err != nil {
		return fmt.Errorf("close SQLite restore staging file: %w", err)
	}
	if err := verifySQLiteStageIdentity(stagePath, stageIdentity); err != nil {
		return err
	}
	if err := validateSQLiteDatabaseFile(ctx, stagePath); err != nil {
		return fmt.Errorf("validate SQLite restore staging file: %w", err)
	}

	if err := verifySQLiteTargetUnchanged(targetPath, initialInfo, targetExists); err != nil {
		return err
	}
	if err := ensureNoSQLiteSidecars(targetPath); err != nil {
		return err
	}
	emitRestore(emit, "Replacing the SQLite target with the verified staging file")
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("SQLite restore canceled before target replacement: %w", err)
	}
	if err := replaceSQLiteStagedFile(stagePath, targetPath); err != nil {
		return fmt.Errorf("atomically replace SQLite target: %w", err)
	}
	stagePublished = true
	if err := syncDirectory(targetDir); err != nil {
		return fmt.Errorf("sync SQLite restore target directory: %w", err)
	}
	return nil
}

func executeSQLiteSQLRestore(ctx context.Context, plan *RestorePlan, payload *payloadSource, emit func(string)) error {
	if err := validateSQLiteSQLForRestore(payload.path); err != nil {
		return err
	}
	tool, err := requireClientTool("sqlite3")
	if err != nil {
		return fmt.Errorf("SQLite SQL restore requires the sqlite3 command-line client: %w", err)
	}

	targetPath := plan.Target.FilePath
	targetDir := filepath.Dir(targetPath)
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return fmt.Errorf("create SQLite restore target directory: %w", err)
	}
	initialInfo, targetExists, err := inspectSQLiteRestoreTarget(targetPath)
	if err != nil {
		return err
	}

	snapshotPath := ""
	if targetExists {
		if err := ensureNoSQLiteSidecars(targetPath); err != nil {
			return err
		}
		snapshotPath, err = createSQLitePreRestoreSnapshot(ctx, targetPath)
		if err != nil {
			return err
		}
		emitRestore(emit, "Verified pre-restore SQLite snapshot: "+snapshotPath)
	} else {
		emitRestore(emit, "SQLite target is new; no pre-restore snapshot was needed")
	}

	stage, err := os.CreateTemp(targetDir, "."+filepath.Base(targetPath)+".sql-restore-*")
	if err != nil {
		return fmt.Errorf("create SQLite SQL restore staging file: %w", err)
	}
	stagePath := stage.Name()
	stageIdentity, err := stage.Stat()
	if err != nil {
		_ = stage.Close()
		_ = os.Remove(stagePath)
		return fmt.Errorf("inspect SQLite SQL restore staging file: %w", err)
	}
	stagePublished := false
	defer func() {
		_ = stage.Close()
		if !stagePublished {
			_ = os.Remove(stagePath)
		}
		for _, suffix := range []string{"-wal", "-shm", "-journal"} {
			_ = os.Remove(stagePath + suffix)
		}
	}()
	if err := stage.Chmod(0o600); err != nil {
		return fmt.Errorf("protect SQLite SQL restore staging file: %w", err)
	}
	if plan.Options.Mode == RestoreModeMerge && targetExists {
		if err := copySQLiteSnapshotIntoStage(ctx, snapshotPath, stage); err != nil {
			return err
		}
	}
	if err := stage.Sync(); err != nil {
		return fmt.Errorf("sync SQLite SQL restore staging file: %w", err)
	}
	if err := stage.Close(); err != nil {
		return fmt.Errorf("close SQLite SQL restore staging file: %w", err)
	}

	initFile, err := os.CreateTemp(filepath.Dir(payload.path), "dbterm-sqlite-init-*")
	if err != nil {
		return fmt.Errorf("create private sqlite3 initialization file: %w", err)
	}
	initPath := initFile.Name()
	defer os.Remove(initPath)
	if err := initFile.Chmod(0o600); err != nil {
		_ = initFile.Close()
		return fmt.Errorf("protect sqlite3 initialization file: %w", err)
	}
	if err := initFile.Close(); err != nil {
		return fmt.Errorf("close sqlite3 initialization file: %w", err)
	}

	emitRestore(emit, "Streaming verified SQLite SQL into a private staged database")
	environment := environmentWithout(os.Environ(), "SQLITE_HISTORY")
	environment = append(environment, "LC_ALL=C")
	err = runRestoreInvocation(ctx, restoreInvocation{
		label:     "sqlite3",
		toolPath:  tool,
		args:      []string{"-batch", "-bail", "-init", initPath, stagePath},
		env:       environment,
		inputPath: payload.path,
	})
	if err != nil {
		return err
	}
	if err := verifySQLiteStageIdentity(stagePath, stageIdentity); err != nil {
		return err
	}
	if err := ensureNoSQLiteSidecars(stagePath); err != nil {
		return fmt.Errorf("SQLite SQL staging database retained an active journal: %w", err)
	}
	if err := validateSQLiteDatabaseFile(ctx, stagePath); err != nil {
		return fmt.Errorf("validate SQLite SQL restore staging database: %w", err)
	}
	if err := verifySQLiteTargetUnchanged(targetPath, initialInfo, targetExists); err != nil {
		return err
	}
	if err := ensureNoSQLiteSidecars(targetPath); err != nil {
		return err
	}

	emitRestore(emit, "Replacing the SQLite target with the verified SQL staging database")
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("SQLite SQL restore canceled before target replacement: %w", err)
	}
	if err := replaceSQLiteStagedFile(stagePath, targetPath); err != nil {
		return fmt.Errorf("atomically replace SQLite target: %w", err)
	}
	stagePublished = true
	if err := syncDirectory(targetDir); err != nil {
		return fmt.Errorf("sync SQLite restore target directory: %w", err)
	}
	return nil
}

func verifySQLiteStageIdentity(path string, expected os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("SQLite restore staging file disappeared or changed: %w", err)
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() || !os.SameFile(expected, current) {
		return fmt.Errorf("SQLite restore staging path changed while sqlite3 was running; no target file was replaced")
	}
	return nil
}

func copySQLiteSnapshotIntoStage(ctx context.Context, snapshotPath string, stage *os.File) error {
	snapshot, err := os.Open(snapshotPath)
	if err != nil {
		return fmt.Errorf("open verified SQLite snapshot for merge staging: %w", err)
	}
	defer snapshot.Close()
	if _, err := stage.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind SQLite merge staging file: %w", err)
	}
	if err := stage.Truncate(0); err != nil {
		return fmt.Errorf("prepare SQLite merge staging file: %w", err)
	}
	if _, err := io.Copy(stage, &contextReader{ctx: ctx, reader: snapshot}); err != nil {
		return fmt.Errorf("copy consistent SQLite target snapshot into merge staging: %w", err)
	}
	return nil
}

func inspectSQLiteRestoreTarget(path string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("inspect SQLite restore target %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("SQLite restore target must not be a symbolic link: %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("SQLite restore target must be a regular file: %s", path)
	}
	if info.Size() == 0 {
		return nil, false, fmt.Errorf("existing SQLite restore target is empty; move it aside or select a new target path")
	}
	return info, true, nil
}

func verifySQLiteTargetUnchanged(path string, initial os.FileInfo, existed bool) error {
	current, err := os.Lstat(path)
	if !existed {
		if err == nil {
			return fmt.Errorf("SQLite restore target appeared while the restore was being staged; no file was replaced")
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("recheck SQLite restore target: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("SQLite restore target changed while the restore was being staged: %w", err)
	}
	if current.Mode()&os.ModeSymlink != 0 || !current.Mode().IsRegular() ||
		!os.SameFile(initial, current) || current.Size() != initial.Size() || current.ModTime() != initial.ModTime() {
		return fmt.Errorf("SQLite restore target changed while the restore was being staged; no file was replaced")
	}
	return nil
}

func ensureNoSQLiteSidecars(path string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		sidecar := path + suffix
		if _, err := os.Lstat(sidecar); err == nil {
			return fmt.Errorf("SQLite restore stopped because %s exists; close every connection to the target and checkpoint its journal before retrying", sidecar)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect SQLite sidecar %q: %w", sidecar, err)
		}
	}
	return nil
}

func createSQLitePreRestoreSnapshot(ctx context.Context, targetPath string) (string, error) {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate pre-restore snapshot name: %w", err)
	}
	snapshotPath := fmt.Sprintf("%s.pre-restore-%s-%s.sqlite3", targetPath,
		time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(random[:]))
	if _, err := os.Lstat(snapshotPath); err == nil {
		return "", fmt.Errorf("pre-restore snapshot path already exists: %s", snapshotPath)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect pre-restore snapshot path: %w", err)
	}

	database, err := sql.Open("sqlite", sqliteFileDSN(targetPath, "rw"))
	if err != nil {
		return "", fmt.Errorf("open SQLite target for pre-restore snapshot: %w", err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return "", fmt.Errorf("open SQLite target for pre-restore snapshot: %w", err)
	}
	if _, err := database.ExecContext(ctx, "VACUUM INTO "+sqliteLiteral(snapshotPath)); err != nil {
		_ = os.Remove(snapshotPath)
		return "", fmt.Errorf("create consistent pre-restore SQLite snapshot: %w", err)
	}
	if err := database.Close(); err != nil {
		_ = os.Remove(snapshotPath)
		return "", fmt.Errorf("close SQLite target after pre-restore snapshot: %w", err)
	}
	if err := os.Chmod(snapshotPath, 0o600); err != nil {
		_ = os.Remove(snapshotPath)
		return "", fmt.Errorf("protect pre-restore SQLite snapshot: %w", err)
	}
	if err := syncRegularFile(snapshotPath); err != nil {
		_ = os.Remove(snapshotPath)
		return "", err
	}
	if err := validateSQLiteDatabaseFile(ctx, snapshotPath); err != nil {
		_ = os.Remove(snapshotPath)
		return "", fmt.Errorf("validate pre-restore SQLite snapshot: %w", err)
	}
	return snapshotPath, nil
}

func validateSQLiteDatabaseFile(ctx context.Context, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open SQLite database file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("inspect SQLite database file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		_ = file.Close()
		return fmt.Errorf("SQLite database must be a non-empty regular file")
	}
	prefix := make([]byte, min(payloadPeekBytes, info.Size()))
	if _, err := io.ReadFull(file, prefix); err != nil {
		_ = file.Close()
		return fmt.Errorf("read SQLite database header: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close SQLite database header: %w", err)
	}
	if !strings.HasPrefix(string(prefix), "SQLite format 3\x00") {
		return fmt.Errorf("SQLite format 3 header is missing")
	}
	if err := validateSQLiteHeader(prefix, info.Size()); err != nil {
		return err
	}

	database, err := sql.Open("sqlite", sqliteFileDSN(path, "ro"))
	if err != nil {
		return fmt.Errorf("open SQLite database for integrity check: %w", err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return fmt.Errorf("open SQLite database for integrity check: %w", err)
	}
	rows, err := database.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return fmt.Errorf("run SQLite quick_check: %w", err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("read SQLite quick_check result: %w", err)
		}
		found = true
		if !strings.EqualFold(strings.TrimSpace(result), "ok") {
			return fmt.Errorf("SQLite quick_check failed: %s", result)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read SQLite quick_check results: %w", err)
	}
	if !found {
		return fmt.Errorf("SQLite quick_check returned no result")
	}
	return nil
}

func sqliteFileDSN(path, mode string) string {
	slashed := filepath.ToSlash(path)
	if filepath.VolumeName(path) != "" && !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	location := &url.URL{Scheme: "file", Path: slashed}
	query := location.Query()
	query.Set("mode", mode)
	query.Add("_pragma", "busy_timeout(5000)")
	if mode == "ro" {
		query.Add("_pragma", "query_only(1)")
	}
	location.RawQuery = query.Encode()
	return location.String()
}

func syncRegularFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open file for sync %q: %w", path, err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync file %q: %w", path, err)
	}
	return nil
}
