package backup

import (
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
	"unicode"
)

const (
	maxFileSets          = 64
	maxFileSetPatterns   = 256
	maxFileSetFiles      = 100_000
	maxFileSetPathBytes  = 4096
	maxFileSetLabelBytes = 64
)

// FileSet describes application files that must travel with a database
// recovery point. Root is machine-local configuration and is never written to
// a portable artifact manifest. Include and Exclude use slash-separated globs;
// ** is supported only as a complete path segment.
type FileSet struct {
	Label    string   `json:"label"`
	Root     string   `json:"root"`
	Include  []string `json:"include,omitempty"`
	Exclude  []string `json:"exclude,omitempty"`
	Required bool     `json:"required"`
}

type preparedFileSet struct {
	config  FileSet
	summary ManifestFileSet
	files   []preparedFileSetFile
	root    string
}

type preparedFileSetFile struct {
	relativePath string
	stagedPath   string
	size         int64
	sha256       string
}

type fileSetCandidate struct {
	relativePath string
	sourcePath   string
	info         os.FileInfo
}

type selectedFileSet struct {
	config     FileSet
	sourceRoot string
	candidates []fileSetCandidate
	sizeBytes  uint64
}

type pendingFileSet struct {
	config    FileSet
	selection *selectedFileSet
	prepared  *preparedFileSet
	warning   string
}

func normalizeJobFileSets(fileSets []FileSet) error {
	if len(fileSets) > maxFileSets {
		return fmt.Errorf("backup job supports at most %d file sets", maxFileSets)
	}
	for index := range fileSets {
		set := &fileSets[index]
		set.Label = strings.TrimSpace(set.Label)
		set.Root = strings.TrimSpace(set.Root)
		if set.Root != "" {
			absolute, err := filepath.Abs(filepath.Clean(set.Root))
			if err != nil {
				return fmt.Errorf("file set %q root: %w", set.Label, err)
			}
			set.Root = absolute
		}
		if len(set.Include) == 0 {
			set.Include = []string{"**"}
		}
		for patternIndex := range set.Include {
			set.Include[patternIndex] = strings.TrimSpace(set.Include[patternIndex])
		}
		for patternIndex := range set.Exclude {
			set.Exclude[patternIndex] = strings.TrimSpace(set.Exclude[patternIndex])
		}
	}
	return validateJobFileSets(fileSets)
}

func validateJobFileSets(fileSets []FileSet) error {
	if len(fileSets) > maxFileSets {
		return fmt.Errorf("backup job supports at most %d file sets", maxFileSets)
	}
	labels := make(map[string]struct{}, len(fileSets))
	for index, set := range fileSets {
		if err := validateFileSet(set); err != nil {
			return fmt.Errorf("file set %d: %w", index+1, err)
		}
		folded := strings.ToLower(set.Label)
		if _, duplicate := labels[folded]; duplicate {
			return fmt.Errorf("file set label %q is duplicated", set.Label)
		}
		labels[folded] = struct{}{}
	}
	return nil
}

func validateFileSet(set FileSet) error {
	if !validFileSetLabel(set.Label) {
		return fmt.Errorf("label %q must be 1-%d bytes of letters, digits, dot, dash, or underscore and start with a letter or digit", set.Label, maxFileSetLabelBytes)
	}
	if strings.TrimSpace(set.Root) == "" {
		return fmt.Errorf("%q root is required", set.Label)
	}
	if !filepath.IsAbs(set.Root) || filepath.Clean(set.Root) != set.Root {
		return fmt.Errorf("%q root must be an absolute normalized path", set.Label)
	}
	if len(set.Include) == 0 {
		return fmt.Errorf("%q requires at least one include pattern", set.Label)
	}
	if len(set.Include)+len(set.Exclude) > maxFileSetPatterns {
		return fmt.Errorf("%q supports at most %d include/exclude patterns", set.Label, maxFileSetPatterns)
	}
	seen := make(map[string]string, len(set.Include)+len(set.Exclude))
	for _, group := range []struct {
		kind     string
		patterns []string
	}{{kind: "include", patterns: set.Include}, {kind: "exclude", patterns: set.Exclude}} {
		kind, patterns := group.kind, group.patterns
		for _, pattern := range patterns {
			if err := validateFileSetPattern(pattern); err != nil {
				return fmt.Errorf("%q %s pattern %q: %w", set.Label, kind, pattern, err)
			}
			key := kind + "\x00" + strings.ToLower(pattern)
			if previous, duplicate := seen[key]; duplicate {
				return fmt.Errorf("%q %s pattern %q duplicates %q", set.Label, kind, pattern, previous)
			}
			seen[key] = pattern
		}
	}
	return nil
}

