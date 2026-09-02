package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/shreyam1008/dbterm/internal/appdirs"
	"github.com/shreyam1008/dbterm/internal/config"
	"github.com/shreyam1008/dbterm/internal/privatefile"
)

var reportedDBTermVersion atomic.Value

const (
	ArtifactManifestSchemaVersion = 1
	ArtifactManifestSuffix        = ".dbterm.json"
	ArtifactVerificationPassed    = "passed"
	ArtifactVerificationBasic     = "basic-structural"
	EncryptionSchemeNone          = "none"
	EncryptionSchemeAgeX25519V1   = "age-x25519-v1"

	maxArtifactManifestBytes = 1 << 20
	producerIDFilename       = "producer-id"
)

// ManifestFileSet reserves the portable shape used by future dbterm bundles.
// Database-only backups deliberately publish an empty list rather than null.
type ManifestFileSet struct {
	Label        string   `json:"label"`
	FileCount    int64    `json:"file_count"`
	SizeBytes    int64    `json:"size_bytes"`
	Consistency  string   `json:"consistency"`
	ChangedFiles []string `json:"changed_files"`
	Warnings     []string `json:"warnings"`
}

// ArtifactManifest is the portable completion contract beside a verified
// artifact. It contains recovery metadata only: never connection details,
// credentials, private age identities, or the full public recipient.
type ArtifactManifest struct {
	SchemaVersion     int               `json:"schema_version"`
	ArtifactID        string            `json:"artifact_id"`
	RunID             string            `json:"run_id"`
	JobID             string            `json:"job_id"`
	CreatedAt         time.Time         `json:"created_at"`
	ProducerID        string            `json:"producer_id"`
	DBTermVersion     string            `json:"dbterm_version"`
	Engine            config.DBType     `json:"engine"`
	Format            string            `json:"format"`
	Compression       Compression       `json:"compression"`
	Encryption        string            `json:"encryption"`
	Encrypted         bool              `json:"encrypted"`
	SizeBytes         int64             `json:"size_bytes"`
	SHA256            string            `json:"sha256"`
	Verification      string            `json:"verification"`
	VerificationLevel string            `json:"verification_level"`
	FileSets          []ManifestFileSet `json:"file_sets"`
	Warnings          []string          `json:"warnings"`
}

func (manifest ArtifactManifest) Validate() error {
	if manifest.SchemaVersion != ArtifactManifestSchemaVersion {
		return fmt.Errorf("unsupported dbterm artifact manifest schema version %d", manifest.SchemaVersion)
	}
	for label, value := range map[string]string{
		"artifact ID": manifest.ArtifactID,
		"run ID":      manifest.RunID,
		"job ID":      manifest.JobID,
		"producer ID": manifest.ProducerID,
	} {
		if err := validateManifestText(label, value, 256); err != nil {
			return err
		}
	}
	if manifest.CreatedAt.IsZero() {
		return fmt.Errorf("artifact manifest creation time is required")
	}
	if err := validateManifestText("dbterm version", manifest.DBTermVersion, 128); err != nil {
		return err
	}
	if err := validateManifestEngineFormat(manifest.Engine, manifest.Format); err != nil {
		return err
	}
	switch manifest.Compression {
	case CompressionNone, CompressionGzip, CompressionZip, CompressionZstd:
	default:
		return fmt.Errorf("artifact manifest has unsupported compression %q", manifest.Compression)
	}
	switch manifest.Encryption {
	case EncryptionSchemeNone:
		if manifest.Encrypted {
			return fmt.Errorf("artifact manifest marks encryption as both none and enabled")
		}
	case EncryptionSchemeAgeX25519V1:
		if !manifest.Encrypted {
			return fmt.Errorf("artifact manifest marks age encryption as disabled")
		}
	default:
		return fmt.Errorf("artifact manifest has unsupported encryption %q", manifest.Encryption)
	}
	if manifest.SizeBytes <= 0 {
		return fmt.Errorf("artifact manifest size must be greater than zero")
	}
	if !validSHA256(manifest.SHA256) {
		return fmt.Errorf("artifact manifest SHA-256 must contain exactly 64 hexadecimal characters")
	}
	if manifest.Verification != ArtifactVerificationPassed {
		return fmt.Errorf("artifact manifest verification must be %q", ArtifactVerificationPassed)
	}
	if manifest.VerificationLevel != ArtifactVerificationBasic {
		return fmt.Errorf("artifact manifest verification level must be %q", ArtifactVerificationBasic)
	}
	for _, warning := range manifest.Warnings {
		if err := validateManifestText("artifact warning", warning, 4096); err != nil {
			return err
		}
	}
	for _, fileSet := range manifest.FileSets {
		if err := validateManifestFileSet(fileSet); err != nil {
			return err
		}
	}
	return nil
}

