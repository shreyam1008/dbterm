package backup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/shreyam1008/dbterm/internal/config"
)

func TestArtifactManifestStrictRoundTrip(t *testing.T) {
	manifest := validTestArtifactManifest()
	var encoded bytes.Buffer
	if err := EncodeArtifactManifest(&encoded, manifest); err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeArtifactManifest(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ArtifactID != manifest.ArtifactID || decoded.RunID != manifest.RunID || decoded.SHA256 != manifest.SHA256 {
		t.Fatalf("decoded manifest = %#v, want identity fields from %#v", decoded, manifest)
	}
	if decoded.Encryption != EncryptionSchemeAgeX25519V1 || !decoded.Encrypted {
		t.Fatalf("decoded encryption = %q/%t, want age X25519 v1", decoded.Encryption, decoded.Encrypted)
	}
	if decoded.FileSets == nil || decoded.Warnings == nil {
		t.Fatalf("portable arrays decoded as nil: %#v", decoded)
	}
}

func TestArtifactManifestRejectsUnknownVersionFieldsAndTrailingJSON(t *testing.T) {
	manifest := validTestArtifactManifest()
	var encoded bytes.Buffer
	if err := EncodeArtifactManifest(&encoded, manifest); err != nil {
		t.Fatal(err)
	}

	var object map[string]any
	if err := json.Unmarshal(encoded.Bytes(), &object); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "trailing value", data: append(append([]byte{}, encoded.Bytes()...), []byte(`{"second":true}`)...), want: "more than one JSON value"},
		{
			name: "oversized trailing whitespace",
			data: append(append([]byte{}, encoded.Bytes()...), bytes.Repeat([]byte(" "), maxArtifactManifestBytes)...),
			want: "exceeds",
		},
		{
			name: "duplicate identity field",
			data: bytes.Replace(encoded.Bytes(), []byte(`"artifact_id": "artifact_test"`), []byte(`"artifact_id": "artifact_first", "artifact_id": "artifact_test"`), 1),
			want: "duplicate field",
		},
		{
			name: "non-canonical field casing",
			data: bytes.Replace(encoded.Bytes(), []byte(`"sha256"`), []byte(`"SHA256"`), 1),
			want: "non-canonical",
		},
	}
	object["schema_version"] = 2
	unsupported, _ := json.Marshal(object)
	tests = append(tests, struct {
		name string
		data []byte
		want string
	}{name: "future schema", data: unsupported, want: "unsupported"})
	object["schema_version"] = float64(ArtifactManifestSchemaVersion)
	object["unexpected"] = true
	unknown, _ := json.Marshal(object)
	tests = append(tests, struct {
		name string
		data []byte
		want string
	}{name: "unknown field", data: unknown, want: "unknown field"})
	delete(object, "unexpected")
	delete(object, "warnings")
	missing, _ := json.Marshal(object)
	tests = append(tests, struct {
		name string
		data []byte
		want string
	}{name: "missing required field", data: missing, want: "required fields are missing"})
	object["warnings"] = nil
	nullArray, _ := json.Marshal(object)
	tests = append(tests, struct {
		name string
		data []byte
		want string
	}{name: "null required array", data: nullArray, want: "must be an array"})
	object["warnings"] = []any{}
	object["file_sets"] = []any{map[string]any{
		"label": "photos", "file_count": 1, "size_bytes": 10,
		"consistency": "best-effort", "changed_files": []any{},
	}}
	missingNested, _ := json.Marshal(object)
	tests = append(tests, struct {
		name string
		data []byte
		want string
	}{name: "missing nested required field", data: missingNested, want: "required fields are missing"})

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeArtifactManifest(bytes.NewReader(test.data))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeArtifactManifest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestArtifactManifestRejectsUnknownEngineAndImpossibleFormatPair(t *testing.T) {
	for _, test := range []struct {
		name   string
		engine config.DBType
		format string
		want   string
	}{
		{name: "unknown engine", engine: config.DBType("oracle"), format: "oracle_dump", want: "unsupported database engine"},
		{name: "wrong native format", engine: config.MySQL, format: string(FormatSQLiteDatabase), want: "invalid for database engine"},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := validTestArtifactManifest()
			manifest.Engine = test.engine
			manifest.Format = test.format
			var encoded bytes.Buffer
			if err := EncodeArtifactManifest(&encoded, manifest); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("EncodeArtifactManifest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestBuildArtifactManifestDoesNotExposeAgeRecipientOrSecrets(t *testing.T) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	recipient := identity.Recipient().String()
	job := Job{
		ID: "job_orders", Name: "orders", ConnectionID: "conn_secret", Destination: t.TempDir(),
		Compression: CompressionZstd, Encryption: EncryptionAge, AgeRecipient: recipient,
		Schedule: Schedule{Kind: ScheduleManual}, Retention: Retention{KeepLast: 1}, TimeoutMinutes: 5,
	}
	manifest, err := buildArtifactManifest(job, &config.ConnectionConfig{
		Type: config.MySQL, Host: "secret.internal", User: "backup-user", Password: "password-value", Database: "orders",
	}, "run_orders", "artifact_orders", "producer_test", "1.2.3", time.Now().UTC(), "mysql_sql", 42, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := EncodeArtifactManifest(&encoded, manifest); err != nil {
		t.Fatal(err)
	}
	text := encoded.String()
	for _, secret := range []string{recipient, identity.String(), "secret.internal", "backup-user", "password-value", "conn_secret"} {
		if strings.Contains(text, secret) {
			t.Errorf("portable manifest exposes %q: %s", secret, text)
		}
	}
	if strings.Contains(text, "recipient_key_ids") {
		t.Fatalf("portable manifest exposes recipient-derived identifiers: %s", text)
	}
}

func TestResolveProducerIDIsStableAcrossConcurrentReaders(t *testing.T) {
	t.Setenv("DBTERM_STATE_DIR", t.TempDir())
	const readers = 12
	values := make(chan string, readers)
	errors := make(chan error, readers)
	var group sync.WaitGroup
	for index := 0; index < readers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			value, err := resolveProducerID("")
			if err != nil {
				errors <- err
				return
			}
			values <- value
		}()
	}
	group.Wait()
	close(values)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	want := ""
	for value := range values {
		if want == "" {
			want = value
		}
		if value != want {
			t.Fatalf("producer IDs differ: %q and %q", want, value)
		}
	}
	if !strings.HasPrefix(want, "producer_") {
		t.Fatalf("producer ID = %q", want)
	}
}

func TestReadArtifactManifestRejectsSymlink(t *testing.T) {
	directory := t.TempDir()
	realPath := filepath.Join(directory, "real.json")
	linkedPath := filepath.Join(directory, "linked.json")
	var encoded bytes.Buffer
	if err := EncodeArtifactManifest(&encoded, validTestArtifactManifest()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(realPath, encoded.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realPath, linkedPath); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	if _, err := ReadArtifactManifest(linkedPath); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("ReadArtifactManifest() error = %v, want symlink refusal", err)
	}
}

func TestInspectionManifestRejectsImpossibleWrapperOrder(t *testing.T) {
	manifest := validTestArtifactManifest()
	for _, test := range []struct {
		name       string
		wrappers   []Wrapper
		confidence string
	}{
		{name: "compression outside locked age", wrappers: []Wrapper{WrapperZstd, WrapperAge}, confidence: ConfidenceLocked},
		{name: "compression before unlocked age", wrappers: []Wrapper{WrapperZstd, WrapperAge}, confidence: ConfidenceExact},
		{name: "duplicate compression", wrappers: []Wrapper{WrapperAge, WrapperZstd, WrapperZstd}, confidence: ConfidenceExact},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspection := &Inspection{
				Manifest: &manifest, Wrappers: test.wrappers, Confidence: test.confidence,
				Format: FormatMySQLSQL, Engine: config.MySQL,
			}
			if err := verifyInspectionManifestDescription(inspection); err == nil {
				t.Fatalf("wrapper stack %#v was accepted", test.wrappers)
			}
		})
	}
}

func validTestArtifactManifest() ArtifactManifest {
	return ArtifactManifest{
		SchemaVersion: ArtifactManifestSchemaVersion,
		ArtifactID:    "artifact_test", RunID: "run_test", JobID: "job_test",
		CreatedAt:  time.Date(2026, 9, 3, 1, 4, 9, 0, time.FixedZone("IST", 5*60*60+30*60)),
		ProducerID: "producer_test", DBTermVersion: "1.2.3", Engine: config.MySQL,
		Format: "mysql_sql", Compression: CompressionZstd,
		Encryption: EncryptionSchemeAgeX25519V1, Encrypted: true,
		SizeBytes: 123, SHA256: strings.Repeat("a", 64), Verification: ArtifactVerificationPassed, VerificationLevel: ArtifactVerificationBasic,
		FileSets: []ManifestFileSet{}, Warnings: []string{},
	}
}