func validFileSetLabel(label string) bool {
	if label == "" || len([]byte(label)) > maxFileSetLabelBytes || label == "." || label == ".." {
		return false
	}
	for index, character := range label {
		if character > unicode.MaxASCII || unicode.IsControl(character) {
			return false
		}
		valid := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if index > 0 {
			valid = valid || character == '.' || character == '-' || character == '_'
		}
		if !valid {
			return false
		}
	}
	return true
}

func validateFileSetPattern(pattern string) error {
	if pattern == "" || len([]byte(pattern)) > maxFileSetPathBytes {
		return fmt.Errorf("pattern must contain between 1 and %d bytes", maxFileSetPathBytes)
	}
	if strings.Contains(pattern, "\\") || strings.HasPrefix(pattern, "/") || strings.HasSuffix(pattern, "/") || strings.ContainsAny(pattern, "\x00\r\n") {
		return fmt.Errorf("pattern must be a relative slash-separated portable path")
	}
	for _, segment := range strings.Split(pattern, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("pattern contains an empty, current, or parent path segment")
		}
		if strings.Contains(segment, "**") && segment != "**" {
			return fmt.Errorf("double-star must be a complete path segment")
		}
		if segment != "**" {
			if _, err := path.Match(segment, "probe"); err != nil {
				return fmt.Errorf("invalid glob: %w", err)
			}
		}
	}
	return nil
}

func fileSetPathMatches(relative string, includes, excludes []string) bool {
	included := false
	for _, pattern := range includes {
		if matchFileSetGlob(pattern, relative) {
			included = true
			break
		}
	}
	if !included {
		return false
	}
	for _, pattern := range excludes {
		if matchFileSetGlob(pattern, relative) {
			return false
		}
	}
	return true
}

func matchFileSetGlob(pattern, relative string) bool {
	patterns := strings.Split(pattern, "/")
	names := strings.Split(relative, "/")
	type state struct{ pattern, name int }
	memo := make(map[state]bool)
	seen := make(map[state]bool)
	var match func(int, int) bool
	match = func(patternIndex, nameIndex int) bool {
		key := state{pattern: patternIndex, name: nameIndex}
		if seen[key] {
			return memo[key]
		}
		seen[key] = true
		var matched bool
		switch {
		case patternIndex == len(patterns):
			matched = nameIndex == len(names)
		case patterns[patternIndex] == "**":
			matched = match(patternIndex+1, nameIndex) || nameIndex < len(names) && match(patternIndex, nameIndex+1)
		case nameIndex < len(names):
			segmentMatched, err := path.Match(patterns[patternIndex], names[nameIndex])
			matched = err == nil && segmentMatched && match(patternIndex+1, nameIndex+1)
		}
		memo[key] = matched
		return matched
	}
	return match(0, 0)
}

func prepareJobFileSets(ctx context.Context, fileSets []FileSet, privateStage string) ([]preparedFileSet, []ManifestFileSet, []string, error) {
	return prepareJobFileSetsWithCapacity(ctx, fileSets, privateStage, nil)
}

