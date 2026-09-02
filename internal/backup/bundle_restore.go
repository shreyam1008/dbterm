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
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shreyam1008/dbterm/internal/privatefile"
)

const (
	DefaultMaxRestoreFileSetFiles int64 = maxFileSetFiles
	DefaultMaxRestoreFileSetBytes int64 = 10 << 30 // 10 GiB across selected sets.
)

type stagedRestoreFile struct {
	label        string
	relativePath string
	stagePath    string
	targetRoot   string
	targetPath   string
	size         int64
	digest       string
}

func buildRestoreFileSetPlan(inspection *Inspection, options RestoreOptions) ([]RestoreFileSetPlan, RestoreOptions, error) {
	if options.MaxFileSetFiles < 0 {
		return nil, options, fmt.Errorf("maximum restored file-set count cannot be negative")
	}
	if options.MaxFileSetBytes < 0 {
		return nil, options, fmt.Errorf("maximum restored file-set size cannot be negative")
	}
	if options.MaxFileSetFiles == 0 {
		options.MaxFileSetFiles = DefaultMaxRestoreFileSetFiles
	}
	if options.MaxFileSetBytes == 0 {
		options.MaxFileSetBytes = DefaultMaxRestoreFileSetBytes
	}
	if len(options.FileSetTargets) == 0 {
		options.FileSetTargets = nil
		return nil, options, nil
	}
	if inspection == nil || inspection.Format != FormatDBTermBundle {
		return nil, options, fmt.Errorf("file-set targets are supported only for an inspected dbterm bundle")
	}

	included := make(map[string]ManifestFileSet, len(inspection.FileSets))
	for _, set := range inspection.FileSets {
		included[strings.ToLower(set.Label)] = set
	}
	seenLabels := make(map[string]struct{}, len(options.FileSetTargets))
	seenRoots := make([]string, 0, len(options.FileSetTargets))
	plan := make([]RestoreFileSetPlan, 0, len(options.FileSetTargets))
	normalizedTargets := make([]RestoreFileSetTarget, 0, len(options.FileSetTargets))
	var totalFiles, totalBytes int64
	for _, target := range options.FileSetTargets {
		label := strings.TrimSpace(target.Label)
		key := strings.ToLower(label)
		set, exists := included[key]
		if !exists {
			return nil, options, fmt.Errorf("dbterm bundle does not include file set %q", label)
		}
		if _, duplicate := seenLabels[key]; duplicate {
			return nil, options, fmt.Errorf("file set %q has more than one restore target", label)
		}
		seenLabels[key] = struct{}{}
		root, err := normalizeRestoreFileSetRoot(target.Root)
		if err != nil {
			return nil, options, fmt.Errorf("file set %q target: %w", set.Label, err)
		}
		for _, other := range seenRoots {
			if restoreRootsOverlap(root, other) {
				return nil, options, fmt.Errorf("file-set restore roots must not overlap: %s and %s", root, other)
			}
		}
		seenRoots = append(seenRoots, root)
		if set.FileCount < 0 || set.SizeBytes < 0 || totalFiles > options.MaxFileSetFiles-set.FileCount {
			return nil, options, fmt.Errorf("selected file sets exceed the %d-file restore limit", options.MaxFileSetFiles)
		}
		if totalBytes > options.MaxFileSetBytes-set.SizeBytes {
			return nil, options, fmt.Errorf("selected file sets exceed the %d-byte restore limit", options.MaxFileSetBytes)
		}
		totalFiles += set.FileCount
		totalBytes += set.SizeBytes
		plan = append(plan, RestoreFileSetPlan{Label: set.Label, Root: root, FileCount: set.FileCount, SizeBytes: set.SizeBytes})
		normalizedTargets = append(normalizedTargets, RestoreFileSetTarget{Label: set.Label, Root: root})
	}
	sort.Slice(plan, func(i, j int) bool { return plan[i].Label < plan[j].Label })
	sort.Slice(normalizedTargets, func(i, j int) bool { return normalizedTargets[i].Label < normalizedTargets[j].Label })
	options.FileSetTargets = normalizedTargets
	return plan, options, nil
}

