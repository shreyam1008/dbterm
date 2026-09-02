package backup

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"filippo.io/age"
	"filippo.io/age/armor"
	"github.com/klauspost/compress/zstd"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/privatefile"
)

const (
	// DefaultMaxDecodedBytes limits each decoded wrapper layer. Inspection
	// materializes decoded layers on disk rather than retaining database dumps
	// in memory.
	DefaultMaxDecodedBytes int64 = 1 << 30 // 1 GiB
	maxWrapperDepth              = 3
	payloadPeekBytes             = 256 << 10
	wrapperPeekBytes             = 2 << 10
)

type Format string

const (
	FormatPostgresCustom Format = "postgres_custom"
	FormatPostgresTar    Format = "postgres_tar"
	FormatPostgresSQL    Format = "postgres_sql"
	FormatMySQLSQL       Format = "mysql_sql"
	FormatSQLiteDatabase Format = "sqlite_database"
	FormatSQLiteSQL      Format = "sqlite_sql"
	FormatGenericSQL     Format = "generic_sql"
	FormatDBTermBundle   Format = "dbterm_bundle"
	FormatUnknown        Format = "unknown"
)

type Wrapper string

const (
	WrapperGzip Wrapper = "gzip"
	WrapperZstd Wrapper = "zstd"
	WrapperZip  Wrapper = "zip"
	WrapperAge  Wrapper = "age"
)

const (
	ConfidenceExact     = "exact"
	ConfidenceStrong    = "strong"
	ConfidenceAmbiguous = "ambiguous"
	ConfidenceUnknown   = "unknown"
	ConfidenceLocked    = "locked"
)

type Inspection struct {
	Path           string
	Size           int64
	SHA256         string
	Manifest       *ArtifactManifest
	Wrappers       []Wrapper
	Format         Format
	DatabaseFormat Format
	Engine         config.DBType
	Confidence     string
	Evidence       []string
	Warnings       []string
	Locked         bool
	RequiredTools  []string
	FileSets       []ManifestFileSet
}

type InspectOptions struct {
	AgeIdentityPath string
	MaxDecodedBytes int64
}

type payloadSource struct {
	file             *os.File
	path             string
	size             int64
	temporary        bool
	restoreStageRoot string
	restoreFiles     []stagedRestoreFile
}

func (s *payloadSource) cleanup() {
	if s == nil {
		return
	}
	if s.file != nil {
		_ = s.file.Close()
	}
	if s.temporary && s.path != "" {
		_ = os.Remove(s.path)
	}
	if s.restoreStageRoot != "" {
		_ = os.RemoveAll(s.restoreStageRoot)
	}
}