func prepareJobFileSetsWithCapacity(ctx context.Context, fileSets []FileSet, privateStage string, capacity *bundleCapacityGuard) ([]preparedFileSet, []ManifestFileSet, []string, error) {
	pending := make([]pendingFileSet, len(fileSets))
	var requiredBytes uint64
	requiredFiles := 0
	for index, set := range fileSets {
		pending[index].config = set
		selection, err := selectOneFileSet(ctx, set)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, nil, nil, ctxErr
			}
			if set.Required {
				return nil, nil, nil, fmt.Errorf("required file set %q: %w", set.Label, err)
			}
			pending[index].warning = optionalFileSetWarning(set.Label, false)
			continue
		}
		pending[index].selection = &selection
		if set.Required {
			if requiredFiles > maxFileSetFiles-len(selection.candidates) {
				return nil, nil, nil, fmt.Errorf("required file set %q: configured required file sets exceed the combined %d-file limit", set.Label, maxFileSetFiles)
			}
			requiredFiles += len(selection.candidates)
			var addErr error
			requiredBytes, addErr = checkedCapacityAdd("required file-set size", requiredBytes, selection.sizeBytes)
			if addErr != nil {
				return nil, nil, nil, fmt.Errorf("required file set %q: %w", set.Label, addErr)
			}
		}
	}

	selectedBytes := requiredBytes
	selectedFiles := requiredFiles
	if capacity != nil {
		// Establish that the database-only/required-set recovery point fits
		// before optional data is allowed to consume private staging space.
		if err := capacity.ensurePipeline(requiredBytes, requiredBytes, uint64(requiredFiles)); err != nil {
			return nil, nil, nil, fmt.Errorf("required dbterm bundle capacity: %w", err)
		}
	}
	remainingRequiredBytes := requiredBytes
	for index := range pending {
		item := &pending[index]
		if item.selection == nil || !item.config.Required {
			continue
		}
		beforeCopy := func(size int64) error {
			if size < 0 || uint64(size) > remainingRequiredBytes {
				return fmt.Errorf("selected file size changed outside the supported capacity plan")
			}
			if capacity != nil {
				if err := capacity.ensurePipeline(remainingRequiredBytes, selectedBytes, uint64(selectedFiles)); err != nil {
					return &fileSetCapacityCheckError{err: err}
				}
			}
			return nil
		}
		afterCopy := func(size int64) {
			copied := uint64(size)
			remainingRequiredBytes -= copied
		}
		staged, err := stageSelectedFileSet(ctx, *item.selection, privateStage, beforeCopy, afterCopy)
		if err == nil {
			item.prepared = &staged
			continue
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, nil, nil, ctxErr
		}
		var capacityErr *fileSetCapacityCheckError
		if errors.As(err, &capacityErr) && !isInsufficientBundleCapacity(capacityErr.err) {
			return nil, nil, nil, fmt.Errorf("file-set capacity changed during capture: %w", capacityErr.err)
		}
		return nil, nil, nil, fmt.Errorf("required file set %q: %w", item.config.Label, err)
	}

	// Required sets are fully and durably staged before optional sets are
	// admitted. Optional data can therefore never displace a required recovery
	// component merely because it appeared earlier in configuration order.
	for index := range pending {
		item := &pending[index]
		if item.selection == nil || item.config.Required {
			continue
		}
		if selectedFiles > maxFileSetFiles-len(item.selection.candidates) {
			item.warning = optionalFileSetWarning(item.config.Label, false)
			item.selection = nil
			continue
		}
		prospectiveBytes, err := checkedCapacityAdd("configured file-set size", selectedBytes, item.selection.sizeBytes)
		if err != nil {
			item.warning = optionalFileSetWarning(item.config.Label, true)
			item.selection = nil
			continue
		}
		prospectiveFiles := selectedFiles + len(item.selection.candidates)
		if capacity != nil {
			err = capacity.ensurePipeline(item.selection.sizeBytes, prospectiveBytes, uint64(prospectiveFiles))
			if err != nil {
				if isInsufficientBundleCapacity(err) {
					item.warning = optionalFileSetWarning(item.config.Label, true)
					item.selection = nil
					continue
				}
				return nil, nil, nil, fmt.Errorf("check optional file-set capacity: %w", err)
			}
		}
		remainingOptionalBytes := item.selection.sizeBytes
		beforeCopy := func(size int64) error {
			if size < 0 || uint64(size) > remainingOptionalBytes {
				return fmt.Errorf("selected file size changed outside the supported capacity plan")
			}
			if capacity != nil {
				if err := capacity.ensurePipeline(remainingOptionalBytes, prospectiveBytes, uint64(prospectiveFiles)); err != nil {
					return &fileSetCapacityCheckError{err: err}
				}
			}
			return nil
		}
		afterCopy := func(size int64) { remainingOptionalBytes -= uint64(size) }
		staged, err := stageSelectedFileSet(ctx, *item.selection, privateStage, beforeCopy, afterCopy)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, nil, nil, ctxErr
			}
			var capacityErr *fileSetCapacityCheckError
			if errors.As(err, &capacityErr) && !isInsufficientBundleCapacity(capacityErr.err) {
				return nil, nil, nil, fmt.Errorf("file-set capacity changed during capture: %w", capacityErr.err)
			}
			item.selection = nil
			item.warning = optionalFileSetWarning(item.config.Label, errors.As(err, &capacityErr))
			continue
		}
		item.prepared = &staged
		selectedBytes = prospectiveBytes
		selectedFiles = prospectiveFiles
	}
	if capacity != nil {
		if err := capacity.ensurePipeline(0, selectedBytes, uint64(selectedFiles)); err != nil {
			return nil, nil, nil, fmt.Errorf("recheck dbterm bundle capacity before archive creation: %w", err)
		}
	}

	prepared := make([]preparedFileSet, 0, len(fileSets))
	summaries := make([]ManifestFileSet, 0, len(fileSets))
	warnings := make([]string, 0)
	for index := range pending {
		item := &pending[index]
		if item.prepared != nil {
			prepared = append(prepared, *item.prepared)
			summaries = append(summaries, item.prepared.summary)
			continue
		}
		warning := item.warning
		if warning == "" {
			warning = optionalFileSetWarning(item.config.Label, false)
		}
		warnings = append(warnings, warning)
		summaries = append(summaries, ManifestFileSet{
			Label: item.config.Label, FileCount: 0, SizeBytes: 0, Consistency: FileSetConsistencyOmitted,
			ChangedFiles: []string{}, Warnings: []string{warning},
		})
	}
	return prepared, summaries, warnings, nil
}

