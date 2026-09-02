package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/privatefile"
)

const (
	DBTermBundleSchemaVersion = 1
	dbtermBundleManifestPath  = "dbterm/bundle.json"
	maxDBTermBundleManifest   = 32 << 20
)

// DBTermBundleManifest is the self-contained recovery index inside a dbterm
// bundle. It intentionally excludes configured absolute source roots.
type DBTermBundleManifest struct {
	SchemaVersion int                   `json:"schema_version"`
	Database      DBTermBundleDatabase  `json:"database"`
	FileSets      []DBTermBundleFileSet `json:"file_sets"`
}

type DBTermBundleDatabase struct {
	Engine    config.DBType `json:"engine"`
	Format    string        `json:"format"`
	Path      string        `json:"path"`
	SizeBytes int64         `json:"size_bytes"`
	SHA256    string        `json:"sha256"`
}

type DBTermBundleFileSet struct {
	Label     string             `json:"label"`
	Required  bool               `json:"required"`
	FileCount int64              `json:"file_count"`
	SizeBytes int64              `json:"size_bytes"`
	Files     []DBTermBundleFile `json:"files"`
}

type DBTermBundleFile struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type builtDBTermBundle struct {
	path     string
	fileSets []ManifestFileSet
	warnings []string
	manifest DBTermBundleManifest
}

func buildDBTermBundle(ctx context.Context, privateStage, nativePath string, cfg *config.ConnectionConfig, plan NativePlan, sets []FileSet) (builtDBTermBundle, error) {
	return buildDBTermBundleWithCapacity(ctx, privateStage, nativePath, cfg, plan, sets, nil)
}

