package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shreyam1008/dbterm/internal/config"
)

func TestRunnerBuildsInspectableDBTermBundle(t *testing.T) {
	isolateBackupState(t)
	destination := t.TempDir()
	source := createRunnerSQLiteFixture(t, t.TempDir(), "source.sqlite3")
	applicationRoot := t.TempDir()
	writeFileSetTestFile(t, applicationRoot, "config.json", `{"enabled":true}`)
	writeFileSetTestFile(t, applicationRoot, "uploads/avatar.txt", "avatar")
	writeFileSetTestFile(t, applicationRoot, "uploads/cache.tmp", "ignored")
	job := runnerSQLiteJob(destination, "complete_{run}", "job_bundle")
	job.FileSets = []FileSet{{
		Label: "application", Root: applicationRoot,
		Include: []string{"config.json", "uploads/**"}, Exclude: []string{"**/*.tmp"}, Required: true,
	}}
	if err := job.ApplyDefaults(testNow()); err != nil {
		t.Fatal(err)
	}
	cfg := &config.ConnectionConfig{ID: job.ConnectionID, Name: "bundle source", Type: config.SQLite, FilePath: source}
	artifact, err := (Runner{Now: testNow}).Run(context.Background(), job, cfg, "run_bundle_1234567890")
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Format != string(FormatDBTermBundle) || !strings.HasSuffix(artifact.Path, ".dbterm") {
		t.Fatalf("bundle artifact = %+v", artifact)
	}
	manifest, err := ReadArtifactManifest(artifact.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Format != string(FormatDBTermBundle) || len(manifest.FileSets) != 1 || manifest.FileSets[0].FileCount != 2 || manifest.FileSets[0].Consistency != FileSetConsistencyBestEffort {
		t.Fatalf("portable bundle manifest = %+v", manifest)
	}
	inspection, err := Inspect(context.Background(), artifact.Path, InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Format != FormatDBTermBundle || inspection.DatabaseFormat != FormatSQLiteDatabase || inspection.Engine != config.SQLite || len(inspection.FileSets) != 1 || inspection.FileSets[0].FileCount != 2 {
		t.Fatalf("bundle inspection = %+v", inspection)
	}
	file, err := os.Open(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyDBTermBundleEnvelope(file, info.Size()); err != nil {
		t.Fatalf("verify bundle envelope: %v", err)
	}
	entries, internal := readBundleTestEntries(t, artifact.Path)
	wantEntries := strings.Join([]string{
		dbtermBundleManifestPath,
		"dbterm/database/native.sqlite3",
		"dbterm/files/application/config.json",
		"dbterm/files/application/uploads/avatar.txt",
	}, ",")
	if strings.Join(entries, ",") != wantEntries {
		t.Fatalf("bundle entries = %v, want %s", entries, wantEntries)
	}
	if strings.Contains(string(internal), applicationRoot) {
		t.Fatalf("internal bundle manifest leaked configured absolute root: %s", internal)
	}
}

func TestDBTermBundleBytesAreDeterministic(t *testing.T) {
	native := createRunnerSQLiteFixture(t, t.TempDir(), "native.sqlite3")
	root := t.TempDir()
	writeFileSetTestFile(t, root, "z.txt", "last")
	writeFileSetTestFile(t, root, "a.txt", "first")
	set := FileSet{Label: "data", Root: root, Include: []string{"**"}, Required: true}
	cfg := &config.ConnectionConfig{Name: "db", Type: config.SQLite, FilePath: native}
	plan, err := PlanFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	first, err := buildDBTermBundle(context.Background(), t.TempDir(), native, cfg, plan, []FileSet{set})
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildDBTermBundle(context.Background(), t.TempDir(), native, cfg, plan, []FileSet{set})
	if err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(first.path)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		firstHash, secondHash := sha256.Sum256(firstBytes), sha256.Sum256(secondBytes)
		t.Fatalf("bundle bytes are not deterministic: %s != %s", hex.EncodeToString(firstHash[:]), hex.EncodeToString(secondHash[:]))
	}
}

func TestRunnerDBTermBundleUsesExistingCompressionPipeline(t *testing.T) {
	isolateBackupState(t)
	source := createRunnerSQLiteFixture(t, t.TempDir(), "source.sqlite3")
	root := t.TempDir()
	writeFileSetTestFile(t, root, "settings.ini", "enabled=true\n")
	for _, compression := range []Compression{CompressionGzip, CompressionZip, CompressionZstd} {
		t.Run(string(compression), func(t *testing.T) {
			destination := t.TempDir()
			job := runnerSQLiteJob(destination, "bundle_{run}", "job_bundle_"+string(compression))
			job.Compression = compression
			job.FileSets = []FileSet{{Label: "config", Root: root, Include: []string{"**"}, Required: true}}
			if err := job.ApplyDefaults(testNow()); err != nil {
				t.Fatal(err)
			}
			cfg := &config.ConnectionConfig{ID: job.ConnectionID, Name: "source", Type: config.SQLite, FilePath: source}
			artifact, err := (Runner{Now: testNow}).Run(context.Background(), job, cfg, "run_compressed")
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := Inspect(context.Background(), artifact.Path, InspectOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if inspection.Format != FormatDBTermBundle || inspection.DatabaseFormat != FormatSQLiteDatabase || len(inspection.Wrappers) != 1 {
				t.Fatalf("compressed bundle inspection = %+v", inspection)
			}
		})
	}
}

func TestRunnerOptionalFileSetWarningAndRequiredFailure(t *testing.T) {
	isolateBackupState(t)
	source := createRunnerSQLiteFixture(t, t.TempDir(), "source.sqlite3")
	cfg := &config.ConnectionConfig{ID: "conn_bundle_optional", Name: "source", Type: config.SQLite, FilePath: source}
	missing := filepath.Join(t.TempDir(), "missing")

	optionalDestination := t.TempDir()
	optional := runnerSQLiteJob(optionalDestination, "optional_{run}", "job_bundle_optional")
	optional.ConnectionID = cfg.ID
	optional.FileSets = []FileSet{{Label: "uploads", Root: missing, Include: []string{"**"}}}
	if err := optional.ApplyDefaults(testNow()); err != nil {
		t.Fatal(err)
	}
	artifact, err := (Runner{Now: testNow}).Run(context.Background(), optional, cfg, "run_optional")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadArtifactManifest(artifact.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Warnings) != 1 || len(manifest.FileSets) != 1 || manifest.FileSets[0].Consistency != "omitted" {
		t.Fatalf("optional file-set manifest = %+v", manifest)
	}
	inspection, err := Inspect(context.Background(), artifact.Path, InspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.FileSets) != 0 || len(inspection.Warnings) == 0 {
		t.Fatalf("optional bundle inspection = %+v", inspection)
	}

	requiredDestination := t.TempDir()
	required := runnerSQLiteJob(requiredDestination, "required_{run}", "job_bundle_required")
	required.ConnectionID = cfg.ID
	required.FileSets = []FileSet{{Label: "uploads", Root: missing, Include: []string{"**"}, Required: true}}
	if err := required.ApplyDefaults(testNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := (Runner{Now: testNow}).Run(context.Background(), required, cfg, "run_required"); err == nil || !strings.Contains(err.Error(), "required file set") {
		t.Fatalf("required file-set error = %v", err)
	}
	entries, err := os.ReadDir(requiredDestination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("required file-set failure published destination entries: %v", entries)
	}
}

func TestRunnerWithoutFileSetsKeepsNativeFormatAndFilename(t *testing.T) {
	isolateBackupState(t)
	destination := t.TempDir()
	source := createRunnerSQLiteFixture(t, t.TempDir(), "source.sqlite3")
	job := runnerSQLiteJob(destination, "native_{run}", "job_native_unchanged")
	cfg := &config.ConnectionConfig{ID: job.ConnectionID, Name: "source", Type: config.SQLite, FilePath: source}
	artifact, err := (Runner{Now: testNow}).Run(context.Background(), job, cfg, "run_native")
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Format != string(FormatSQLiteDatabase) || !strings.HasSuffix(artifact.Path, ".sqlite3") {
		t.Fatalf("native-only artifact changed shape: %+v", artifact)
	}
}

func TestInspectRejectsBundleEntryChecksumMismatch(t *testing.T) {
	native := createRunnerSQLiteFixture(t, t.TempDir(), "native.sqlite3")
	root := t.TempDir()
	writeFileSetTestFile(t, root, "file.txt", "original")
	set := FileSet{Label: "data", Root: root, Include: []string{"**"}, Required: true}
	cfg := &config.ConnectionConfig{Name: "db", Type: config.SQLite, FilePath: native}
	plan, err := PlanFor(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := buildDBTermBundle(context.Background(), t.TempDir(), native, cfg, plan, []FileSet{set})
	if err != nil {
		t.Fatal(err)
	}
	tampered := filepath.Join(t.TempDir(), "tampered.dbterm")
	tamperBundleEntry(t, bundle.path, tampered, "dbterm/files/data/file.txt")
	if _, err := Inspect(context.Background(), tampered, InspectOptions{}); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("tampered bundle inspection error = %v", err)
	}
}

func readBundleTestEntries(t *testing.T, bundlePath string) ([]string, []byte) {
	t.Helper()
	file, err := os.Open(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	var entries []string
	var internal []byte
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, header.Name)
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == dbtermBundleManifestPath {
			internal = data
		}
	}
	return entries, internal
}

func tamperBundleEntry(t *testing.T, source, destination, target string) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	writer := tar.NewWriter(output)
	reader := tar.NewReader(input)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		if header.Name == target && len(data) > 0 {
			data[0] ^= 0xff
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}