func normalizeRestoreFileSetRoot(value string) (string, error) {
	if strings.TrimSpace(value) == "" || strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("an explicit target root is required")
	}
	absolute, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", fmt.Errorf("resolve target root: %w", err)
	}
	if filepath.Dir(absolute) == absolute {
		return "", fmt.Errorf("filesystem roots are not valid isolated restore targets")
	}
	if err := validateRestoreDirectoryChain(absolute); err != nil {
		return "", err
	}
	return absolute, nil
}

func restoreRootsOverlap(left, right string) bool {
	return sameFileSetPath(left, right) || pathWithin(left, right) || pathWithin(right, left)
}

func restoreFileSetWarnings(plan *RestorePlan) []string {
	if plan == nil || len(plan.IncludedFileSets) == 0 {
		return nil
	}
	if len(plan.FileSetTargets) == 0 {
		return []string{fmt.Sprintf("This bundle includes %d file set(s); the default database-only restore will not extract them. Add explicit isolated targets to restore files.", len(plan.IncludedFileSets))}
	}
	warnings := []string{fmt.Sprintf("%d selected file set(s) will be restored only to the explicit roots shown in this plan; original source roots are not stored in the bundle.", len(plan.FileSetTargets))}
	warnings = append(warnings, "File-set publication is atomic per file, but database and multi-file restore are not one cross-resource transaction.")
	if plan.Options.OverwriteFileSetFiles {
		warnings = append(warnings, "File overwrite is explicitly enabled; existing regular files at selected targets may be atomically replaced.")
	} else {
		warnings = append(warnings, "File overwrite is disabled; any existing destination path stops the restore before the database client starts.")
	}
	return warnings
}

