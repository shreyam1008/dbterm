package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
)

func verifyNativeBackup(cfg *config.ConnectionConfig, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open staged backup for verification: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		return fmt.Errorf("backup tool produced an empty file")
	}
	prefix := make([]byte, 32)
	n, _ := io.ReadFull(file, prefix)
	prefix = prefix[:n]
	switch cfg.Type {
	case config.PostgreSQL:
		if !bytes.HasPrefix(prefix, []byte("PGDMP")) {
			return fmt.Errorf("PostgreSQL backup validation failed: pg_dump output is not a custom archive")
		}
	case config.SQLite:
		if !bytes.HasPrefix(prefix, []byte("SQLite format 3\x00")) {
			return fmt.Errorf("SQLite backup validation failed: snapshot header is invalid")
		}
	case config.MySQL, config.Turso, config.CloudflareD1:
		if len(bytes.TrimSpace(prefix)) == 0 {
			return fmt.Errorf("logical backup validation failed: output contains no SQL")
		}
	}
	return nil
}

// publishNoReplace keeps the final pathname absent until an atomic,
// no-replace filesystem operation publishes a complete and synced file. A
// cancellation that wins before that operation removes only private partials;
// cancellation after the atomic boundary never removes the published backup.
func publishNoReplace(ctx context.Context, stagedPath, finalPath string, progress ProgressFunc) error {
	return publishNoReplaceWithOps(ctx, stagedPath, finalPath, progress, publicationOps{
		link:   os.Link,
		atomic: atomicPublishNoReplace,
	})
}

type publicationOps struct {
	link   func(string, string) error
	atomic func(string, string) error
}

func publishNoReplaceWithOps(ctx context.Context, stagedPath, finalPath string, progress ProgressFunc, ops publicationOps) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := atomicPublicationSupported(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("backup stopped before publication: %w", err)
	}
	if ops.link == nil || ops.atomic == nil {
		return fmt.Errorf("atomic backup publication is unavailable")
	}
	if removed, err := cleanupStalePublicationPartials(filepath.Dir(finalPath), time.Now().Add(-48*time.Hour)); err != nil {
		if progress != nil {
			progress(ProgressEvent{Phase: "publish", Message: "stale destination publication cleanup warning: " + err.Error()})
		}
	} else if removed > 0 && progress != nil {
		progress(ProgressEvent{Phase: "publish", Message: fmt.Sprintf("removed %d stale destination publication partial(s)", removed)})
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("backup stopped before publication: %w", err)
	}

	// A hard link is the cheapest atomic no-replace publication when staging
	// and destination share a filesystem. If it is unavailable, never fall
	// back to copying into the visible final name.
	if err := ops.link(stagedPath, finalPath); err == nil {
		return finishPublishedBackup(stagedPath, finalPath)
	}
	if err := finalPathAvailable(finalPath); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("backup stopped before destination staging: %w", err)
	}

	// Runner artifacts already live in the destination directory. On a
	// filesystem without hard links, publish that private local stage directly
	// with the platform's atomic no-replace primitive and avoid a second copy.
	if sameCleanDirectory(stagedPath, finalPath) {
		if err := ops.atomic(stagedPath, finalPath); err != nil {
			return publicationError(finalPath, err)
		}
		return finishPublishedBackup(stagedPath, finalPath)
	}

	partial, err := os.CreateTemp(filepath.Dir(finalPath), ".dbterm-publish-*.partial")
	if err != nil {
		return fmt.Errorf("create destination-local backup staging file: %w", err)
	}
	partialPath := partial.Name()
	partialClosed := false
	defer func() {
		if !partialClosed {
			_ = partial.Close()
		}
		_ = os.Remove(partialPath)
	}()
	if err := partial.Chmod(0o600); err != nil {
		return fmt.Errorf("protect destination-local backup staging file: %w", err)
	}

	source, err := os.Open(stagedPath)
	if err != nil {
		return fmt.Errorf("open completed backup for publication: %w", err)
	}
	defer source.Close()
	sourceInfo, err := source.Stat()
	if err != nil {
		return fmt.Errorf("inspect completed backup for publication: %w", err)
	}
	if !sourceInfo.Mode().IsRegular() {
		return fmt.Errorf("completed backup staging path is not a regular file: %s", stagedPath)
	}

	message := "copying completed backup into destination-local private staging"
	if progress != nil {
		progress(ProgressEvent{Phase: "publish", Message: message, TotalBytes: sourceInfo.Size()})
	}
	written, err := copyPublicationStage(ctx, partial, source, sourceInfo.Size(), message, progress)
	if err != nil {
		return fmt.Errorf("stage completed backup in destination: %w", err)
	}
	if err := partial.Sync(); err != nil {
		return fmt.Errorf("sync destination-local backup staging file: %w", err)
	}
	if err := partial.Close(); err != nil {
		return fmt.Errorf("close destination-local backup staging file: %w", err)
	}
	partialClosed = true
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("backup stopped before publication: %w", err)
	}
	if progress != nil {
		progress(ProgressEvent{Phase: "publish", Message: "destination-local backup staging complete", CurrentBytes: written, TotalBytes: sourceInfo.Size()})
	}
	if err := ops.atomic(partialPath, finalPath); err != nil {
		return publicationError(finalPath, err)
	}
	return finishPublishedBackup(stagedPath, finalPath)
}