type fileSetCapacityCheckError struct{ err error }

func (err *fileSetCapacityCheckError) Error() string { return err.err.Error() }
func (err *fileSetCapacityCheckError) Unwrap() error { return err.err }

func optionalFileSetWarning(label string, capacity bool) string {
	if capacity {
		return fmt.Sprintf("optional file set %q omitted because conservative private staging capacity was insufficient", label)
	}
	return fmt.Sprintf("optional file set %q omitted because its root or selected files were unavailable, unsafe, changed, or empty", label)
}

func selectOneFileSet(ctx context.Context, set FileSet) (selectedFileSet, error) {
	root, err := resolveExactFileSetRoot(set.Root)
	if err != nil {
		return selectedFileSet{}, err
	}
	candidates, err := scanFileSetCandidates(ctx, root, set)
	if err != nil {
		return selectedFileSet{}, err
	}
	if len(candidates) == 0 {
		return selectedFileSet{}, fmt.Errorf("no regular files matched the include/exclude policy")
	}
	var sizeBytes uint64
	for _, candidate := range candidates {
		if candidate.info == nil || candidate.info.Size() < 0 {
			return selectedFileSet{}, fmt.Errorf("selected file reported an invalid size")
		}
		if uint64(candidate.info.Size()) > uint64(^uint64(0)>>1)-sizeBytes {
			return selectedFileSet{}, fmt.Errorf("file-set size exceeds supported range")
		}
		sizeBytes, err = checkedCapacityAdd("file-set selected size", sizeBytes, uint64(candidate.info.Size()))
		if err != nil {
			return selectedFileSet{}, err
		}
	}
	return selectedFileSet{config: set, sourceRoot: root, candidates: candidates, sizeBytes: sizeBytes}, nil
}

func prepareOneFileSet(ctx context.Context, set FileSet, privateStage string) (preparedFileSet, error) {
	selection, err := selectOneFileSet(ctx, set)
	if err != nil {
		return preparedFileSet{}, err
	}
	return stageSelectedFileSet(ctx, selection, privateStage, nil, nil)
}

func stageSelectedFileSet(ctx context.Context, selection selectedFileSet, privateStage string, beforeCopy func(int64) error, afterCopy func(int64)) (preparedFileSet, error) {
	setStage, err := os.MkdirTemp(privateStage, ".dbterm-fileset-")
	if err != nil {
		return preparedFileSet{}, fmt.Errorf("create private file-set stage: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(setStage)
		}
	}()
	result := preparedFileSet{
		config: selection.config, root: setStage, files: make([]preparedFileSetFile, 0, len(selection.candidates)),
		summary: ManifestFileSet{Label: selection.config.Label, Consistency: FileSetConsistencyBestEffort, ChangedFiles: []string{}, Warnings: []string{}},
	}
	for _, candidate := range selection.candidates {
		if err := ctx.Err(); err != nil {
			return preparedFileSet{}, err
		}
		if beforeCopy != nil {
			if err := beforeCopy(candidate.info.Size()); err != nil {
				return preparedFileSet{}, fmt.Errorf("check capacity before snapshotting %q: %w", candidate.relativePath, err)
			}
		}
		staged, err := stageFileSetCandidate(ctx, setStage, candidate)
		if err != nil {
			return preparedFileSet{}, err
		}
		if afterCopy != nil {
			afterCopy(staged.size)
		}
		result.files = append(result.files, staged)
		result.summary.FileCount++
		if staged.size > 0 && result.summary.SizeBytes > int64(^uint64(0)>>1)-staged.size {
			return preparedFileSet{}, fmt.Errorf("file-set size exceeds supported range")
		}
		result.summary.SizeBytes += staged.size
	}
	after, err := scanFileSetCandidates(ctx, selection.sourceRoot, selection.config)
	if err != nil || !sameFileSetCandidateSnapshot(selection.candidates, after) {
		if err != nil {
			return preparedFileSet{}, fmt.Errorf("recheck application file set after capture: %w", err)
		}
		return preparedFileSet{}, fmt.Errorf("application file-set membership or metadata changed during capture")
	}
	keep = true
	return result, nil
}