func validateManifestEngineFormat(engine config.DBType, format string) error {
	expected := ""
	switch engine {
	case config.PostgreSQL:
		expected = string(FormatPostgresCustom)
	case config.MySQL:
		expected = string(FormatMySQLSQL)
	case config.SQLite:
		expected = string(FormatSQLiteDatabase)
	case config.Turso, config.CloudflareD1:
		expected = string(FormatSQLiteSQL)
	default:
		return fmt.Errorf("artifact manifest has unsupported database engine %q", engine)
	}
	if format != expected {
		return fmt.Errorf("artifact manifest format %q is invalid for database engine %q; expected %q", format, engine, expected)
	}
	return nil
}

func validateManifestFileSet(fileSet ManifestFileSet) error {
	if err := validateManifestText("file-set label", fileSet.Label, 256); err != nil {
		return err
	}
	if fileSet.FileCount < 0 || fileSet.SizeBytes < 0 {
		return fmt.Errorf("artifact manifest file-set counts cannot be negative")
	}
	if err := validateManifestText("file-set consistency", fileSet.Consistency, 128); err != nil {
		return err
	}
	for _, value := range append(append([]string{}, fileSet.ChangedFiles...), fileSet.Warnings...) {
		if err := validateManifestText("file-set detail", value, 4096); err != nil {
			return err
		}
	}
	return nil
}

func validateManifestText(label, value string, max int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("artifact manifest %s is required", label)
	}
	if len(value) > max {
		return fmt.Errorf("artifact manifest %s is too long", label)
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return fmt.Errorf("artifact manifest %s contains control characters", label)
		}
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

// EncodeArtifactManifest writes one strict schema-v1 JSON document.
func EncodeArtifactManifest(writer io.Writer, manifest ArtifactManifest) error {
	if writer == nil {
		return fmt.Errorf("artifact manifest writer is required")
	}
	if manifest.FileSets == nil {
		manifest.FileSets = []ManifestFileSet{}
	}
	if manifest.Warnings == nil {
		manifest.Warnings = []string{}
	}
	if err := manifest.Validate(); err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(manifest); err != nil {
		return fmt.Errorf("encode artifact manifest: %w", err)
	}
	return nil
}

// DecodeArtifactManifest rejects unknown fields, trailing JSON values, and
// unsupported schema versions so a vault never guesses at publication data.
func DecodeArtifactManifest(reader io.Reader) (*ArtifactManifest, error) {
	if reader == nil {
		return nil, fmt.Errorf("artifact manifest reader is required")
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxArtifactManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read artifact manifest: %w", err)
	}
	if len(data) > maxArtifactManifestBytes {
		return nil, fmt.Errorf("artifact manifest exceeds %d bytes", maxArtifactManifestBytes)
	}
	if err := validateManifestJSONKeys(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest ArtifactManifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode artifact manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("artifact manifest contains more than one JSON value")
		}
		return nil, fmt.Errorf("decode artifact manifest trailing data: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	if manifest.FileSets == nil {
		manifest.FileSets = []ManifestFileSet{}
	}
	if manifest.Warnings == nil {
		manifest.Warnings = []string{}
	}
	return &manifest, nil
}