func copyPublicationStage(ctx context.Context, destination io.Writer, source io.Reader, total int64, message string, progress ProgressFunc) (int64, error) {
	reporter := &publicationProgressWriter{
		writer:   destination,
		total:    total,
		message:  message,
		progress: progress,
	}
	written, err := io.CopyBuffer(reporter, &contextReader{ctx: ctx, reader: source}, make([]byte, 128*1024))
	if err != nil {
		return written, err
	}
	if err := ctx.Err(); err != nil {
		return written, err
	}
	reporter.finish()
	return written, nil
}

type publicationProgressWriter struct {
	writer     io.Writer
	current    int64
	total      int64
	lastReport time.Time
	message    string
	progress   ProgressFunc
}

func (writer *publicationProgressWriter) Write(buffer []byte) (int, error) {
	written, err := writer.writer.Write(buffer)
	writer.current += int64(written)
	if writer.progress != nil && (writer.lastReport.IsZero() || time.Since(writer.lastReport) >= 250*time.Millisecond) {
		writer.lastReport = time.Now()
		writer.progress(ProgressEvent{Phase: "publish", Message: writer.message, CurrentBytes: writer.current, TotalBytes: writer.total})
	}
	return written, err
}

func (writer *publicationProgressWriter) finish() {
	if writer.progress != nil {
		writer.progress(ProgressEvent{Phase: "publish", Message: writer.message, CurrentBytes: writer.current, TotalBytes: writer.total})
	}
}

func sameCleanDirectory(first, second string) bool {
	firstDirectory, firstErr := filepath.Abs(filepath.Dir(first))
	secondDirectory, secondErr := filepath.Abs(filepath.Dir(second))
	return firstErr == nil && secondErr == nil && filepath.Clean(firstDirectory) == filepath.Clean(secondDirectory)
}

func finalPathAvailable(finalPath string) error {
	if _, err := os.Lstat(finalPath); err == nil {
		return fmt.Errorf("backup file already exists: %s", finalPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check backup publication path: %w", err)
	}
	return nil
}

func publicationError(finalPath string, err error) error {
	if availabilityErr := finalPathAvailable(finalPath); availabilityErr != nil {
		return availabilityErr
	}
	return fmt.Errorf("publish backup atomically without replacing files: %w", err)
}

func finishPublishedBackup(stagedPath, finalPath string) error {
	// Publication has crossed its atomic boundary. Never remove finalPath from
	// this point onward, including if cancellation arrives concurrently.
	if err := syncDirectory(filepath.Dir(finalPath)); err != nil {
		return fmt.Errorf("backup was published at %s but its directory could not be synced; the completed artifact was preserved: %w", finalPath, err)
	}
	_ = os.Remove(stagedPath)
	return nil
}

func cleanupStalePublicationPartials(directory string, olderThan time.Time) (int, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(name, ".dbterm-publish-") || !strings.HasSuffix(name, ".partial") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || !info.ModTime().Before(olderThan) {
			continue
		}
		if err := os.Remove(filepath.Join(directory, name)); err != nil && !os.IsNotExist(err) {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func externalCommandError(ctx context.Context, tool string, output []byte, runErr error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s stopped: %w", tool, ctx.Err())
	}
	detail := strings.TrimSpace(string(output))
	if len(detail) > 4000 {
		detail = detail[len(detail)-4000:]
	}
	if detail == "" {
		detail = runErr.Error()
	}
	return fmt.Errorf("%s failed: %s", tool, detail)
}

func redactConnectionError(err error, cfg *config.ConnectionConfig) error {
	if err == nil || cfg == nil {
		return err
	}
	message := err.Error()
	for _, secret := range []string{cfg.Password, cfg.AuthToken} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	if message == err.Error() {
		return err
	}
	return redactedConnectionError{message: message, cause: err}
}

type redactedConnectionError struct {
	message string
	cause   error
}

func (err redactedConnectionError) Error() string { return err.message }
func (err redactedConnectionError) Unwrap() error { return err.cause }

func defaultPort(cfg *config.ConnectionConfig) string {
	if strings.TrimSpace(cfg.Port) != "" {
		return strings.TrimSpace(cfg.Port)
	}
	if cfg.Type == config.MySQL {
		return "3306"
	}
	return "5432"
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
