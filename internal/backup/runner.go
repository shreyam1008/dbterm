package backup

import (
	"archive/zip"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"
	"github.com/shreyam1008/dbterm/internal/config"
)

// ProgressEvent describes a bounded, low-overhead backup status update. A zero
// TotalBytes means that the native database tool cannot report its final size
// in advance; CurrentBytes still reflects the staging file observed on disk.
type ProgressEvent struct {
	Phase        string
	Message      string
	CurrentBytes int64
	TotalBytes   int64
	Elapsed      time.Duration
}

type ProgressFunc func(ProgressEvent)

type Runner struct {
	Now      func() time.Time
	Progress ProgressFunc
}

func (r Runner) Run(ctx context.Context, job Job, cfg *config.ConnectionConfig, runID string) (artifact Artifact, err error) {
	defer func() { err = redactConnectionError(err, cfg) }()
	started := time.Now()
	var progressMutex sync.Mutex
	progress := func(event ProgressEvent) {
		if r.Progress == nil {
			return
		}
		event.Elapsed = time.Since(started)
		progressMutex.Lock()
		defer progressMutex.Unlock()
		r.Progress(event)
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	if err := job.Validate(); err != nil {
		return Artifact{}, err
	}
	if cfg == nil {
		return Artifact{}, fmt.Errorf("saved connection %q is unavailable", job.ConnectionID)
	}
	if strings.TrimSpace(runID) == "" {
		var err error
		runID, err = NewID("run")
		if err != nil {
			return Artifact{}, err
		}
	}
	plan, err := PlanFor(cfg)
	if err != nil {
		return Artifact{}, err
	}
	destination, err := resolveDestination(job.Destination)
	if err != nil {
		return Artifact{}, err
	}
	if err := ensureDestination(destination); err != nil {
		return Artifact{}, err
	}
	if removed, cleanupErr := cleanupStalePartials(destination, now.Add(-48*time.Hour)); cleanupErr != nil {
		progress(ProgressEvent{Phase: "preflight", Message: "stale partial cleanup warning: " + cleanupErr.Error()})
	} else if removed > 0 {
		progress(ProgressEvent{Phase: "preflight", Message: fmt.Sprintf("removed %d stale dbterm partial artifact(s)", removed)})
	}
	fileName, err := buildArtifactFilename(job, cfg, plan, runID, now)
	if err != nil {
		return Artifact{}, err
	}
	finalPath := filepath.Join(destination, fileName)
	if _, err := os.Lstat(finalPath); err == nil {
		return Artifact{}, fmt.Errorf("backup file already exists: %s", finalPath)
	} else if !os.IsNotExist(err) {
		return Artifact{}, fmt.Errorf("check backup destination: %w", err)
	}

	progress(ProgressEvent{Phase: "preflight", Message: "destination and backup settings validated"})
	privateStage, err := newPrivateNativeStage(now)
	if err != nil {
		return Artifact{}, err
	}
	defer os.RemoveAll(privateStage)
	rawPath := filepath.Join(privateStage, "native"+plan.Extension)
	pgCompression := 6
	if job.Compression != CompressionNone {
		pgCompression = 0
	}
	if err := createNativeAtPath(ctx, cfg, rawPath, NativeOptions{PostgresCompression: pgCompression, Progress: progress}); err != nil {
		return Artifact{}, fmt.Errorf("native dump phase: %w", err)
	}
	progress(ProgressEvent{Phase: "verify", Message: "validating engine-native backup"})
	if err := verifyNativeBackup(cfg, rawPath); err != nil {
		return Artifact{}, fmt.Errorf("verification phase: %w", err)
	}
	progress(ProgressEvent{Phase: "verify", Message: "engine-native backup passed basic validation"})

	artifactOutput, err := os.CreateTemp(destination, ".dbterm-artifact-*.partial")
	if err != nil {
		return Artifact{}, fmt.Errorf("create private artifact staging file: %w", err)
	}
	artifactStage := artifactOutput.Name()
	if err := artifactOutput.Chmod(0o600); err != nil {
		_ = artifactOutput.Close()
		_ = os.Remove(artifactStage)
		return Artifact{}, fmt.Errorf("protect private artifact staging file: %w", err)
	}
	defer os.Remove(artifactStage)
	entryName := strings.TrimSuffix(fileName, ".age")
	entryName = strings.TrimSuffix(entryName, ".zip")
	checksum, size, err := wrapArtifact(ctx, rawPath, artifactOutput, entryName, job, progress)
	if err != nil {
		return Artifact{}, fmt.Errorf("compression/encryption phase: %w", err)
	}
	// Compression finalization and fsync can complete after the last streaming
	// context check. Cancellation remains authoritative until atomic publication
	// starts; after that boundary the complete artifact is preserved.
	if err := ctx.Err(); err != nil {
		return Artifact{}, fmt.Errorf("backup stopped before publication: %w", err)
	}
	progress(ProgressEvent{Phase: "publish", Message: "publishing completed artifact without replacing existing files"})
	if err := publishNoReplace(ctx, artifactStage, finalPath, progress); err != nil {
		return Artifact{}, err
	}
	progress(ProgressEvent{Phase: "publish", Message: "backup artifact published", CurrentBytes: size, TotalBytes: size})
	return Artifact{
		Path:       finalPath,
		Size:       size,
		SHA256:     checksum,
		Format:     plan.Format,
		Verified:   true,
		CreatedAt:  now,
		BackupName: job.Name,
	}, nil
}

func wrapArtifact(ctx context.Context, rawPath string, output *os.File, entryName string, job Job, progress ProgressFunc) (string, int64, error) {
	input, err := os.Open(rawPath)
	if err != nil {
		return "", 0, err
	}
	defer input.Close()
	inputInfo, err := input.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("inspect native backup before wrapping: %w", err)
	}
	totalBytes := inputInfo.Size()
	if output == nil {
		return "", 0, fmt.Errorf("backup artifact staging file is required")
	}
	outputPath := output.Name()
	defer output.Close()

	hash := sha256.New()
	var sink io.Writer = io.MultiWriter(output, hash)
	var encryptionWriter io.WriteCloser
	if job.Encryption == EncryptionAge {
		recipient, err := age.ParseX25519Recipient(strings.TrimSpace(job.AgeRecipient))
		if err != nil {
			return "", 0, fmt.Errorf("parse age X25519 recipient: %w", err)
		}
		encryptionWriter, err = age.Encrypt(sink, recipient)
		if err != nil {
			return "", 0, fmt.Errorf("start age encryption: %w", err)
		}
		sink = encryptionWriter
	}
	payloadWriter, compressionClose, err := compressionWriter(sink, job.Compression, job.CompressionLevel, entryName)
	if err != nil {
		return "", 0, err
	}

	message := "compressing backup artifact"
	if job.Encryption == EncryptionAge && job.Compression == CompressionNone {
		message = "encrypting backup artifact"
	} else if job.Encryption == EncryptionAge {
		message = "compressing and encrypting backup artifact"
	} else if job.Compression == CompressionNone {
		message = "wrapping backup artifact"
	}
	if progress != nil {
		progress(ProgressEvent{Phase: "wrap", Message: message, TotalBytes: totalBytes})
	}
	counter := &progressReader{
		reader:   &contextReader{ctx: ctx, reader: input},
		total:    totalBytes,
		progress: progress,
		message:  message,
	}
	if _, err := io.Copy(payloadWriter, counter); err != nil {
		return "", 0, fmt.Errorf("stream native backup: %w", err)
	}
	counter.finish()
	if err := compressionClose(); err != nil {
		return "", 0, fmt.Errorf("finalize %s compression: %w", job.Compression, err)
	}
	if encryptionWriter != nil {
		if err := encryptionWriter.Close(); err != nil {
			return "", 0, fmt.Errorf("finalize age encryption: %w", err)
		}
	}
	if err := output.Sync(); err != nil {
		return "", 0, fmt.Errorf("sync backup artifact: %w", err)
	}
	if err := output.Close(); err != nil {
		return "", 0, fmt.Errorf("close backup artifact: %w", err)
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), info.Size(), nil
}