func validateManifestJSONKeys(data []byte) error {
	allowed := map[string]struct{}{
		"schema_version": {}, "artifact_id": {}, "run_id": {}, "job_id": {},
		"created_at": {}, "producer_id": {}, "dbterm_version": {}, "engine": {},
		"format": {}, "compression": {}, "encryption": {}, "encrypted": {},
		"size_bytes": {}, "sha256": {}, "verification": {}, "verification_level": {},
		"file_sets": {}, "warnings": {},
	}
	fileSetAllowed := map[string]struct{}{
		"label": {}, "file_count": {}, "size_bytes": {}, "consistency": {},
		"changed_files": {}, "warnings": {},
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode artifact manifest: %w", err)
	}
	if opening != json.Delim('{') {
		return fmt.Errorf("decode artifact manifest: top-level value must be an object")
	}
	seen := make(map[string]struct{}, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode artifact manifest field: %w", err)
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("decode artifact manifest: object key is not text")
		}
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("decode artifact manifest: unknown field or non-canonical spelling %q", key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("decode artifact manifest: duplicate field %q", key)
		}
		seen[key] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return fmt.Errorf("decode artifact manifest field %q: %w", key, err)
		}
		switch key {
		case "file_sets":
			var fileSets []json.RawMessage
			if err := decodeRequiredJSONArray(raw, &fileSets); err != nil {
				return fmt.Errorf("decode artifact manifest file_sets: %w", err)
			}
			for index, fileSet := range fileSets {
				if err := validateExactJSONObjectKeys(fileSet, fileSetAllowed); err != nil {
					return fmt.Errorf("decode artifact manifest file_sets[%d]: %w", index, err)
				}
			}
		case "warnings":
			var warnings []json.RawMessage
			if err := decodeRequiredJSONArray(raw, &warnings); err != nil {
				return fmt.Errorf("decode artifact manifest warnings: %w", err)
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return fmt.Errorf("decode artifact manifest: %w", err)
	}
	if len(seen) != len(allowed) {
		return fmt.Errorf("decode artifact manifest: one or more required fields are missing")
	}
	return nil
}

func validateExactJSONObjectKeys(data []byte, allowed map[string]struct{}) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil {
		return err
	}
	if opening != json.Delim('{') {
		return fmt.Errorf("value must be an object")
	}
	seen := make(map[string]struct{}, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("object key is not text")
		}
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown field or non-canonical spelling %q", key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate field %q", key)
		}
		seen[key] = struct{}{}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return fmt.Errorf("decode field %q: %w", key, err)
		}
		if key == "changed_files" || key == "warnings" {
			var values []json.RawMessage
			if err := decodeRequiredJSONArray(raw, &values); err != nil {
				return fmt.Errorf("field %q: %w", key, err)
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	if len(seen) != len(allowed) {
		return fmt.Errorf("one or more required fields are missing")
	}
	return nil
}

func decodeRequiredJSONArray(data []byte, destination any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return fmt.Errorf("value must be an array, not null")
	}
	if err := json.Unmarshal(trimmed, destination); err != nil {
		return fmt.Errorf("value must be an array: %w", err)
	}
	return nil
}

// ReadArtifactManifest reads a local sidecar without following a symlink.
func ReadArtifactManifest(path string) (*ArtifactManifest, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("artifact manifest path is required")
	}
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect artifact manifest %q: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact manifest must be a regular file, not a symlink: %s", path)
	}
	if info.Size() <= 0 || info.Size() > maxArtifactManifestBytes {
		return nil, fmt.Errorf("artifact manifest size must be between 1 and %d bytes: %s", maxArtifactManifestBytes, path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open artifact manifest %q: %w", path, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened artifact manifest %q: %w", path, err)
	}
	if !os.SameFile(info, opened) {
		return nil, fmt.Errorf("artifact manifest changed while it was being opened: %s", path)
	}
	manifest, err := DecodeArtifactManifest(file)
	if err != nil {
		return nil, fmt.Errorf("read artifact manifest %q: %w", path, err)
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || opened.ModTime() != after.ModTime() {
		return nil, fmt.Errorf("artifact manifest changed while it was being read: %s", path)
	}
	currentPath, err := os.Lstat(path)
	if err != nil || currentPath.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, currentPath) ||
		opened.Size() != currentPath.Size() || opened.ModTime() != currentPath.ModTime() {
		return nil, fmt.Errorf("artifact manifest changed while it was being read: %s", path)
	}
	return manifest, nil
}