// Inspect identifies a database backup from its bytes, independently of its
// filename. The SHA-256 digest and Size always describe the original outer
// artifact, while Wrappers is ordered outermost first.
func Inspect(ctx context.Context, path string, opts InspectOptions) (*Inspection, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	maxDecoded, err := configuredMaxDecodedBytes(opts.MaxDecodedBytes)
	if err != nil {
		return nil, err
	}

	resolved, info, err := resolveInspectionPath(path)
	if err != nil {
		return nil, err
	}

	outer, err := os.Open(resolved)
	if err != nil {
		return nil, fmt.Errorf("open backup file %q: %w", resolved, err)
	}
	openedInfo, err := outer.Stat()
	if err != nil {
		_ = outer.Close()
		return nil, fmt.Errorf("inspect backup file %q: %w", resolved, err)
	}
	if !openedInfo.Mode().IsRegular() {
		_ = outer.Close()
		return nil, fmt.Errorf("backup source must be a regular file: %s", resolved)
	}
	if openedInfo.Size() == 0 {
		_ = outer.Close()
		return nil, fmt.Errorf("backup file is empty: %s", resolved)
	}
	if !os.SameFile(info, openedInfo) {
		_ = outer.Close()
		return nil, fmt.Errorf("backup file changed while it was being opened: %s", resolved)
	}

	digest, err := hashOpenedFile(ctx, outer)
	if err != nil {
		_ = outer.Close()
		return nil, fmt.Errorf("hash backup file %q: %w", resolved, err)
	}
	afterHash, err := outer.Stat()
	if err != nil {
		_ = outer.Close()
		return nil, fmt.Errorf("inspect backup file %q after hashing: %w", resolved, err)
	}
	if info.Size() != openedInfo.Size() || info.ModTime() != openedInfo.ModTime() ||
		afterHash.Size() != openedInfo.Size() || afterHash.ModTime() != openedInfo.ModTime() {
		_ = outer.Close()
		return nil, fmt.Errorf("backup file changed while it was being inspected: %s", resolved)
	}

	result := &Inspection{
		Path:       resolved,
		Size:       openedInfo.Size(),
		SHA256:     digest,
		Format:     FormatUnknown,
		Confidence: ConfidenceUnknown,
	}
	manifest, foundManifest, err := ReadArtifactManifestForArtifact(resolved)
	if err != nil {
		_ = outer.Close()
		return nil, err
	}
	if foundManifest {
		if err := verifyManifestArtifact(manifest, Artifact{Size: result.Size, SHA256: result.SHA256}); err != nil {
			_ = outer.Close()
			return nil, fmt.Errorf("artifact completion manifest does not match %s: %w", resolved, err)
		}
		result.Manifest = manifest
		result.Warnings = append(result.Warnings, manifest.Warnings...)
		for _, fileSet := range manifest.FileSets {
			if fileSet.Consistency == FileSetConsistencyBestEffort {
				result.FileSets = append(result.FileSets, fileSet)
			}
		}
		result.Evidence = append(result.Evidence, "dbterm completion manifest v1 matches the artifact size and SHA-256")
	}

	current := &payloadSource{file: outer, path: resolved, size: openedInfo.Size()}
	defer func() { current.cleanup() }()

	for depth := 0; ; depth++ {
		kind, armored, err := detectWrapper(current)
		if err != nil {
			return nil, err
		}
		if kind == "" {
			break
		}
		if depth >= maxWrapperDepth {
			return nil, fmt.Errorf("backup wrapper nesting exceeds the maximum depth of %d", maxWrapperDepth)
		}

		result.Wrappers = append(result.Wrappers, kind)
		result.Evidence = append(result.Evidence, wrapperEvidence(kind, armored))
		if kind == WrapperAge && strings.TrimSpace(opts.AgeIdentityPath) == "" {
			if !current.temporary {
				if err := verifyOpenedFileUnchanged(current.file, openedInfo, resolved); err != nil {
					return nil, err
				}
			}
			result.Locked = true
			result.Confidence = ConfidenceLocked
			result.Warnings = append(result.Warnings,
				"Encrypted age backup detected; select an identity file to inspect its database format.")
			if err := verifyInspectionManifestDescription(result); err != nil {
				return nil, err
			}
			addExtensionWarnings(result)
			return result, nil
		}

		next, err := decodeWrapper(ctx, current, kind, armored, opts, maxDecoded)
		if err != nil {
			return nil, err
		}
		if !current.temporary {
			if err := verifyOpenedFileUnchanged(current.file, openedInfo, resolved); err != nil {
				next.cleanup()
				return nil, err
			}
		}
		current.cleanup()
		current = next
	}

	bundle, isBundle, err := inspectDBTermBundle(ctx, current, maxDecoded)
	if err != nil {
		return nil, err
	}
	var format, databaseFormat Format
	var engine config.DBType
	var confidence string
	var evidence, warnings []string
	if isBundle {
		format = FormatDBTermBundle
		databaseFormat = bundle.databaseFormat
		engine = bundle.engine
		confidence = bundle.confidence
		evidence = bundle.evidence
		warnings = bundle.warnings
		result.FileSets = bundle.fileSets
	} else {
		format, engine, confidence, evidence, warnings, err = detectPayload(ctx, current)
		if err != nil {
			return nil, err
		}
		databaseFormat = format
	}
	if !current.temporary {
		if err := verifyOpenedFileUnchanged(current.file, openedInfo, resolved); err != nil {
			return nil, err
		}
	}
	result.Format = format
	result.DatabaseFormat = databaseFormat
	result.Engine = engine
	result.Confidence = confidence
	result.Evidence = append(result.Evidence, evidence...)
	result.Warnings = append(result.Warnings, warnings...)
	result.RequiredTools = requiredToolsFor(databaseFormat)
	if err := verifyInspectionManifestDescription(result); err != nil {
		return nil, err
	}
	addExtensionWarnings(result)
	return result, nil
}