func sameFileSetCandidateSnapshot(before, after []fileSetCandidate) bool {
	if len(before) != len(after) {
		return false
	}
	for index := range before {
		if before[index].relativePath != after[index].relativePath || !sameFileSetInfo(before[index].info, after[index].info) || !os.SameFile(before[index].info, after[index].info) {
			return false
		}
	}
	return true
}

func resolveExactFileSetRoot(configured string) (string, error) {
	absolute, err := filepath.Abs(filepath.Clean(configured))
	if err != nil {
		return "", fmt.Errorf("resolve root: %w", err)
	}
	if absolute != configured {
		return "", fmt.Errorf("root must remain the exact normalized absolute path %q", absolute)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("inspect root %q: %w", absolute, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || fileSetPathIsReparsePoint(absolute, info) {
		return "", fmt.Errorf("root must not be a symbolic link or reparse point: %s", absolute)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("root must be a directory: %s", absolute)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve root links: %w", err)
	}
	if !sameFileSetPath(absolute, resolved) {
		return "", fmt.Errorf("root resolves through a symbolic link or reparse point (%s -> %s)", absolute, resolved)
	}
	return absolute, nil
}

func scanFileSetCandidates(ctx context.Context, root string, set FileSet) ([]fileSetCandidate, error) {
	candidates := make([]fileSetCandidate, 0)
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, current)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
			return fmt.Errorf("path escaped configured root: %s", current)
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || fileSetPathIsReparsePoint(current, info) {
			return fmt.Errorf("symbolic link or reparse point is not allowed: %s", current)
		}
		if current == root || info.IsDir() {
			return nil
		}
		portable := filepath.ToSlash(relative)
		if err := validateBundleRelativePath(portable); err != nil {
			return fmt.Errorf("unsafe path %q: %w", portable, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("nonregular application file is not allowed: %s", current)
		}
		if !fileSetPathMatches(portable, set.Include, set.Exclude) {
			return nil
		}
		if len(candidates) >= maxFileSetFiles {
			return fmt.Errorf("file set exceeds the %d-file limit", maxFileSetFiles)
		}
		candidates = append(candidates, fileSetCandidate{relativePath: portable, sourcePath: current, info: info})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].relativePath < candidates[j].relativePath })
	return candidates, nil
}