func materializeDBTermBundleRestore(ctx context.Context, bundle *payloadSource, inspection *Inspection, options RestoreOptions, maxDecoded int64) (*payloadSource, error) {
	if bundle == nil || bundle.file == nil {
		return nil, fmt.Errorf("materialized dbterm bundle is required")
	}
	if err := VerifyDBTermBundleEnvelopeContext(ctx, bundle.file, bundle.size); err != nil {
		return nil, fmt.Errorf("verify dbterm bundle for restore: %w", err)
	}
	if _, err := bundle.file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind dbterm bundle for restore: %w", err)
	}
	reader := tar.NewReader(bundle.file)
	header, err := reader.Next()
	if err != nil || header.Name != dbtermBundleManifestPath || header.Typeflag != tar.TypeReg {
		if err == nil {
			err = fmt.Errorf("unexpected first entry %q", header.Name)
		}
		return nil, fmt.Errorf("read dbterm bundle restore manifest: %w", err)
	}
	manifestBytes, err := io.ReadAll(io.LimitReader(&contextReader{ctx: ctx, reader: reader}, maxDBTermBundleManifest+1))
	if err != nil || int64(len(manifestBytes)) != header.Size {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("read dbterm bundle restore manifest: %w", err)
	}
	manifest, err := DecodeDBTermBundleManifest(bytes.NewReader(manifestBytes))
	if err != nil {
		return nil, err
	}
	includedFileSets := make([]ManifestFileSet, 0, len(manifest.FileSets))
	for _, set := range manifest.FileSets {
		includedFileSets = append(includedFileSets, ManifestFileSet{
			Label: set.Label, FileCount: set.FileCount, SizeBytes: set.SizeBytes,
			Consistency: FileSetConsistencyBestEffort, ChangedFiles: []string{}, Warnings: []string{},
		})
	}
	if err := verifyBundleFileSetSummaries(inspection.FileSets, includedFileSets); err != nil {
		return nil, fmt.Errorf("dbterm bundle file-set declaration changed since inspection: %w", err)
	}
	expectedFormat, err := databaseRestoreFormat(inspection)
	if err != nil {
		return nil, err
	}
	if string(expectedFormat) != manifest.Database.Format || !manifestEngineMatchesInspection(manifest.Database.Engine, inspection.Engine) {
		return nil, fmt.Errorf("dbterm bundle database declaration changed since inspection")
	}
	if manifest.Database.SizeBytes > maxDecoded {
		return nil, decodedLimitError(maxDecoded)
	}

	databaseHeader, err := reader.Next()
	if err != nil {
		return nil, fmt.Errorf("read embedded database payload: %w", err)
	}
	if databaseHeader.Name != manifest.Database.Path || databaseHeader.Typeflag != tar.TypeReg || databaseHeader.Size != manifest.Database.SizeBytes {
		return nil, fmt.Errorf("embedded database payload does not match the bundle manifest")
	}
	database, err := materializeVerifiedBundleEntry(ctx, reader, manifest.Database.SizeBytes, manifest.Database.SHA256, "dbterm-restore-database-")
	if err != nil {
		return nil, fmt.Errorf("extract embedded database payload: %w", err)
	}
	fail := func(err error) (*payloadSource, error) {
		database.cleanup()
		return nil, err
	}

	selected := make(map[string]string, len(options.FileSetTargets))
	for _, target := range options.FileSetTargets {
		selected[strings.ToLower(target.Label)] = target.Root
	}
	if len(selected) == 0 {
		return database, nil
	}
	maxFiles := options.MaxFileSetFiles
	if maxFiles == 0 {
		maxFiles = DefaultMaxRestoreFileSetFiles
	}
	maxBytes := options.MaxFileSetBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxRestoreFileSetBytes
	}
	stageRoot, err := privatefile.CreateTempDirectory(os.TempDir(), "dbterm-restore-files-")
	if err != nil {
		return fail(fmt.Errorf("create private file-set restore staging directory: %w", err))
	}
	database.restoreStageRoot = stageRoot
	seenTargets := make(map[string]struct{})
	var fileCount, byteCount int64
	for _, set := range manifest.FileSets {
		targetRoot, restoreSet := selected[strings.ToLower(set.Label)]
		for _, file := range set.Files {
			header, err := reader.Next()
			if err != nil {
				return fail(fmt.Errorf("read bundled file %q: %w", file.Path, err))
			}
			expectedPath := path.Join("dbterm/files", set.Label, file.Path)
			if header.Name != expectedPath || header.Typeflag != tar.TypeReg || header.Size != file.SizeBytes {
				return fail(fmt.Errorf("bundled file %q does not match the bundle manifest", expectedPath))
			}
			if !restoreSet {
				if _, err := io.Copy(io.Discard, &contextReader{ctx: ctx, reader: reader}); err != nil {
					return fail(fmt.Errorf("skip unselected bundled file %q: %w", expectedPath, err))
				}
				continue
			}
			if err := validateRestoreRelativePath(file.Path); err != nil {
				return fail(fmt.Errorf("file set %q path %q: %w", set.Label, file.Path, err))
			}
			if fileCount >= maxFiles || byteCount > maxBytes-file.SizeBytes {
				return fail(fmt.Errorf("selected file sets exceed the configured restore limits"))
			}
			fileCount++
			byteCount += file.SizeBytes
			targetPath, err := restoreTargetPath(targetRoot, file.Path)
			if err != nil {
				return fail(fmt.Errorf("file set %q path %q: %w", set.Label, file.Path, err))
			}
			collisionKey := strings.ToLower(filepath.Clean(targetPath))
			if _, duplicate := seenTargets[collisionKey]; duplicate {
				return fail(fmt.Errorf("selected file sets map more than one bundled file to %s", targetPath))
			}
			seenTargets[collisionKey] = struct{}{}
			staged, err := materializeVerifiedBundleEntry(ctx, reader, file.SizeBytes, file.SHA256, "file-")
			if err != nil {
				return fail(fmt.Errorf("stage file set %q path %q: %w", set.Label, file.Path, err))
			}
			// Move ownership of the temporary path to the bundle staging root.
			stagedPath := staged.path
			_ = staged.file.Close()
			staged.file = nil
			newStagePath := filepath.Join(stageRoot, filepath.Base(stagedPath))
			if err := os.Rename(stagedPath, newStagePath); err != nil {
				staged.cleanup()
				return fail(fmt.Errorf("adopt private file-set stage: %w", err))
			}
			staged.temporary = false
			database.restoreFiles = append(database.restoreFiles, stagedRestoreFile{
				label: set.Label, relativePath: file.Path, stagePath: newStagePath,
				targetRoot: targetRoot, targetPath: targetPath, size: file.SizeBytes, digest: file.SHA256,
			})
		}
	}
	return database, nil
}