type progressReader struct {
	reader     io.Reader
	current    int64
	total      int64
	lastReport time.Time
	progress   ProgressFunc
	message    string
}

func (reader *progressReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	reader.current += int64(count)
	if reader.progress != nil && (reader.lastReport.IsZero() || time.Since(reader.lastReport) >= 250*time.Millisecond) {
		reader.lastReport = time.Now()
		reader.progress(ProgressEvent{Phase: "wrap", Message: reader.message, CurrentBytes: reader.current, TotalBytes: reader.total})
	}
	return count, err
}

func (reader *progressReader) finish() {
	if reader.progress != nil {
		reader.progress(ProgressEvent{Phase: "wrap", Message: reader.message, CurrentBytes: reader.current, TotalBytes: reader.total})
	}
}

func compressionWriter(destination io.Writer, algorithm Compression, level int, entryName string) (io.Writer, func() error, error) {
	switch algorithm {
	case CompressionNone:
		return destination, func() error { return nil }, nil
	case CompressionGzip:
		if level == 0 {
			level = gzip.DefaultCompression
		}
		if level < gzip.HuffmanOnly || level > gzip.BestCompression {
			return nil, nil, fmt.Errorf("gzip level must be between 1 and 9")
		}
		writer, err := gzip.NewWriterLevel(destination, level)
		return writer, writer.Close, err
	case CompressionZip:
		if level == 0 {
			level = flate.DefaultCompression
		}
		if level < flate.HuffmanOnly || level > flate.BestCompression {
			return nil, nil, fmt.Errorf("zip level must be between 1 and 9")
		}
		writer := zip.NewWriter(destination)
		writer.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
			return flate.NewWriter(out, level)
		})
		header := &zip.FileHeader{Name: sanitizeArchiveEntryName(entryName), Method: zip.Deflate}
		header.SetModTime(time.Now())
		entry, err := writer.CreateHeader(header)
		if err != nil {
			_ = writer.Close()
			return nil, nil, err
		}
		return entry, writer.Close, nil
	case CompressionZstd:
		options := []zstd.EOption{zstd.WithEncoderConcurrency(1), zstd.WithLowerEncoderMem(true)}
		if level != 0 {
			options = append(options, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(level)))
		}
		writer, err := zstd.NewWriter(destination, options...)
		if err != nil {
			return nil, nil, err
		}
		return writer, writer.Close, nil
	default:
		return nil, nil, fmt.Errorf("unsupported compression %q", algorithm)
	}
}