func stageFileSetCandidate(ctx context.Context, setStage string, candidate fileSetCandidate) (preparedFileSetFile, error) {
	resolved, err := filepath.EvalSymlinks(candidate.sourcePath)
	if err != nil || !sameFileSetPath(candidate.sourcePath, resolved) {
		return preparedFileSetFile{}, fmt.Errorf("application file resolves through a symbolic link or reparse point: %s", candidate.sourcePath)
	}
	before, err := os.Lstat(candidate.sourcePath)
	if err != nil || !sameFileSetInfo(candidate.info, before) || before.Mode()&os.ModeSymlink != 0 || fileSetPathIsReparsePoint(candidate.sourcePath, before) {
		return preparedFileSetFile{}, fmt.Errorf("application file changed after scan: %s", candidate.sourcePath)
	}
	source, err := os.Open(candidate.sourcePath)
	if err != nil {
		return preparedFileSetFile{}, fmt.Errorf("open application file %s: %w", candidate.sourcePath, err)
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !sameFileSetInfo(before, opened) || !os.SameFile(before, opened) {
		return preparedFileSetFile{}, fmt.Errorf("application file changed while opening: %s", candidate.sourcePath)
	}
	destination := filepath.Join(setStage, filepath.FromSlash(candidate.relativePath))
	if !pathWithin(setStage, destination) {
		return preparedFileSetFile{}, fmt.Errorf("staged application path escaped private root: %s", candidate.relativePath)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return preparedFileSetFile{}, fmt.Errorf("create private application staging directory: %w", err)
	}
	target, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return preparedFileSetFile{}, fmt.Errorf("create private staged application file: %w", err)
	}
	targetClosed := false
	defer func() {
		if !targetClosed {
			_ = target.Close()
		}
	}()
	hash := sha256.New()
	written, copyErr := copyExpectedFileBytes(ctx, io.MultiWriter(target, hash), source, before.Size())
	if copyErr == nil && written != before.Size() {
		copyErr = fmt.Errorf("copied %d bytes; expected %d", written, before.Size())
	}
	if copyErr == nil {
		copyErr = verifyFileSetSourceUnchanged(candidate.sourcePath, source, before)
	}
	if copyErr == nil {
		copyErr = target.Sync()
	}
	closeErr := target.Close()
	targetClosed = true
	if copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		return preparedFileSetFile{}, fmt.Errorf("snapshot application file %s: %w", candidate.sourcePath, copyErr)
	}
	return preparedFileSetFile{relativePath: candidate.relativePath, stagedPath: destination, size: written, sha256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func verifyFileSetSourceUnchanged(sourcePath string, file *os.File, expected os.FileInfo) error {
	after, handleErr := file.Stat()
	current, pathErr := os.Lstat(sourcePath)
	resolved, resolveErr := filepath.EvalSymlinks(sourcePath)
	if handleErr != nil || pathErr != nil || !sameFileSetInfo(expected, after) || !sameFileSetInfo(expected, current) ||
		!os.SameFile(expected, after) || !os.SameFile(expected, current) || current.Mode()&os.ModeSymlink != 0 || fileSetPathIsReparsePoint(sourcePath, current) {
		return fmt.Errorf("application file changed while it was being copied: %s", sourcePath)
	}
	if resolveErr != nil || !sameFileSetPath(sourcePath, resolved) {
		return fmt.Errorf("application file escaped through a symbolic link or reparse point: %s", sourcePath)
	}
	return nil
}

func sameFileSetInfo(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.Mode() == right.Mode() && left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func validateBundleRelativePath(relative string) error {
	if relative == "" || relative == "." || len([]byte(relative)) > maxFileSetPathBytes || strings.HasPrefix(relative, "/") || strings.Contains(relative, "\\") || strings.ContainsAny(relative, "\x00\r\n") {
		return fmt.Errorf("path must be a bounded portable relative path")
	}
	cleaned := path.Clean(relative)
	if cleaned != relative || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("path contains traversal or non-canonical segments")
	}
	return nil
}

func copyAndHashStableFile(ctx context.Context, sourcePath string, expectedSize int64, destination io.Writer) (string, error) {
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || fileSetPathIsReparsePoint(sourcePath, info) || info.Size() != expectedSize {
		return "", fmt.Errorf("staged file changed or is not regular: %s", sourcePath)
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || !sameFileSetInfo(info, opened) {
		return "", fmt.Errorf("staged file changed while opening: %s", sourcePath)
	}
	hash := sha256.New()
	written, err := copyExpectedFileBytes(ctx, io.MultiWriter(destination, hash), file, expectedSize)
	if err != nil {
		return "", err
	}
	if written != expectedSize {
		return "", fmt.Errorf("staged file size changed: %s", sourcePath)
	}
	if err := verifyFileSetSourceUnchanged(sourcePath, file, info); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// copyExpectedFileBytes never writes beyond the size captured during the
// metadata scan. It still reads one additional byte to detect a growing file,
// then the caller re-stats the open handle and path to catch later races.
func copyExpectedFileBytes(ctx context.Context, destination io.Writer, source io.Reader, expectedSize int64) (int64, error) {
	if expectedSize < 0 {
		return 0, fmt.Errorf("expected file size must not be negative")
	}
	reader := &contextReader{ctx: ctx, reader: source}
	written, err := io.CopyBuffer(destination, io.LimitReader(reader, expectedSize), make([]byte, 256*1024))
	if err != nil {
		return written, err
	}
	if written != expectedSize {
		return written, fmt.Errorf("file was truncated while being copied: copied %d bytes; expected %d", written, expectedSize)
	}
	var extra [1]byte
	count, readErr := io.ReadFull(reader, extra[:])
	if count != 0 || readErr == nil {
		return written, fmt.Errorf("file grew beyond the %d-byte scanned size while being copied", expectedSize)
	}
	if !errors.Is(readErr, io.EOF) {
		return written, fmt.Errorf("check file length after bounded copy: %w", readErr)
	}
	return written, nil
}