func buildDBTermBundleWithCapacity(ctx context.Context, privateStage, nativePath string, cfg *config.ConnectionConfig, plan NativePlan, sets []FileSet, capacity *bundleCapacityGuard) (builtDBTermBundle, error) {
	if cfg == nil {
		return builtDBTermBundle{}, fmt.Errorf("database connection metadata is required for a dbterm bundle")
	}
	nativeInfo, err := os.Lstat(nativePath)
	if err != nil {
		return builtDBTermBundle{}, fmt.Errorf("inspect verified native database payload: %w", err)
	}
	if nativeInfo.Mode()&os.ModeSymlink != 0 || !nativeInfo.Mode().IsRegular() || fileSetPathIsReparsePoint(nativePath, nativeInfo) || nativeInfo.Size() <= 0 {
		return builtDBTermBundle{}, fmt.Errorf("verified native database payload must be a non-empty regular file")
	}
	nativeDigest, err := copyAndHashStableFile(ctx, nativePath, nativeInfo.Size(), io.Discard)
	if err != nil {
		return builtDBTermBundle{}, fmt.Errorf("hash verified native database payload: %w", err)
	}
	prepared, summaries, warnings, err := prepareJobFileSetsWithCapacity(ctx, sets, privateStage, capacity)
	if err != nil {
		return builtDBTermBundle{}, err
	}

	databaseName := "dbterm/database/native" + plan.Extension
	if err := validateBundleArchivePath(databaseName); err != nil {
		return builtDBTermBundle{}, fmt.Errorf("database bundle path: %w", err)
	}
	manifest := DBTermBundleManifest{
		SchemaVersion: DBTermBundleSchemaVersion,
		Database: DBTermBundleDatabase{
			Engine: cfg.Type, Format: plan.Format, Path: databaseName,
			SizeBytes: nativeInfo.Size(), SHA256: nativeDigest,
		},
		FileSets: make([]DBTermBundleFileSet, 0, len(prepared)),
	}
	sort.Slice(prepared, func(i, j int) bool { return prepared[i].config.Label < prepared[j].config.Label })
	for _, set := range prepared {
		bundleSet := DBTermBundleFileSet{
			Label: set.config.Label, Required: set.config.Required,
			FileCount: set.summary.FileCount, SizeBytes: set.summary.SizeBytes,
			Files: make([]DBTermBundleFile, 0, len(set.files)),
		}
		for _, file := range set.files {
			bundleSet.Files = append(bundleSet.Files, DBTermBundleFile{Path: file.relativePath, SizeBytes: file.size, SHA256: file.sha256})
		}
		manifest.FileSets = append(manifest.FileSets, bundleSet)
	}
	if err := manifest.Validate(); err != nil {
		return builtDBTermBundle{}, fmt.Errorf("build dbterm bundle manifest: %w", err)
	}
	if capacity != nil {
		var selectedBytes uint64
		var selectedFiles uint64
		for _, set := range prepared {
			if set.summary.SizeBytes < 0 || set.summary.FileCount < 0 {
				return builtDBTermBundle{}, fmt.Errorf("prepared file-set capacity metadata is invalid")
			}
			selectedBytes, err = checkedCapacityAdd("prepared file-set size", selectedBytes, uint64(set.summary.SizeBytes))
			if err != nil {
				return builtDBTermBundle{}, err
			}
			selectedFiles, err = checkedCapacityAdd("prepared file-set count", selectedFiles, uint64(set.summary.FileCount))
			if err != nil {
				return builtDBTermBundle{}, err
			}
		}
		if err := capacity.ensurePipeline(0, selectedBytes, selectedFiles); err != nil {
			return builtDBTermBundle{}, fmt.Errorf("recheck dbterm bundle capacity before writing archive: %w", err)
		}
	}
	manifestStage, manifestInfo, err := writeDBTermBundleManifestStage(privateStage, manifest)
	if err != nil {
		return builtDBTermBundle{}, err
	}
	defer os.Remove(manifestStage)

	output, err := privatefile.CreateTemp(privateStage, ".dbterm-bundle-", ".partial")
	if err != nil {
		return builtDBTermBundle{}, fmt.Errorf("create private dbterm bundle stage: %w", err)
	}
	outputPath := output.Name()
	closed := false
	keep := false
	defer func() {
		if !closed {
			_ = output.Close()
		}
		if !keep {
			_ = os.Remove(outputPath)
		}
	}()
	if err := output.Chmod(0o600); err != nil {
		return builtDBTermBundle{}, fmt.Errorf("protect private dbterm bundle stage: %w", err)
	}
	tw := tar.NewWriter(output)
	if err := addStableBundleEntry(ctx, tw, dbtermBundleManifestPath, manifestStage, manifestInfo.Size(), ""); err != nil {
		return builtDBTermBundle{}, err
	}
	if err := addStableBundleEntry(ctx, tw, databaseName, nativePath, nativeInfo.Size(), nativeDigest); err != nil {
		return builtDBTermBundle{}, err
	}
	for _, set := range prepared {
		for _, file := range set.files {
			archivePath := path.Join("dbterm/files", set.config.Label, file.relativePath)
			if err := addStableBundleEntry(ctx, tw, archivePath, file.stagedPath, file.size, file.sha256); err != nil {
				return builtDBTermBundle{}, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return builtDBTermBundle{}, fmt.Errorf("finalize dbterm bundle: %w", err)
	}
	if err := output.Sync(); err != nil {
		return builtDBTermBundle{}, fmt.Errorf("sync dbterm bundle: %w", err)
	}
	if err := output.Close(); err != nil {
		return builtDBTermBundle{}, fmt.Errorf("close dbterm bundle: %w", err)
	}
	closed = true
	keep = true
	return builtDBTermBundle{path: outputPath, fileSets: summaries, warnings: warnings, manifest: manifest}, nil
}

func addStableBundleEntry(ctx context.Context, writer *tar.Writer, archivePath, sourcePath string, size int64, expectedDigest string) error {
	if err := validateBundleArchivePath(archivePath); err != nil {
		return err
	}
	header := &tar.Header{
		Name: archivePath, Mode: 0o600, Size: size, Typeflag: tar.TypeReg,
		ModTime: time.Unix(0, 0).UTC(), AccessTime: time.Time{}, ChangeTime: time.Time{},
		Format: tar.FormatPAX,
	}
	if err := writer.WriteHeader(header); err != nil {
		return fmt.Errorf("write dbterm bundle header %q: %w", archivePath, err)
	}
	digest, err := copyAndHashStableFile(ctx, sourcePath, size, writer)
	if err != nil {
		return fmt.Errorf("write dbterm bundle entry %q: %w", archivePath, err)
	}
	if expectedDigest != "" && !strings.EqualFold(digest, expectedDigest) {
		return fmt.Errorf("dbterm bundle input %q changed after it was verified", archivePath)
	}
	return nil
}

func writeDBTermBundleManifestStage(directory string, manifest DBTermBundleManifest) (string, os.FileInfo, error) {
	file, err := privatefile.CreateTemp(directory, ".dbterm-bundle-manifest-", ".partial")
	if err != nil {
		return "", nil, fmt.Errorf("create private dbterm bundle manifest: %w", err)
	}
	filePath := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(filePath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", nil, err
	}
	limited := &boundedBundleWriter{writer: file, remaining: maxDBTermBundleManifest}
	encoder := json.NewEncoder(limited)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		if errors.Is(err, errDBTermBundleManifestTooLarge) {
			return "", nil, fmt.Errorf("dbterm bundle manifest exceeds %d bytes", maxDBTermBundleManifest)
		}
		return "", nil, fmt.Errorf("encode dbterm bundle manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		return "", nil, err
	}
	info, err := os.Stat(filePath)
	if err != nil {
		return "", nil, err
	}
	keep = true
	return filePath, info, nil
}

func DecodeDBTermBundleManifest(reader io.Reader) (*DBTermBundleManifest, error) {
	if reader == nil {
		return nil, fmt.Errorf("dbterm bundle manifest reader is required")
	}
	limited := io.LimitReader(reader, maxDBTermBundleManifest+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read dbterm bundle manifest: %w", err)
	}
	if len(data) > maxDBTermBundleManifest {
		return nil, fmt.Errorf("dbterm bundle manifest exceeds %d bytes", maxDBTermBundleManifest)
	}
	if err := validateDBTermBundleManifestJSONKeys(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest DBTermBundleManifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode dbterm bundle manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("dbterm bundle manifest contains more than one JSON value")
		}
		return nil, fmt.Errorf("decode dbterm bundle manifest trailing data: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func validateDBTermBundleManifestJSONKeys(data []byte) error {
	if err := validateExactJSONObjectKeys(data, map[string]struct{}{
		"schema_version": {}, "database": {}, "file_sets": {},
	}); err != nil {
		return fmt.Errorf("decode dbterm bundle manifest: %w", err)
	}
	var root struct {
		Database json.RawMessage   `json:"database"`
		FileSets []json.RawMessage `json:"file_sets"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("decode dbterm bundle manifest shape: %w", err)
	}
	if err := validateExactJSONObjectKeys(root.Database, map[string]struct{}{
		"engine": {}, "format": {}, "path": {}, "size_bytes": {}, "sha256": {},
	}); err != nil {
		return fmt.Errorf("decode dbterm bundle database: %w", err)
	}
	for setIndex, rawSet := range root.FileSets {
		if err := validateExactJSONObjectKeys(rawSet, map[string]struct{}{
			"label": {}, "required": {}, "file_count": {}, "size_bytes": {}, "files": {},
		}); err != nil {
			return fmt.Errorf("decode dbterm bundle file_sets[%d]: %w", setIndex, err)
		}
		var set struct {
			Files []json.RawMessage `json:"files"`
		}
		if err := json.Unmarshal(rawSet, &set); err != nil {
			return fmt.Errorf("decode dbterm bundle file_sets[%d]: %w", setIndex, err)
		}
		for fileIndex, rawFile := range set.Files {
			if err := validateExactJSONObjectKeys(rawFile, map[string]struct{}{
				"path": {}, "size_bytes": {}, "sha256": {},
			}); err != nil {
				return fmt.Errorf("decode dbterm bundle file_sets[%d].files[%d]: %w", setIndex, fileIndex, err)
			}
		}
	}
	return nil
}

func (manifest DBTermBundleManifest) Validate() error {
	if manifest.SchemaVersion != DBTermBundleSchemaVersion {
		return fmt.Errorf("unsupported dbterm bundle schema version %d", manifest.SchemaVersion)
	}
	if err := validateBundleDatabaseEngineFormat(manifest.Database.Engine, manifest.Database.Format); err != nil {
		return fmt.Errorf("dbterm bundle database: %w", err)
	}
	if manifest.Database.Format == string(FormatDBTermBundle) {
		return fmt.Errorf("dbterm bundle database payload cannot itself be a bundle")
	}
	if err := validateBundleArchivePath(manifest.Database.Path); err != nil || !strings.HasPrefix(manifest.Database.Path, "dbterm/database/") {
		return fmt.Errorf("dbterm bundle database path is invalid")
	}
	if manifest.Database.SizeBytes <= 0 || !validSHA256(manifest.Database.SHA256) {
		return fmt.Errorf("dbterm bundle database size and SHA-256 are required")
	}
	if manifest.FileSets == nil {
		return fmt.Errorf("dbterm bundle file_sets must be an array")
	}
	labels := make(map[string]struct{}, len(manifest.FileSets))
	previousLabel := ""
	totalFiles := 0
	for index, set := range manifest.FileSets {
		if !validFileSetLabel(set.Label) {
			return fmt.Errorf("dbterm bundle file set %d has an invalid label", index+1)
		}
		if previousLabel != "" && set.Label <= previousLabel {
			return fmt.Errorf("dbterm bundle file sets are not in deterministic label order")
		}
		previousLabel = set.Label
		folded := strings.ToLower(set.Label)
		if _, duplicate := labels[folded]; duplicate {
			return fmt.Errorf("dbterm bundle file-set label %q is duplicated", set.Label)
		}
		labels[folded] = struct{}{}
		if set.Files == nil || set.FileCount != int64(len(set.Files)) || set.FileCount < 1 || set.SizeBytes < 0 {
			return fmt.Errorf("dbterm bundle file set %q has inconsistent counts", set.Label)
		}
		var size int64
		previousPath := ""
		for _, file := range set.Files {
			if err := validateBundleRelativePath(file.Path); err != nil {
				return fmt.Errorf("dbterm bundle file set %q: %w", set.Label, err)
			}
			if previousPath != "" && file.Path <= previousPath {
				return fmt.Errorf("dbterm bundle file set %q is not in deterministic path order", set.Label)
			}
			previousPath = file.Path
			if file.SizeBytes < 0 || !validSHA256(file.SHA256) {
				return fmt.Errorf("dbterm bundle file %q has invalid size or SHA-256", file.Path)
			}
			if file.SizeBytes > 0 && size > int64(^uint64(0)>>1)-file.SizeBytes {
				return fmt.Errorf("dbterm bundle file-set size exceeds supported range")
			}
			size += file.SizeBytes
			totalFiles++
			if totalFiles > maxFileSetFiles {
				return fmt.Errorf("dbterm bundle contains too many files")
			}
		}
		if size != set.SizeBytes {
			return fmt.Errorf("dbterm bundle file set %q size does not match its files", set.Label)
		}
	}
	return nil
}

func validateBundleDatabaseEngineFormat(engine config.DBType, format string) error {
	valid := false
	switch engine {
	case config.PostgreSQL:
		valid = format == string(FormatPostgresCustom) || format == string(FormatPostgresTar) || format == string(FormatPostgresSQL)
	case config.MySQL:
		valid = format == string(FormatMySQLSQL)
	case config.SQLite:
		valid = format == string(FormatSQLiteDatabase) || format == string(FormatSQLiteSQL)
	case config.Turso, config.CloudflareD1:
		valid = format == string(FormatSQLiteSQL)
	default:
		return fmt.Errorf("unsupported database engine %q", engine)
	}
	if !valid {
		return fmt.Errorf("format %q is invalid for database engine %q", format, engine)
	}
	return nil
}

func validateBundleArchivePath(name string) error {
	if err := validateBundleRelativePath(name); err != nil {
		return err
	}
	if !strings.HasPrefix(name, "dbterm/") {
		return fmt.Errorf("dbterm bundle entry must be below dbterm/")
	}
	return nil
}

var errDBTermBundleManifestTooLarge = errors.New("dbterm bundle manifest too large")

type boundedBundleWriter struct {
	writer    io.Writer
	remaining int
}

func (writer *boundedBundleWriter) Write(data []byte) (int, error) {
	if len(data) > writer.remaining {
		return 0, errDBTermBundleManifestTooLarge
	}
	written, err := writer.writer.Write(data)
	writer.remaining -= written
	return written, err
}

func hashBundleEntry(reader io.Reader) (int64, string, error) {
	hash := sha256.New()
	written, err := io.Copy(hash, reader)
	if err != nil {
		return 0, "", err
	}
	return written, hex.EncodeToString(hash.Sum(nil)), nil
}

// VerifyDBTermBundleEnvelope validates the complete deterministic tar layout
// and every internal size/SHA-256 without extracting paths. The caller must
// separately authenticate or hash the outer artifact when that is required.
func VerifyDBTermBundleEnvelope(readerAt io.ReaderAt, size int64) error {
	return verifyDBTermBundleEnvelope(context.Background(), readerAt, size)
}

// VerifyDBTermBundleEnvelopeContext is the cancellation-aware form used by
// transfer paths that already own a job context.
func VerifyDBTermBundleEnvelopeContext(ctx context.Context, readerAt io.ReaderAt, size int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return verifyDBTermBundleEnvelope(ctx, readerAt, size)
}

func verifyDBTermBundleEnvelope(ctx context.Context, readerAt io.ReaderAt, size int64) error {
	if readerAt == nil || size <= 0 {
		return fmt.Errorf("non-empty dbterm bundle reader is required")
	}
	reader := tar.NewReader(io.NewSectionReader(readerAt, 0, size))
	header, err := reader.Next()
	if err != nil {
		return fmt.Errorf("read dbterm bundle manifest header: %w", err)
	}
	if header.Name != dbtermBundleManifestPath || header.Typeflag != tar.TypeReg || !header.FileInfo().Mode().IsRegular() || header.Size <= 0 || header.Size > maxDBTermBundleManifest {
		return fmt.Errorf("dbterm bundle must begin with a bounded regular %s entry", dbtermBundleManifestPath)
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: reader}, maxDBTermBundleManifest+1))
	if err != nil || int64(len(manifestBytes)) != header.Size {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return fmt.Errorf("read dbterm bundle manifest: %w", err)
	}
	manifest, err := DecodeDBTermBundleManifest(bytes.NewReader(manifestBytes))
	if err != nil {
		return err
	}
	expected := []expectedBundleEntry{{name: manifest.Database.Path, size: manifest.Database.SizeBytes, digest: manifest.Database.SHA256, db: true}}
	for _, set := range manifest.FileSets {
		for _, file := range set.Files {
			expected = append(expected, expectedBundleEntry{name: path.Join("dbterm/files", set.Label, file.Path), size: file.SizeBytes, digest: file.SHA256})
		}
	}
	for _, wanted := range expected {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := reader.Next()
		if err != nil {
			return fmt.Errorf("dbterm bundle is missing required entry %q: %w", wanted.name, err)
		}
		if header.Name != wanted.name || header.Typeflag != tar.TypeReg || !header.FileInfo().Mode().IsRegular() || header.Size != wanted.size {
			return fmt.Errorf("dbterm bundle entry %q does not match its declared path, type, and size", wanted.name)
		}
		written, digest, err := hashBundleEntry(&contextReader{ctx: ctx, reader: reader})
		if err != nil || written != wanted.size || !strings.EqualFold(digest, wanted.digest) {
			if err == nil {
				err = fmt.Errorf("size or SHA-256 mismatch")
			}
			return fmt.Errorf("verify dbterm bundle entry %q: %w", wanted.name, err)
		}
	}
	if extra, err := reader.Next(); err == nil {
		return fmt.Errorf("dbterm bundle contains unexpected entry %q", extra.Name)
	} else if !errors.Is(err, io.EOF) {
		return fmt.Errorf("finish reading dbterm bundle: %w", err)
	}
	return nil
}