func materializeVerifiedBundleEntry(ctx context.Context, reader io.Reader, size int64, expectedDigest, prefix string) (*payloadSource, error) {
	file, err := privatefile.CreateTemp("", prefix, "")
	if err != nil {
		return nil, err
	}
	filePath := file.Name()
	fail := func(err error) (*payloadSource, error) {
		_ = file.Close()
		_ = os.Remove(filePath)
		return nil, err
	}
	hash := sha256.New()
	written, err := io.CopyN(io.MultiWriter(file, hash), &contextReader{ctx: ctx, reader: reader}, size)
	if err != nil || written != size {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return fail(err)
	}
	if digest := hex.EncodeToString(hash.Sum(nil)); !strings.EqualFold(digest, expectedDigest) {
		return fail(fmt.Errorf("size or SHA-256 does not match the bundle manifest"))
	}
	if err := file.Sync(); err != nil {
		return fail(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	return &payloadSource{file: file, path: filePath, size: written, temporary: true}, nil
}

func validateRestoreRelativePath(relative string) error {
	if err := validateBundleRelativePath(relative); err != nil {
		return err
	}
	for _, component := range strings.Split(relative, "/") {
		if strings.Contains(component, ":") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
			return fmt.Errorf("path is not portable to a safe destination filesystem")
		}
		base := strings.ToUpper(strings.SplitN(component, ".", 2)[0])
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" ||
			(len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '1' && base[3] <= '9') {
			return fmt.Errorf("path contains a reserved device name")
		}
	}
	return nil
}

func restoreTargetPath(root, relative string) (string, error) {
	if err := validateRestoreRelativePath(relative); err != nil {
		return "", err
	}
	target := filepath.Join(root, filepath.FromSlash(relative))
	if !pathWithin(root, target) {
		return "", fmt.Errorf("target escapes its explicit restore root")
	}
	return target, nil
}

func validateRestoreDirectoryChain(directory string) error {
	current := filepath.Clean(directory)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || fileSetPathIsReparsePoint(current, info) || !info.IsDir() {
				return fmt.Errorf("restore directory chain contains a link, reparse point, or non-directory: %s", current)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect restore directory %s: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return nil
}

func preflightRestoreFilePublications(files []stagedRestoreFile, overwrite bool, protected ...string) error {
	for _, file := range files {
		if err := validateRestoreDirectoryChain(file.targetRoot); err != nil {
			return fmt.Errorf("file set %q target root: %w", file.label, err)
		}
		if err := validateRestoreDirectoryChain(filepath.Dir(file.targetPath)); err != nil {
			return fmt.Errorf("file set %q target path: %w", file.label, err)
		}
		for _, path := range protected {
			if strings.TrimSpace(path) != "" && sameFileSetPath(filepath.Clean(path), filepath.Clean(file.targetPath)) {
				return fmt.Errorf("file set %q target would replace a backup or database path: %s", file.label, file.targetPath)
			}
		}
		info, err := os.Lstat(file.targetPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect file-set target %s: %w", file.targetPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || fileSetPathIsReparsePoint(file.targetPath, info) || !info.Mode().IsRegular() {
			return fmt.Errorf("file-set target must not be a link, reparse point, or non-regular file: %s", file.targetPath)
		}
		if !overwrite {
			return fmt.Errorf("file-set target already exists and overwrite is disabled: %s", file.targetPath)
		}
	}
	return nil
}

func publishRestoredFileSets(ctx context.Context, files []stagedRestoreFile, overwrite bool, emit func(string)) error {
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := ensureRestoreDirectory(filepath.Dir(file.targetPath)); err != nil {
			return fmt.Errorf("prepare target for file set %q: %w", file.label, err)
		}
		if err := publishRestoredFile(ctx, file, overwrite); err != nil {
			return fmt.Errorf("publish file set %q path %q: %w", file.label, file.relativePath, err)
		}
		emitRestore(emit, fmt.Sprintf("Restored file set %s: %s", file.label, file.relativePath))
	}
	return nil
}

func ensureRestoreDirectory(directory string) error {
	var missing []string
	current := filepath.Clean(directory)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || fileSetPathIsReparsePoint(current, info) || !info.IsDir() {
				return fmt.Errorf("restore directory is not a safe real directory: %s", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("no existing parent for restore directory %s", directory)
		}
		current = parent
	}
	for index := len(missing) - 1; index >= 0; index-- {
		if err := os.Mkdir(missing[index], 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		info, err := os.Lstat(missing[index])
		if err != nil || info.Mode()&os.ModeSymlink != 0 || fileSetPathIsReparsePoint(missing[index], info) || !info.IsDir() {
			return fmt.Errorf("created restore directory is unsafe: %s", missing[index])
		}
	}
	return validateRestoreDirectoryChain(directory)
}

func publishRestoredFile(ctx context.Context, file stagedRestoreFile, overwrite bool) error {
	if err := validateRestoreDirectoryChain(filepath.Dir(file.targetPath)); err != nil {
		return err
	}
	partial, err := privatefile.CreateTemp(filepath.Dir(file.targetPath), ".dbterm-restore-", ".partial")
	if err != nil {
		return err
	}
	partialPath := partial.Name()
	closed := false
	defer func() {
		if !closed {
			_ = partial.Close()
		}
		_ = os.Remove(partialPath)
	}()
	digest, err := copyAndHashStableFile(ctx, file.stagePath, file.size, partial)
	if err != nil {
		return err
	}
	if !strings.EqualFold(digest, file.digest) {
		return fmt.Errorf("private staged file no longer matches the bundle manifest")
	}
	if err := partial.Sync(); err != nil {
		return err
	}
	if err := partial.Close(); err != nil {
		return err
	}
	closed = true
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRestoreDirectoryChain(filepath.Dir(file.targetPath)); err != nil {
		return err
	}
	existing, statErr := os.Lstat(file.targetPath)
	if statErr == nil {
		if existing.Mode()&os.ModeSymlink != 0 || fileSetPathIsReparsePoint(file.targetPath, existing) || !existing.Mode().IsRegular() {
			return fmt.Errorf("target became a link, reparse point, or non-regular file")
		}
		if !overwrite {
			return fmt.Errorf("target already exists and overwrite is disabled")
		}
		current, err := os.Lstat(file.targetPath)
		if err != nil || !os.SameFile(existing, current) || existing.Size() != current.Size() || existing.ModTime() != current.ModTime() {
			return fmt.Errorf("target changed before atomic replacement")
		}
		if err := replaceSQLiteStagedFile(partialPath, file.targetPath); err != nil {
			return err
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		if err := atomicPublishNoReplace(partialPath, file.targetPath); err != nil {
			return fmt.Errorf("atomic no-overwrite publication refused the target: %w", err)
		}
	} else {
		return statErr
	}
	if err := syncDirectory(filepath.Dir(file.targetPath)); err != nil {
		return err
	}
	return nil
}
