package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/shreyam1008/dbterm/internal/config"
)

type dbtermBundleInspection struct {
	databaseFormat Format
	engine         config.DBType
	confidence     string
	evidence       []string
	warnings       []string
	fileSets       []ManifestFileSet
}

type expectedBundleEntry struct {
	name   string
	size   int64
	digest string
	db     bool
}

func inspectDBTermBundle(ctx context.Context, source *payloadSource, maxDecoded int64) (dbtermBundleInspection, bool, error) {
	if _, err := source.file.Seek(0, io.SeekStart); err != nil {
		return dbtermBundleInspection{}, false, err
	}
	reader := tar.NewReader(source.file)
	header, err := reader.Next()
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return dbtermBundleInspection{}, false, contextErr
		}
		_, _ = source.file.Seek(0, io.SeekStart)
		return dbtermBundleInspection{}, false, nil
	}
	if header.Name != dbtermBundleManifestPath {
		_, _ = source.file.Seek(0, io.SeekStart)
		return dbtermBundleInspection{}, false, nil
	}
	if !header.FileInfo().Mode().IsRegular() || header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > maxDBTermBundleManifest {
		return dbtermBundleInspection{}, true, fmt.Errorf("dbterm bundle must begin with a bounded regular %s entry", dbtermBundleManifestPath)
	}
	manifestData, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: reader}, maxDBTermBundleManifest+1))
	if err != nil {
		return dbtermBundleInspection{}, true, fmt.Errorf("read dbterm bundle manifest: %w", err)
	}
	if int64(len(manifestData)) != header.Size {
		return dbtermBundleInspection{}, true, fmt.Errorf("dbterm bundle manifest is truncated")
	}
	manifest, err := DecodeDBTermBundleManifest(bytes.NewReader(manifestData))
	if err != nil {
		return dbtermBundleInspection{}, true, err
	}

	expected := make([]expectedBundleEntry, 0, 1)
	expected = append(expected, expectedBundleEntry{name: manifest.Database.Path, size: manifest.Database.SizeBytes, digest: manifest.Database.SHA256, db: true})
	fileSets := make([]ManifestFileSet, 0, len(manifest.FileSets))
	for _, set := range manifest.FileSets {
		fileSets = append(fileSets, ManifestFileSet{
			Label: set.Label, FileCount: set.FileCount, SizeBytes: set.SizeBytes,
			Consistency: FileSetConsistencyBestEffort, ChangedFiles: []string{}, Warnings: []string{},
		})
		for _, file := range set.Files {
			expected = append(expected, expectedBundleEntry{
				name: path.Join("dbterm/files", set.Label, file.Path), size: file.SizeBytes, digest: file.SHA256,
			})
		}
	}

	result := dbtermBundleInspection{fileSets: fileSets, evidence: []string{"dbterm bundle schema v1 and deterministic entry layout"}}
	for _, wanted := range expected {
		if err := ctx.Err(); err != nil {
			return dbtermBundleInspection{}, true, err
		}
		header, err := reader.Next()
		if err != nil {
			return dbtermBundleInspection{}, true, fmt.Errorf("dbterm bundle is missing required entry %q: %w", wanted.name, err)
		}
		if header.Name != wanted.name {
			return dbtermBundleInspection{}, true, fmt.Errorf("dbterm bundle entry order mismatch: found %q, expected %q", header.Name, wanted.name)
		}
		if err := validateBundleArchivePath(header.Name); err != nil || header.Typeflag != tar.TypeReg || !header.FileInfo().Mode().IsRegular() {
			return dbtermBundleInspection{}, true, fmt.Errorf("dbterm bundle entry %q must be a safe regular file", header.Name)
		}
		if header.Size != wanted.size {
			return dbtermBundleInspection{}, true, fmt.Errorf("dbterm bundle entry %q size is %d; manifest records %d", header.Name, header.Size, wanted.size)
		}
		if wanted.db {
			if header.Size > maxDecoded {
				return dbtermBundleInspection{}, true, decodedLimitError(maxDecoded)
			}
			hash := sha256.New()
			database, err := materializeDecoded(ctx, io.TeeReader(reader, hash), maxDecoded)
			if err != nil {
				return dbtermBundleInspection{}, true, fmt.Errorf("extract dbterm bundle database payload: %w", err)
			}
			if database.size != wanted.size || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), wanted.digest) {
				database.cleanup()
				return dbtermBundleInspection{}, true, fmt.Errorf("dbterm bundle database payload does not match its size and SHA-256")
			}
			format, engine, confidence, evidence, warnings, err := detectPayload(ctx, database)
			database.cleanup()
			if err != nil {
				return dbtermBundleInspection{}, true, fmt.Errorf("inspect dbterm bundle database payload: %w", err)
			}
			if format == FormatDBTermBundle || string(format) != manifest.Database.Format || !manifestEngineMatchesInspection(manifest.Database.Engine, engine) {
				return dbtermBundleInspection{}, true, fmt.Errorf("dbterm bundle database payload does not match its declared engine and format")
			}
			result.databaseFormat = format
			result.engine = engine
			result.confidence = confidence
			result.evidence = append(result.evidence, fmt.Sprintf("verified embedded database payload: %s", formatLabel(format)))
			result.evidence = append(result.evidence, evidence...)
			result.warnings = append(result.warnings, warnings...)
			continue
		}
		written, digest, err := hashBundleEntry(&contextReader{ctx: ctx, reader: reader})
		if err != nil {
			return dbtermBundleInspection{}, true, fmt.Errorf("read dbterm bundle entry %q: %w", header.Name, err)
		}
		if written != wanted.size || !strings.EqualFold(digest, wanted.digest) {
			return dbtermBundleInspection{}, true, fmt.Errorf("dbterm bundle entry %q does not match its size and SHA-256", header.Name)
		}
	}
	if extra, err := reader.Next(); err == nil {
		return dbtermBundleInspection{}, true, fmt.Errorf("dbterm bundle contains unexpected entry %q", extra.Name)
	} else if !errors.Is(err, io.EOF) {
		return dbtermBundleInspection{}, true, fmt.Errorf("finish reading dbterm bundle: %w", err)
	}
	if _, err := source.file.Seek(0, io.SeekStart); err != nil {
		return dbtermBundleInspection{}, true, err
	}
	return result, true, nil
}

func verifyBundleFileSetSummaries(portable, included []ManifestFileSet) error {
	includedByLabel := make(map[string]ManifestFileSet, len(included))
	for _, set := range included {
		includedByLabel[strings.ToLower(set.Label)] = set
	}
	for _, set := range portable {
		key := strings.ToLower(set.Label)
		inside, exists := includedByLabel[key]
		switch set.Consistency {
		case FileSetConsistencyBestEffort:
			if !exists || inside.FileCount != set.FileCount || inside.SizeBytes != set.SizeBytes {
				return fmt.Errorf("completion manifest file-set summary %q does not match the dbterm bundle", set.Label)
			}
			delete(includedByLabel, key)
		case FileSetConsistencyOmitted:
			if exists || set.FileCount != 0 || set.SizeBytes != 0 || len(set.Warnings) == 0 {
				return fmt.Errorf("completion manifest omitted file-set summary %q is inconsistent", set.Label)
			}
		default:
			return fmt.Errorf("completion manifest file-set summary %q has unsupported consistency %q", set.Label, set.Consistency)
		}
	}
	if len(includedByLabel) != 0 {
		return fmt.Errorf("dbterm bundle contains a file set absent from its completion manifest")
	}
	return nil
}