func configuredMaxDecodedBytes(value int64) (int64, error) {
	if value < 0 {
		return 0, fmt.Errorf("maximum decoded size cannot be negative")
	}
	if value == 0 {
		return DefaultMaxDecodedBytes, nil
	}
	return value, nil
}

func resolveInspectionPath(path string) (string, os.FileInfo, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil, fmt.Errorf("backup file path is required")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", nil, fmt.Errorf("resolve backup file path %q: %w", path, err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("backup file not found: %s", abs)
		}
		return "", nil, fmt.Errorf("access backup file %q: %w", abs, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("backup source must not be a symbolic link: %s", abs)
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("backup source must be a regular file: %s", abs)
	}
	if info.Size() == 0 {
		return "", nil, fmt.Errorf("backup file is empty: %s", abs)
	}
	return abs, info, nil
}

func hashOpenedFile(ctx context.Context, file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, &contextReader{ctx: ctx, reader: file}); err != nil {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func verifyOpenedFileUnchanged(file *os.File, expected os.FileInfo, path string) error {
	current, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect backup file %q after reading: %w", path, err)
	}
	if !os.SameFile(current, expected) || current.Size() != expected.Size() || current.ModTime() != expected.ModTime() {
		return fmt.Errorf("backup file changed while it was being inspected: %s", path)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("backup file changed while it was being inspected: %s: %w", path, err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || !os.SameFile(pathInfo, expected) ||
		pathInfo.Size() != expected.Size() || pathInfo.ModTime() != expected.ModTime() {
		return fmt.Errorf("backup file changed while it was being inspected: %s", path)
	}
	return nil
}

func detectWrapper(source *payloadSource) (kind Wrapper, armored bool, err error) {
	prefix, err := readPrefix(source.file, wrapperPeekBytes)
	if err != nil {
		return "", false, fmt.Errorf("read backup wrapper signature: %w", err)
	}
	switch {
	case len(prefix) >= 3 && prefix[0] == 0x1f && prefix[1] == 0x8b && prefix[2] == 0x08:
		return WrapperGzip, false, nil
	case isZstdPrefix(prefix):
		return WrapperZstd, false, nil
	case isZipPrefix(prefix):
		return WrapperZip, false, nil
	case bytes.HasPrefix(prefix, []byte("age-encryption.org/v1\n")):
		return WrapperAge, false, nil
	case bytes.HasPrefix(bytes.TrimLeft(prefix, " \t\r\n"), []byte(armor.Header)):
		return WrapperAge, true, nil
	default:
		return "", false, nil
	}
}

func isZstdPrefix(prefix []byte) bool {
	if len(prefix) < 4 {
		return false
	}
	if bytes.Equal(prefix[:4], []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		return true
	}
	return prefix[0] >= 0x50 && prefix[0] <= 0x5f &&
		prefix[1] == 0x2a && prefix[2] == 0x4d && prefix[3] == 0x18
}

func isZipPrefix(prefix []byte) bool {
	if len(prefix) < 4 || prefix[0] != 'P' || prefix[1] != 'K' {
		return false
	}
	return (prefix[2] == 0x03 && prefix[3] == 0x04) ||
		(prefix[2] == 0x05 && prefix[3] == 0x06) ||
		(prefix[2] == 0x06 && prefix[3] == 0x06) ||
		(prefix[2] == 0x07 && prefix[3] == 0x08)
}

func wrapperEvidence(kind Wrapper, armored bool) string {
	switch kind {
	case WrapperGzip:
		return "gzip magic 1f8b08"
	case WrapperZstd:
		return "Zstandard frame magic"
	case WrapperZip:
		return "ZIP file signature"
	case WrapperAge:
		if armored {
			return "armored age file header"
		}
		return "age-encryption.org/v1 header"
	default:
		return string(kind)
	}
}

func decodeWrapper(ctx context.Context, source *payloadSource, kind Wrapper, armored bool, opts InspectOptions, maxDecoded int64) (*payloadSource, error) {
	if _, err := source.file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind %s payload: %w", kind, err)
	}

	var reader io.Reader
	var closeReader func()
	switch kind {
	case WrapperGzip:
		decoded, err := gzip.NewReader(source.file)
		if err != nil {
			return nil, fmt.Errorf("invalid gzip backup: %w", err)
		}
		reader = decoded
		closeReader = func() { _ = decoded.Close() }
	case WrapperZstd:
		maxMemory := uint64(maxDecoded)
		if maxMemory < 1<<20 {
			maxMemory = 1 << 20
		}
		decoded, err := zstd.NewReader(source.file,
			zstd.WithDecoderLowmem(true),
			zstd.WithDecoderMaxMemory(maxMemory),
		)
		if err != nil {
			return nil, fmt.Errorf("invalid Zstandard backup: %w", err)
		}
		reader = decoded
		closeReader = decoded.Close
	case WrapperZip:
		zipReader, err := zip.NewReader(source.file, source.size)
		if err != nil {
			return nil, fmt.Errorf("invalid ZIP backup: %w", err)
		}
		if len(zipReader.File) != 1 || !zipReader.File[0].FileInfo().Mode().IsRegular() {
			return nil, fmt.Errorf("ZIP backup must contain exactly one regular file (found %d entries)", len(zipReader.File))
		}
		entry := zipReader.File[0]
		if entry.Flags&0x1 != 0 {
			return nil, fmt.Errorf("encrypted ZIP backups are not supported; use age encryption instead")
		}
		if entry.UncompressedSize64 > uint64(maxDecoded) {
			return nil, decodedLimitError(maxDecoded)
		}
		decoded, err := entry.Open()
		if err != nil {
			return nil, fmt.Errorf("open ZIP backup entry %q: %w", entry.Name, err)
		}
		reader = decoded
		closeReader = func() { _ = decoded.Close() }
	case WrapperAge:
		identities, err := readAgeIdentities(opts.AgeIdentityPath)
		if err != nil {
			return nil, err
		}
		ageInput := io.Reader(source.file)
		if armored {
			ageInput = armor.NewReader(ageInput)
		}
		decoded, err := age.Decrypt(ageInput, identities...)
		if err != nil {
			return nil, fmt.Errorf("decrypt age backup: %w", err)
		}
		reader = decoded
	default:
		return nil, fmt.Errorf("unsupported backup wrapper %q", kind)
	}
	if closeReader != nil {
		defer closeReader()
	}

	next, err := materializeDecoded(ctx, reader, maxDecoded)
	if err != nil {
		return nil, fmt.Errorf("decode %s backup: %w", kind, err)
	}
	return next, nil
}

func readAgeIdentities(path string) ([]age.Identity, error) {
	return readAgeIdentitiesWithOps(path, os.Lstat, os.Open)
}

func readAgeIdentitiesWithOps(
	path string,
	lstat func(string) (os.FileInfo, error),
	open func(string) (*os.File, error),
) ([]age.Identity, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("age identity file is required")
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("resolve age identity path %q: %w", path, err)
	}
	info, err := lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("inspect age identity file %q: %w", abs, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, fmt.Errorf("age identity must be a non-empty regular file, not a symlink: %s", abs)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("age identity file permissions are too broad (%04o); require 0600 or stricter: %s", info.Mode().Perm(), abs)
	}
	file, err := open(abs)
	if err != nil {
		return nil, fmt.Errorf("open age identity file %q: %w", abs, err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect age identity file %q: %w", abs, err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("age identity file changed while it was being opened: %s", abs)
	}
	identities, err := age.ParseIdentities(bufio.NewReader(file))
	if err != nil {
		// age parser errors can quote the full invalid input line. Do not echo a
		// malformed private key (or another accidentally selected secret file)
		// into terminal output or logs.
		return nil, fmt.Errorf("parse age identity file %q: identity data is invalid", abs)
	}
	if len(identities) == 0 {
		return nil, fmt.Errorf("age identity file contains no identities: %s", abs)
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(openedInfo, after) || openedInfo.Size() != after.Size() || openedInfo.ModTime() != after.ModTime() {
		return nil, fmt.Errorf("age identity file changed while it was being read: %s", abs)
	}
	currentPath, err := lstat(abs)
	if err != nil || currentPath.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, currentPath) ||
		openedInfo.Size() != currentPath.Size() || openedInfo.ModTime() != currentPath.ModTime() {
		return nil, fmt.Errorf("age identity file changed while it was being read: %s", abs)
	}
	return identities, nil
}

func materializeDecoded(ctx context.Context, reader io.Reader, maxDecoded int64) (*payloadSource, error) {
	file, err := privatefile.CreateTemp("", "dbterm-inspect-", "")
	if err != nil {
		return nil, fmt.Errorf("create private inspection file: %w", err)
	}
	path := file.Name()
	fail := func(err error) (*payloadSource, error) {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}

	readLimit := maxDecoded
	if maxDecoded < math.MaxInt64 {
		readLimit++
	}
	limited := io.LimitReader(&contextReader{ctx: ctx, reader: reader}, readLimit)
	written, err := io.Copy(file, limited)
	if err != nil {
		return fail(err)
	}
	if written > maxDecoded {
		return fail(decodedLimitError(maxDecoded))
	}
	if written == 0 {
		return fail(fmt.Errorf("decoded backup payload is empty"))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(fmt.Errorf("rewind decoded backup: %w", err))
	}
	return &payloadSource{file: file, path: path, size: written, temporary: true}, nil
}

func decodedLimitError(max int64) error {
	return fmt.Errorf("decoded backup exceeds the configured limit of %d bytes", max)
}

func detectPayload(ctx context.Context, source *payloadSource) (Format, config.DBType, string, []string, []string, error) {
	prefix, err := readPrefix(source.file, payloadPeekBytes)
	if err != nil {
		return FormatUnknown, "", ConfidenceUnknown, nil, nil, fmt.Errorf("read decoded backup payload: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return FormatUnknown, "", ConfidenceUnknown, nil, nil, err
	}

	if bytes.HasPrefix(prefix, []byte("PGDMP")) {
		return FormatPostgresCustom, config.PostgreSQL, ConfidenceExact,
			[]string{"PGDMP custom archive magic"}, nil, nil
	}
	if bytes.HasPrefix(prefix, []byte("SQLite format 3\x00")) {
		if err := validateSQLiteHeader(prefix, source.size); err != nil {
			return FormatUnknown, "", ConfidenceUnknown, nil,
				[]string{fmt.Sprintf("SQLite signature found, but the header is invalid: %v", err)}, nil
		}
		return FormatSQLiteDatabase, config.SQLite, ConfidenceExact,
			[]string{"SQLite format 3 header"}, nil, nil
	}

	isPGTar, tarWarning, err := detectPostgresTar(ctx, source, prefix)
	if err != nil {
		return FormatUnknown, "", ConfidenceUnknown, nil, nil, err
	}
	if isPGTar {
		return FormatPostgresTar, config.PostgreSQL, ConfidenceExact,
			[]string{"tar archive contains toc.dat with PGDMP header"}, nil, nil
	}

	format, engine, confidence, evidence, warnings := detectSQL(prefix)
	if tarWarning != "" {
		warnings = append(warnings, tarWarning)
	}
	return format, engine, confidence, evidence, warnings, nil
}

func validateSQLiteHeader(prefix []byte, size int64) error {
	if len(prefix) < 100 {
		return fmt.Errorf("file is shorter than the 100-byte database header")
	}
	pageField := binary.BigEndian.Uint16(prefix[16:18])
	pageSize := int64(pageField)
	if pageField == 1 {
		pageSize = 65536
	}
	if pageSize < 512 || pageSize > 65536 || pageSize&(pageSize-1) != 0 {
		return fmt.Errorf("invalid page size %d", pageSize)
	}
	if prefix[18] < 1 || prefix[18] > 2 || prefix[19] < 1 || prefix[19] > 2 {
		return fmt.Errorf("unsupported read/write format versions %d/%d", prefix[18], prefix[19])
	}
	if prefix[21] != 64 || prefix[22] != 32 || prefix[23] != 32 {
		return fmt.Errorf("invalid payload fractions %d/%d/%d", prefix[21], prefix[22], prefix[23])
	}
	if size < pageSize || size%pageSize != 0 {
		return fmt.Errorf("file size %d is not a whole number of %d-byte pages", size, pageSize)
	}
	return nil
}

func detectPostgresTar(ctx context.Context, source *payloadSource, prefix []byte) (bool, string, error) {
	looksLikeTar := len(prefix) >= 265 &&
		(bytes.Equal(prefix[257:263], []byte("ustar\x00")) || bytes.Equal(prefix[257:263], []byte("ustar ")))
	if _, err := source.file.Seek(0, io.SeekStart); err != nil {
		return false, "", fmt.Errorf("rewind possible PostgreSQL tar archive: %w", err)
	}
	reader := tar.NewReader(&contextReader{ctx: ctx, reader: source.file})
	header, err := reader.Next()
	if err != nil {
		if looksLikeTar && !errors.Is(err, io.EOF) {
			return false, fmt.Sprintf("Tar signature found, but the archive header is invalid: %v", err), nil
		}
		return false, "", nil
	}
	if header.Name != "toc.dat" {
		if looksLikeTar {
			return false, "Tar archive is not a PostgreSQL dump: first entry is not toc.dat.", nil
		}
		return false, "", nil
	}
	var magic [5]byte
	if _, err := io.ReadFull(reader, magic[:]); err != nil {
		return false, "Tar archive has a truncated PostgreSQL toc.dat entry.", nil
	}
	if string(magic[:]) != "PGDMP" {
		return false, "Tar archive toc.dat does not contain PostgreSQL archive magic.", nil
	}
	return true, "", nil
}

var genericSQLStatement = regexp.MustCompile(`(?im)^\s*(SELECT|WITH|CREATE|INSERT|REPLACE|UPDATE|DELETE|DROP|ALTER|BEGIN|COMMIT|SET|COPY|PRAGMA)\b`)

func detectSQL(prefix []byte) (Format, config.DBType, string, []string, []string) {
	if !looksTextual(prefix) {
		return FormatUnknown, "", ConfidenceUnknown, nil, nil
	}
	text := strings.TrimPrefix(string(prefix), "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	trimmed := strings.TrimLeft(text, " \t\n")
	lower := strings.ToLower(trimmed)

	if strings.HasPrefix(lower, "--\n-- postgresql database dump\n--") ||
		strings.HasPrefix(lower, "--\n-- postgresql database cluster dump\n--") {
		return FormatPostgresSQL, config.PostgreSQL, ConfidenceStrong,
			[]string{"PostgreSQL dump header"}, nil
	}
	if strings.HasPrefix(lower, "-- mysql dump") || strings.HasPrefix(lower, "-- mariadb dump") ||
		strings.HasPrefix(lower, "/*m!999999\\- enable the sandbox mode */") {
		return FormatMySQLSQL, config.MySQL, ConfidenceStrong,
			[]string{"MySQL/MariaDB dump header"}, nil
	}
	if strings.HasPrefix(lower, "pragma foreign_keys=off;") && strings.Contains(lower, "begin transaction;") {
		return FormatSQLiteSQL, config.SQLite, ConfidenceStrong,
			[]string{"SQLite dump PRAGMA and transaction header"}, nil
	}

	pgEvidence := matchingEvidence(lower, []marker{
		{"set client_encoding", "PostgreSQL SET client_encoding"},
		{"set standard_conforming_strings", "PostgreSQL SET standard_conforming_strings"},
		{"select pg_catalog.set_config", "PostgreSQL pg_catalog.set_config"},
		{" from stdin;", "PostgreSQL COPY FROM stdin"},
	})
	myEvidence := matchingEvidence(lower, []marker{
		{"/*!40", "MySQL versioned comment"},
		{"lock tables ", "MySQL LOCK TABLES"},
		{"unlock tables", "MySQL UNLOCK TABLES"},
		{" engine=", "MySQL storage ENGINE clause"},
		{"delimiter ", "MySQL DELIMITER command"},
	})
	sqliteEvidence := matchingEvidence(lower, []marker{
		{"pragma foreign_keys", "SQLite PRAGMA foreign_keys"},
		{"sqlite_sequence", "SQLite sqlite_sequence table"},
		{"without rowid", "SQLite WITHOUT ROWID"},
	})

	nonEmptyScores := 0
	for _, evidence := range [][]string{pgEvidence, myEvidence, sqliteEvidence} {
		if len(evidence) > 0 {
			nonEmptyScores++
		}
	}
	if nonEmptyScores > 1 {
		all := append(append(append([]string{}, pgEvidence...), myEvidence...), sqliteEvidence...)
		return FormatGenericSQL, "", ConfidenceAmbiguous, all,
			[]string{"SQL contains conflicting database-specific markers; restore is blocked because choosing an engine would be unsafe."}
	}
	if len(pgEvidence) > 0 {
		return FormatPostgresSQL, config.PostgreSQL, ConfidenceStrong, pgEvidence, nil
	}
	if len(myEvidence) > 0 {
		return FormatMySQLSQL, config.MySQL, ConfidenceStrong, myEvidence, nil
	}
	if len(sqliteEvidence) > 0 {
		return FormatSQLiteSQL, config.SQLite, ConfidenceStrong, sqliteEvidence, nil
	}
	if genericSQLStatement.MatchString(text) {
		return FormatGenericSQL, "", ConfidenceAmbiguous,
			[]string{"SQL statements found without a database-specific signature"},
			[]string{"SQL dialect is ambiguous; restore is blocked. Export an engine-specific dump with a recognizable header."}
	}
	return FormatUnknown, "", ConfidenceUnknown, nil, nil
}

type marker struct {
	needle   string
	evidence string
}

func matchingEvidence(text string, markers []marker) []string {
	var evidence []string
	for _, item := range markers {
		if strings.Contains(text, item.needle) {
			evidence = append(evidence, item.evidence)
		}
	}
	return evidence
}

func looksTextual(data []byte) bool {
	if len(data) == 0 || bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	var controls int
	for _, b := range data {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' && b != '\f' {
			controls++
		}
	}
	return controls*100 <= len(data)*2
}

func readPrefix(file *os.File, limit int64) ([]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return data, nil
}

func requiredToolsFor(format Format) []string {
	switch format {
	case FormatPostgresCustom, FormatPostgresTar:
		return []string{"pg_restore"}
	case FormatPostgresSQL:
		return []string{"psql"}
	case FormatMySQLSQL:
		return []string{"mysql"}
	case FormatSQLiteSQL:
		return []string{"sqlite3"}
	default:
		return nil
	}
}

func addExtensionWarnings(result *Inspection) {
	if result == nil {
		return
	}
	name := strings.ToLower(result.Path)
	for _, wrapper := range result.Wrappers {
		var suffixes []string
		switch wrapper {
		case WrapperGzip:
			suffixes = []string{".gz", ".gzip"}
		case WrapperZstd:
			suffixes = []string{".zst", ".zstd"}
		case WrapperZip:
			suffixes = []string{".zip"}
		case WrapperAge:
			suffixes = []string{".age"}
		}
		matched := ""
		for _, suffix := range suffixes {
			if strings.HasSuffix(name, suffix) {
				matched = suffix
				break
			}
		}
		if matched == "" {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Detected %s wrapper, but the filename does not use its usual extension.", wrapper))
			continue
		}
		name = strings.TrimSuffix(name, matched)
	}

	ext := filepath.Ext(name)
	if ext == "" || extensionMatchesFormat(ext, result.Format) {
		return
	}
	result.Warnings = append(result.Warnings,
		fmt.Sprintf("File extension %q does not match detected %s content; file contents were used.", ext, formatLabel(result.Format)))
}

func extensionMatchesFormat(ext string, format Format) bool {
	switch format {
	case FormatPostgresCustom:
		return ext == ".dump" || ext == ".backup" || ext == ".pgdump"
	case FormatPostgresTar:
		return ext == ".tar"
	case FormatPostgresSQL, FormatMySQLSQL, FormatSQLiteSQL, FormatGenericSQL:
		return ext == ".sql"
	case FormatSQLiteDatabase:
		return ext == ".sqlite" || ext == ".sqlite3" || ext == ".db" || ext == ".db3"
	case FormatDBTermBundle:
		return ext == ".dbterm"
	default:
		return true
	}
}

func formatLabel(format Format) string {
	if format == FormatUnknown {
		return "unknown"
	}
	return strings.ReplaceAll(string(format), "_", " ")
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if err == nil {
		if ctxErr := r.ctx.Err(); ctxErr != nil {
			return n, ctxErr
		}
	}
	return n, err
}
