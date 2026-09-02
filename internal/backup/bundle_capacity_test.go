package backup

import (
	"bytes"
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundlePipelineRequirementsAccountForSharedAndSeparateFilesystems(t *testing.T) {
	const rawBytes = uint64(4096)
	const selectedBytes = uint64(8192)
	const selectedFiles = uint64(2)
	separateStage, separateArtifact, err := bundlePipelineRequirements(rawBytes, selectedBytes, selectedBytes, selectedFiles, false)
	if err != nil {
		t.Fatal(err)
	}
	sharedStage, sharedArtifact, err := bundlePipelineRequirements(rawBytes, selectedBytes, selectedBytes, selectedFiles, true)
	if err != nil {
		t.Fatal(err)
	}
	if separateStage == 0 || separateArtifact == 0 {
		t.Fatalf("separate requirements = stage %d, artifact %d", separateStage, separateArtifact)
	}
	if sharedArtifact != 0 || sharedStage <= separateStage {
		t.Fatalf("shared requirements = stage %d, artifact %d; separate stage = %d", sharedStage, sharedArtifact, separateStage)
	}
	artifactUpper, err := wrappedArtifactUpperBound(mustBundleUpperBound(t, rawBytes, selectedBytes, selectedFiles))
	if err != nil {
		t.Fatal(err)
	}
	if sharedStage-separateStage != artifactUpper {
		t.Fatalf("shared filesystem added %d artifact bytes, want %d", sharedStage-separateStage, artifactUpper)
	}
}

func TestBundleCapacityArithmeticFailsClosedOnOverflow(t *testing.T) {
	if _, _, err := bundlePipelineRequirements(math.MaxUint64, 1, 1, 1, true); err == nil || !strings.Contains(err.Error(), "supported capacity range") {
		t.Fatalf("pipeline overflow error = %v", err)
	}
	if _, err := checkedCapacityMultiply("test multiplication", math.MaxUint64, 2); err == nil {
		t.Fatal("overflowing capacity multiplication succeeded")
	}
}

func TestBundleCapacityGuardChargesSharedFilesystemOnceAsOneCombinedPeak(t *testing.T) {
	stage := filepath.Join(t.TempDir(), "stage")
	destination := filepath.Join(t.TempDir(), "destination")
	required, _, err := bundlePipelineRequirements(1024, 2048, 2048, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	probe := func(path string) (DiskUsage, error) {
		available := required
		if sameFileSetPath(filepath.Clean(path), filepath.Clean(destination)) {
			available--
		}
		return DiskUsage{Path: path, Volume: "shared-volume", AvailableBytes: available}, nil
	}
	guard, err := newBundleCapacityGuard(stage, destination, 1024, probe)
	if err != nil {
		t.Fatal(err)
	}
	if !guard.sharedFilesystem {
		t.Fatal("same volume was not recognized as one filesystem")
	}
	err = guard.ensurePipeline(2048, 2048, 1)
	if err == nil || !isInsufficientBundleCapacity(err) {
		t.Fatalf("combined shared-filesystem capacity error = %v", err)
	}
}

func TestBundleCapacityGuardChecksSeparateArtifactFilesystem(t *testing.T) {
	stage := filepath.Join(t.TempDir(), "stage")
	destination := filepath.Join(t.TempDir(), "destination")
	stageRequired, artifactRequired, err := bundlePipelineRequirements(1024, 2048, 2048, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	probe := func(path string) (DiskUsage, error) {
		if sameFileSetPath(filepath.Clean(path), filepath.Clean(stage)) {
			return DiskUsage{Path: path, Volume: "stage-volume", AvailableBytes: stageRequired}, nil
		}
		return DiskUsage{Path: path, Volume: "artifact-volume", AvailableBytes: artifactRequired - 1}, nil
	}
	guard, err := newBundleCapacityGuard(stage, destination, 1024, probe)
	if err != nil {
		t.Fatal(err)
	}
	if guard.sharedFilesystem {
		t.Fatal("different volumes were treated as one filesystem")
	}
	err = guard.ensurePipeline(2048, 2048, 1)
	if err == nil || !isInsufficientBundleCapacity(err) || !strings.Contains(err.Error(), "local artifact filesystem") {
		t.Fatalf("separate artifact-filesystem capacity error = %v", err)
	}
}

func TestBundleCapacityGuardRejectsFilesystemIdentityChange(t *testing.T) {
	stage := filepath.Join(t.TempDir(), "stage")
	calls := 0
	probe := func(path string) (DiskUsage, error) {
		calls++
		volume := "original-volume"
		if calls > 1 {
			volume = "replacement-volume"
		}
		return DiskUsage{Path: path, Volume: volume, AvailableBytes: math.MaxUint64}, nil
	}
	guard, err := newBundleCapacityGuard(stage, stage, 1, probe)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.ensurePipeline(0, 0, 0); err == nil || !strings.Contains(err.Error(), "filesystem changed") {
		t.Fatalf("filesystem identity-change error = %v", err)
	}
}

func TestFileSetCapacityOmitsOptionalButKeepsRequired(t *testing.T) {
	stage := t.TempDir()
	requiredRoot := t.TempDir()
	optionalRoot := t.TempDir()
	writeFileSetTestFile(t, requiredRoot, "required.bin", "1234")
	writeFileSetTestFile(t, optionalRoot, "optional.bin", "5678")
	requiredBytes, _, err := bundlePipelineRequirements(4, 4, 4, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	probe := fixedBundleUsageProbe("one-volume", requiredBytes)
	guard, err := newBundleCapacityGuard(stage, stage, 4, probe)
	if err != nil {
		t.Fatal(err)
	}
	prepared, summaries, warnings, err := prepareJobFileSetsWithCapacity(context.Background(), []FileSet{
		{Label: "optional", Root: optionalRoot, Include: []string{"**"}},
		{Label: "required", Root: requiredRoot, Include: []string{"**"}, Required: true},
	}, stage, guard)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 1 || prepared[0].config.Label != "required" {
		t.Fatalf("prepared file sets = %+v", prepared)
	}
	if len(summaries) != 2 || summaries[0].Consistency != FileSetConsistencyOmitted || summaries[1].Consistency != FileSetConsistencyBestEffort {
		t.Fatalf("summaries = %+v", summaries)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "capacity") {
		t.Fatalf("capacity warnings = %v", warnings)
	}
}

func TestFileSetCapacityRequiredShortageFailsBeforeCopy(t *testing.T) {
	stage := t.TempDir()
	root := t.TempDir()
	writeFileSetTestFile(t, root, "required.bin", "1234")
	requiredBytes, _, err := bundlePipelineRequirements(4, 4, 4, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := newBundleCapacityGuard(stage, stage, 4, fixedBundleUsageProbe("one-volume", requiredBytes-1))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = prepareJobFileSetsWithCapacity(context.Background(), []FileSet{{
		Label: "required", Root: root, Include: []string{"**"}, Required: true,
	}}, stage, guard)
	if err == nil || !isInsufficientBundleCapacity(err) {
		t.Fatalf("required capacity error = %v", err)
	}
	entries, readErr := filepath.Glob(filepath.Join(stage, ".dbterm-fileset-*"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("required preflight created file-set stages: %v", entries)
	}
}

func TestFileSetCapacityRechecksBeforeCopyAndOmitsOptionalOnShrink(t *testing.T) {
	stage := t.TempDir()
	root := t.TempDir()
	writeFileSetTestFile(t, root, "optional.bin", "1234")
	baseBytes, _, err := bundlePipelineRequirements(4, 0, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	optionalBytes, _, err := bundlePipelineRequirements(4, 4, 4, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	probe := func(path string) (DiskUsage, error) {
		calls++
		available := optionalBytes
		if calls >= 4 {
			available = baseBytes
		}
		return DiskUsage{Path: path, Volume: "one-volume", AvailableBytes: available}, nil
	}
	guard, err := newBundleCapacityGuard(stage, stage, 4, probe)
	if err != nil {
		t.Fatal(err)
	}
	prepared, summaries, warnings, err := prepareJobFileSetsWithCapacity(context.Background(), []FileSet{{
		Label: "optional", Root: root, Include: []string{"**"}, Required: false,
	}}, stage, guard)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared) != 0 || len(summaries) != 1 || summaries[0].Consistency != FileSetConsistencyOmitted || len(warnings) != 1 || !strings.Contains(warnings[0], "capacity") {
		t.Fatalf("dynamic optional result: prepared=%+v summaries=%+v warnings=%v", prepared, summaries, warnings)
	}
}

func TestCopyExpectedFileBytesDetectsGrowthWithoutWritingIt(t *testing.T) {
	var destination bytes.Buffer
	written, err := copyExpectedFileBytes(context.Background(), &destination, strings.NewReader("abcdef"), 3)
	if err == nil || !strings.Contains(err.Error(), "grew") {
		t.Fatalf("growth error = %v", err)
	}
	if written != 3 || destination.String() != "abc" {
		t.Fatalf("bounded copy wrote %d bytes %q", written, destination.String())
	}
}

func fixedBundleUsageProbe(volume string, available uint64) diskUsageProbe {
	return func(path string) (DiskUsage, error) {
		return DiskUsage{Path: path, Volume: volume, AvailableBytes: available}, nil
	}
}

func mustBundleUpperBound(t *testing.T, rawBytes, selectedBytes, selectedFiles uint64) uint64 {
	t.Helper()
	value, err := bundleTarUpperBound(rawBytes, selectedBytes, selectedFiles)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