func buildArtifactFilename(job Job, cfg *config.ConnectionConfig, plan NativePlan, runID string, now time.Time) (string, error) {
	base := job.FilenameTemplate
	shortRun := strings.TrimPrefix(runID, "run_")
	if len(shortRun) > 10 {
		shortRun = shortRun[:10]
	}
	values := map[string]string{
		"{job}":        job.Name,
		"{connection}": cfg.Name,
		"{database}":   backupDatabaseName(cfg),
		"{engine}":     string(cfg.Type),
		"{date}":       now.Format("20060102"),
		"{time}":       now.Format("150405"),
		"{timestamp}":  now.Format("20060102T150405Z"),
		"{run}":        shortRun,
	}
	for token, value := range values {
		base = strings.ReplaceAll(base, token, sanitizeFilename(value))
	}
	if strings.ContainsAny(base, "{}") {
		return "", fmt.Errorf("filename template contains an unknown token")
	}
	base = sanitizeFilename(base)
	if base == "" {
		return "", fmt.Errorf("filename template produced an empty name")
	}
	name := base + plan.Extension
	switch job.Compression {
	case CompressionGzip:
		name += ".gz"
	case CompressionZip:
		name += ".zip"
	case CompressionZstd:
		name += ".zst"
	}
	if job.Encryption == EncryptionAge {
		name += ".age"
	}
	if len([]byte(name)) > 240 {
		return "", fmt.Errorf("backup filename is too long (%d bytes; maximum 240)", len([]byte(name)))
	}
	return name, nil
}

func sanitizeFilename(value string) string {
	value = strings.TrimSpace(value)
	var out strings.Builder
	lastSeparator := false
	for _, r := range value {
		valid := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_'
		if valid {
			out.WriteRune(r)
			lastSeparator = false
		} else if !lastSeparator {
			out.WriteByte('_')
			lastSeparator = true
		}
	}
	return strings.Trim(out.String(), "_.-")
}

func sanitizeArchiveEntryName(value string) string {
	value = strings.TrimSpace(value)
	var out strings.Builder
	lastSeparator := false
	for _, r := range value {
		valid := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.'
		if valid {
			out.WriteRune(r)
			lastSeparator = false
		} else if !lastSeparator {
			out.WriteByte('_')
			lastSeparator = true
		}
	}
	name := strings.Trim(out.String(), " .")
	if name == "" || name == "." || name == ".." {
		return "backup"
	}
	return name
}

func backupDatabaseName(cfg *config.ConnectionConfig) string {
	if cfg == nil {
		return "database"
	}
	if cfg.Type == config.SQLite && cfg.FilePath != "" {
		base := filepath.Base(cfg.FilePath)
		return strings.TrimSuffix(base, filepath.Ext(base))
	}
	if cfg.Type == config.CloudflareD1 && cfg.DatabaseID != "" {
		return cfg.DatabaseID
	}
	return nonEmpty(cfg.Database, nonEmpty(cfg.Name, "database"))
}

func resolveDestination(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", fmt.Errorf("backup destination is required")
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
		return "", fmt.Errorf("resolve backup destination: %w", err)
	}
	return abs, nil
}

func ensureDestination(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("backup destination does not exist: %s (create it while saving the job, then retry)", path)
		}
		return fmt.Errorf("access backup destination: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("backup destination is not a folder: %s", path)
	}
	probe, err := os.CreateTemp(path, ".dbterm-write-test-*")
	if err != nil {
		return fmt.Errorf("backup destination is not writable: %w", err)
	}
	probePath := probe.Name()
	_ = probe.Close()
	_ = os.Remove(probePath)
	return nil
}

func cleanupStalePartials(directory string, olderThan time.Time) (int, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(name, ".dbterm-") || !strings.HasSuffix(name, ".partial") {
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