func artifactManifestPath(artifactPath string) string {
	return artifactPath + ArtifactManifestSuffix
}

// ReadArtifactManifestForArtifact returns the sidecar only when it exists.
// Missing sidecars preserve inspection compatibility for legacy artifacts.
func ReadArtifactManifestForArtifact(artifactPath string) (*ArtifactManifest, bool, error) {
	path := artifactManifestPath(artifactPath)
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("inspect artifact manifest %q: %w", path, err)
	}
	manifest, err := ReadArtifactManifest(path)
	if err != nil {
		return nil, false, err
	}
	return manifest, true, nil
}

func writeArtifactManifestStage(directory string, manifest ArtifactManifest) (path string, size int64, digest string, err error) {
	file, err := privatefile.CreateTemp(directory, ".dbterm-manifest-", ".partial")
	if err != nil {
		return "", 0, "", fmt.Errorf("create private artifact manifest staging file: %w", err)
	}
	path = file.Name()
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", 0, "", fmt.Errorf("protect artifact manifest staging file: %w", err)
	}
	hash := sha256.New()
	if err := EncodeArtifactManifest(io.MultiWriter(file, hash), manifest); err != nil {
		return "", 0, "", err
	}
	if err := file.Sync(); err != nil {
		return "", 0, "", fmt.Errorf("sync artifact manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", 0, "", fmt.Errorf("close artifact manifest: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, "", fmt.Errorf("inspect staged artifact manifest: %w", err)
	}
	cleanup = false
	return path, info.Size(), hex.EncodeToString(hash.Sum(nil)), nil
}

func buildArtifactManifest(job Job, cfg *config.ConnectionConfig, runID, artifactID, producerID, version string, createdAt time.Time, format string, size int64, digest string) (ArtifactManifest, error) {
	manifest := ArtifactManifest{
		SchemaVersion: ArtifactManifestSchemaVersion,
		ArtifactID:    artifactID, RunID: runID, JobID: job.ID,
		CreatedAt: createdAt, ProducerID: producerID, DBTermVersion: version,
		Engine: cfg.Type, Format: format, Compression: job.Compression,
		Encryption: EncryptionSchemeNone, Encrypted: false,
		SizeBytes: size, SHA256: strings.ToLower(digest), Verification: ArtifactVerificationPassed, VerificationLevel: ArtifactVerificationBasic,
		FileSets: []ManifestFileSet{}, Warnings: []string{},
	}
	if job.Encryption == EncryptionAge {
		manifest.Encryption = EncryptionSchemeAgeX25519V1
		manifest.Encrypted = true
	}
	return manifest, manifest.Validate()
}

func currentDBTermVersion() string {
	if value, ok := reportedDBTermVersion.Load().(string); ok && strings.TrimSpace(value) != "" {
		return value
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		version := strings.TrimSpace(info.Main.Version)
		if version != "" && version != "(devel)" {
			return strings.TrimPrefix(version, "v")
		}
	}
	return "dev"
}

// SetDBTermVersion lets the main package report its linker-provided release
// version to backup manifests. Library callers safely fall back to build info.
func SetDBTermVersion(version string) {
	version = strings.TrimSpace(version)
	if version != "" {
		reportedDBTermVersion.Store(strings.TrimPrefix(version, "v"))
	}
}

func resolveProducerID(override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		value := strings.TrimSpace(override)
		if err := validateManifestText("producer ID", value, 256); err != nil {
			return "", err
		}
		return value, nil
	}
	state, err := appdirs.StateDir()
	if err != nil {
		return "", fmt.Errorf("resolve producer identity state: %w", err)
	}
	directory := filepath.Join(state, "backup")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create producer identity state directory: %w", err)
	}
	path := filepath.Join(directory, producerIDFilename)
	for attempts := 0; attempts < 2; attempts++ {
		value, readErr := readProducerID(path)
		if readErr == nil {
			return value, nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return "", readErr
		}
		value, err = NewID("producer")
		if err != nil {
			return "", err
		}
		file, createErr := privatefile.CreateTemp(directory, ".producer-id-", ".partial")
		if createErr != nil {
			return "", fmt.Errorf("stage producer identity: %w", createErr)
		}
		stagePath := file.Name()
		defer func() {
			_ = file.Close()
			_ = os.Remove(stagePath)
		}()
		if _, err = io.WriteString(file, value+"\n"); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return "", fmt.Errorf("stage producer identity: %w", err)
		}
		publishErr := os.Link(stagePath, path)
		if publishErr != nil {
			publishErr = atomicPublishNoReplace(stagePath, path)
		}
		if publishErr != nil {
			if existing, readErr := readProducerID(path); readErr == nil {
				return existing, nil
			}
			return "", fmt.Errorf("publish producer identity without replacement: %w", publishErr)
		}
		if err := syncDirectory(directory); err != nil {
			return "", fmt.Errorf("sync producer identity directory: %w", err)
		}
		_ = os.Remove(stagePath)
		return value, nil
	}
	return readProducerID(path)
}

func readProducerID(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 512 {
		return "", fmt.Errorf("producer identity must be a small regular file: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open producer identity: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", fmt.Errorf("producer identity changed while it was being opened: %s", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, 513))
	if err != nil {
		return "", fmt.Errorf("read producer identity: %w", err)
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || opened.ModTime() != after.ModTime() {
		return "", fmt.Errorf("producer identity changed while it was being read: %s", path)
	}
	currentPath, err := os.Lstat(path)
	if err != nil || currentPath.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, currentPath) ||
		opened.Size() != currentPath.Size() || opened.ModTime() != currentPath.ModTime() {
		return "", fmt.Errorf("producer identity changed while it was being read: %s", path)
	}
	value := strings.TrimSpace(string(data))
	if err := validateManifestText("producer ID", value, 256); err != nil {
		return "", err
	}
	return value, nil
}

func verifyManifestArtifact(manifest *ArtifactManifest, artifact Artifact) error {
	if manifest == nil {
		return fmt.Errorf("artifact manifest is required")
	}
	if artifact.ID != "" && manifest.ArtifactID != artifact.ID {
		return fmt.Errorf("artifact manifest ID %q does not match catalog ID %q", manifest.ArtifactID, artifact.ID)
	}
	if manifest.SizeBytes != artifact.Size {
		return fmt.Errorf("artifact manifest size %d does not match artifact size %d", manifest.SizeBytes, artifact.Size)
	}
	if !strings.EqualFold(manifest.SHA256, artifact.SHA256) {
		return fmt.Errorf("artifact manifest SHA-256 does not match the artifact")
	}
	return nil
}

func verifyInspectionManifestDescription(inspection *Inspection) error {
	if inspection == nil || inspection.Manifest == nil {
		return nil
	}
	manifest := inspection.Manifest
	hasWrapper := func(expected Wrapper) bool {
		for _, wrapper := range inspection.Wrappers {
			if wrapper == expected {
				return true
			}
		}
		return false
	}
	ageWrapped := hasWrapper(WrapperAge)
	if manifest.Encrypted != ageWrapped {
		return fmt.Errorf("artifact completion manifest encryption does not match the artifact bytes")
	}
	if ageWrapped && manifest.Encryption != EncryptionSchemeAgeX25519V1 {
		return fmt.Errorf("artifact completion manifest encryption %q does not match the detected age payload", manifest.Encryption)
	}
	if !ageWrapped && manifest.Encryption != EncryptionSchemeNone {
		return fmt.Errorf("artifact completion manifest encryption %q does not match the unencrypted payload", manifest.Encryption)
	}
	if inspection.Confidence == ConfidenceLocked {
		// Without an identity only the outer age envelope is observable. The
		// compression and native payload are authenticated during full decoding.
		// age must still be the one observable outermost wrapper: Runner never
		// emits compression around ciphertext.
		if len(inspection.Wrappers) != 1 || inspection.Wrappers[0] != WrapperAge {
			return fmt.Errorf("artifact completion manifest wrapper order does not match dbterm's age encryption pipeline")
		}
		return nil
	}
	expected := make([]Wrapper, 0, 2)
	if manifest.Encrypted {
		expected = append(expected, WrapperAge)
	}
	switch manifest.Compression {
	case CompressionGzip:
		expected = append(expected, WrapperGzip)
	case CompressionZip:
		expected = append(expected, WrapperZip)
	case CompressionZstd:
		expected = append(expected, WrapperZstd)
	}
	if len(inspection.Wrappers) != len(expected) {
		return fmt.Errorf("artifact completion manifest wrapper stack does not match the artifact bytes")
	}
	for index := range expected {
		if inspection.Wrappers[index] != expected[index] {
			return fmt.Errorf("artifact completion manifest wrapper stack does not match the artifact bytes")
		}
	}
	if inspection.Confidence != ConfidenceLocked && inspection.Format != FormatUnknown && manifest.Format != string(inspection.Format) {
		return fmt.Errorf("artifact completion manifest format %q does not match detected format %q", manifest.Format, inspection.Format)
	}
	if !manifestEngineMatchesInspection(manifest.Engine, inspection.Engine) {
		return fmt.Errorf("artifact completion manifest engine %q does not match detected engine %q", manifest.Engine, inspection.Engine)
	}
	return nil
}

func manifestEngineMatchesInspection(manifestEngine, detectedEngine config.DBType) bool {
	if detectedEngine == "" {
		return true
	}
	if manifestEngine == detectedEngine {
		return true
	}
	// Turso and Cloudflare D1 backups intentionally use SQLite-compatible SQL,
	// so byte inspection identifies their recovery engine as SQLite.
	return detectedEngine == config.SQLite && (manifestEngine == config.Turso || manifestEngine == config.CloudflareD1)
}

func readRcloneArtifactManifest(ctx context.Context, object destinationSpec) (*ArtifactManifest, int64, string, error) {
	limited := &limitedManifestBuffer{remaining: maxArtifactManifestBytes + 1}
	if err := runRclone(ctx, limited, "cat", object.rclonePath()); err != nil {
		return nil, 0, "", err
	}
	if limited.overflow {
		return nil, 0, "", fmt.Errorf("artifact manifest exceeds %d bytes", maxArtifactManifestBytes)
	}
	data := []byte(limited.builder.String())
	manifest, err := DecodeArtifactManifest(strings.NewReader(string(data)))
	if err != nil {
		return nil, 0, "", err
	}
	digest := sha256.Sum256(data)
	return manifest, int64(len(data)), hex.EncodeToString(digest[:]), nil
}

func verifyManifestRun(manifest *ArtifactManifest, run Run) error {
	if err := verifyManifestArtifact(manifest, run.Artifact); err != nil {
		return err
	}
	if manifest.RunID != run.ID {
		return fmt.Errorf("artifact manifest run ID %q does not match catalog run %q", manifest.RunID, run.ID)
	}
	if manifest.JobID != run.JobID {
		return fmt.Errorf("artifact manifest job ID %q does not match catalog job %q", manifest.JobID, run.JobID)
	}
	return nil
}

type limitedManifestBuffer struct {
	builder   strings.Builder
	remaining int
	overflow  bool
}

func (buffer *limitedManifestBuffer) Write(data []byte) (int, error) {
	original := len(data)
	if len(data) > buffer.remaining {
		data = data[:buffer.remaining]
		buffer.overflow = true
	}
	if len(data) > 0 {
		_, _ = buffer.builder.Write(data)
		buffer.remaining -= len(data)
	}
	return original, nil
}
