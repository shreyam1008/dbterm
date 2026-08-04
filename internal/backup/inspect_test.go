package backup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/armor"
	"github.com/klauspost/compress/zstd"
	"github.com/shreyam1008/dbterm/config"
)

func TestInspectDetectsSupportedPayloads(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		filename        string
		data            []byte
		wantFormat      Format
		wantEngine      config.DBType
		wantConfidence  string
		wantTools       []string
		warningContains string
	}{
		{
			name:            "PostgreSQL custom archive ignores SQL extension",
			filename:        "misleading.sql",
			data:            append([]byte("PGDMP"), 1, 15, 0, 4),
			wantFormat:      FormatPostgresCustom,
			wantEngine:      config.PostgreSQL,
			wantConfidence:  ConfidenceExact,
			wantTools:       []string{"pg_restore"},
			warningContains: "does not match",
		},
		{
			name:           "PostgreSQL tar archive",
			filename:       "backup.tar",
			data:           postgresTarFixture(t),
			wantFormat:     FormatPostgresTar,
			wantEngine:     config.PostgreSQL,
			wantConfidence: ConfidenceExact,
			wantTools:      []string{"pg_restore"},
		},
		{
			name:           "SQLite database",
			filename:       "backup.sqlite3",
			data:           sqliteFixture(512),
			wantFormat:     FormatSQLiteDatabase,
			wantEngine:     config.SQLite,
			wantConfidence: ConfidenceExact,
		},
		{
			name:     "PostgreSQL plain SQL",
			filename: "backup.sql",
			data: []byte("\ufeff--\r\n-- PostgreSQL database dump\r\n--\r\n\r\n" +
				"SET statement_timeout = 0;\r\nSET client_encoding = 'UTF8';\r\n"),
			wantFormat:     FormatPostgresSQL,
			wantEngine:     config.PostgreSQL,
			wantConfidence: ConfidenceStrong,
			wantTools:      []string{"psql"},
		},
		{
			name:           "MySQL plain SQL",
			filename:       "backup.sql",
			data:           []byte("-- MySQL dump 10.13  Distrib 8.4.0\n--\nCREATE TABLE `users` (`id` bigint);\n"),
			wantFormat:     FormatMySQLSQL,
			wantEngine:     config.MySQL,
			wantConfidence: ConfidenceStrong,
			wantTools:      []string{"mysql"},
		},
		{
			name:           "MariaDB sandbox header",
			filename:       "backup.sql",
			data:           []byte("/*M!999999\\- enable the sandbox mode */ \n-- MariaDB dump 10.19\n"),
			wantFormat:     FormatMySQLSQL,
			wantEngine:     config.MySQL,
			wantConfidence: ConfidenceStrong,
			wantTools:      []string{"mysql"},
		},
		{
			name:           "SQLite plain SQL",
			filename:       "backup.sql",
			data:           []byte("PRAGMA foreign_keys=OFF;\nBEGIN TRANSACTION;\nCREATE TABLE items(id INTEGER);\nCOMMIT;\n"),
			wantFormat:     FormatSQLiteSQL,
			wantEngine:     config.SQLite,
			wantConfidence: ConfidenceStrong,
			wantTools:      []string{"sqlite3"},
		},
		{
			name:            "generic SQL is deliberately ambiguous",
			filename:        "backup.sql",
			data:            []byte("CREATE TABLE widgets (id INTEGER PRIMARY KEY);\nINSERT INTO widgets VALUES (1);\n"),
			wantFormat:      FormatGenericSQL,
			wantConfidence:  ConfidenceAmbiguous,
			warningContains: "restore is blocked",
		},
		{
			name:            "conflicting dialect markers stay ambiguous",
			filename:        "backup.sql",
			data:            []byte("SET client_encoding = 'UTF8';\n/*!40101 SET NAMES utf8mb4 */;\nCREATE TABLE x(id int);\n"),
			wantFormat:      FormatGenericSQL,
			wantConfidence:  ConfidenceAmbiguous,
			warningContains: "conflicting",
		},
		{
			name:           "unknown binary",
			filename:       "backup.dump",
			data:           []byte{0xde, 0xad, 0xbe, 0xef, 0x00, 0x7f},
			wantFormat:     FormatUnknown,
			wantConfidence: ConfidenceUnknown,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := writeInspectionFixture(t, test.filename, test.data)
			got, err := Inspect(context.Background(), path, InspectOptions{})
			if err != nil {
				t.Fatalf("Inspect() error = %v", err)
			}
			if got.Format != test.wantFormat {
				t.Errorf("Format = %q, want %q (evidence: %v; warnings: %v)", got.Format, test.wantFormat, got.Evidence, got.Warnings)
			}
			if got.Engine != test.wantEngine {
				t.Errorf("Engine = %q, want %q", got.Engine, test.wantEngine)
			}
			if got.Confidence != test.wantConfidence {
				t.Errorf("Confidence = %q, want %q", got.Confidence, test.wantConfidence)
			}
			if !reflect.DeepEqual(got.RequiredTools, test.wantTools) {
				t.Errorf("RequiredTools = %v, want %v", got.RequiredTools, test.wantTools)
			}
			if test.warningContains != "" && !containsString(got.Warnings, test.warningContains) {
				t.Errorf("Warnings = %v, want a warning containing %q", got.Warnings, test.warningContains)
			}
		})
	}
}

func TestInspectValidatesSQLiteHeader(t *testing.T) {
	t.Parallel()

	invalid := make([]byte, 512)
	copy(invalid, "SQLite format 3\x00")
	binary.BigEndian.PutUint16(invalid[16:18], 513)
	invalid[18], invalid[19] = 1, 1
	invalid[21], invalid[22], invalid[23] = 64, 32, 32

	result, err := Inspect(context.Background(), writeInspectionFixture(t, "broken.sqlite", invalid), InspectOptions{})
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if result.Format != FormatUnknown || result.Engine != "" {
		t.Fatalf("invalid SQLite header detected as %q/%q", result.Format, result.Engine)
	}
	if !containsString(result.Warnings, "header is invalid") {
		t.Fatalf("Warnings = %v, want invalid-header warning", result.Warnings)
	}
}

func TestInspectHashesOuterArtifactAndUnwrapsInOrder(t *testing.T) {
	t.Parallel()

	payload := append([]byte("PGDMP"), 1, 15, 0, 4)
	inner := zstdFixture(t, payload)
	outer := gzipFixture(t, inner)
	path := writeInspectionFixture(t, "backup.dump.zst.gz", outer)

	result, err := Inspect(context.Background(), path, InspectOptions{MaxDecodedBytes: math.MaxInt64})
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	wantWrappers := []Wrapper{WrapperGzip, WrapperZstd}
	if !reflect.DeepEqual(result.Wrappers, wantWrappers) {
		t.Errorf("Wrappers = %v, want %v", result.Wrappers, wantWrappers)
	}
	if result.Format != FormatPostgresCustom {
		t.Errorf("Format = %q, want %q", result.Format, FormatPostgresCustom)
	}
	digest := sha256.Sum256(outer)
	if result.SHA256 != hex.EncodeToString(digest[:]) {
		t.Errorf("SHA256 = %q, want outer artifact digest %q", result.SHA256, hex.EncodeToString(digest[:]))
	}
	if result.Size != int64(len(outer)) {
		t.Errorf("Size = %d, want outer artifact size %d", result.Size, len(outer))
	}
	if result.Path != path {
		t.Errorf("Path = %q, want resolved path %q", result.Path, path)
	}
}

func TestInspectWarnsForArbitraryExtensionAndMissingWrapperSuffix(t *testing.T) {
	t.Parallel()

	payload := append([]byte("PGDMP"), 1, 15, 0, 4)
	result, err := Inspect(context.Background(), writeInspectionFixture(t, "backup.txt", gzipFixture(t, payload)), InspectOptions{})
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if !containsString(result.Warnings, "Detected gzip wrapper") {
		t.Errorf("Warnings = %v, want missing gzip suffix warning", result.Warnings)
	}
	if !containsString(result.Warnings, `extension ".txt" does not match`) {
		t.Errorf("Warnings = %v, want content extension mismatch warning", result.Warnings)
	}
}

func TestInspectAgeLockedAndUnlocked(t *testing.T) {
	t.Parallel()

	payload := append([]byte("PGDMP"), 1, 15, 0, 4)
	compressed := gzipFixture(t, payload)
	ciphertext, identity := ageFixture(t, compressed, false)
	path := writeInspectionFixture(t, "backup.dump.gz.age", ciphertext)

	t.Run("locked without identity is a successful inspection", func(t *testing.T) {
		result, err := Inspect(context.Background(), path, InspectOptions{})
		if err != nil {
			t.Fatalf("Inspect() error = %v", err)
		}
		if !result.Locked || result.Confidence != ConfidenceLocked {
			t.Fatalf("Locked/Confidence = %v/%q, want true/%q", result.Locked, result.Confidence, ConfidenceLocked)
		}
		if result.Format != FormatUnknown || result.Engine != "" {
			t.Errorf("locked Format/Engine = %q/%q, want unknown/empty", result.Format, result.Engine)
		}
		if !reflect.DeepEqual(result.Wrappers, []Wrapper{WrapperAge}) {
			t.Errorf("Wrappers = %v, want [age]", result.Wrappers)
		}
		if !containsString(result.Warnings, "identity file") {
			t.Errorf("Warnings = %v, want identity-file guidance", result.Warnings)
		}
	})

	t.Run("identity decrypts and continues recursive inspection", func(t *testing.T) {
		identityPath := writeInspectionFixture(t, " identity with spaces ", []byte(identity.String()+"\n"))
		result, err := Inspect(context.Background(), path, InspectOptions{AgeIdentityPath: identityPath})
		if err != nil {
			t.Fatalf("Inspect() error = %v", err)
		}
		if result.Locked {
			t.Fatal("Locked = true after successful decryption")
		}
		if !reflect.DeepEqual(result.Wrappers, []Wrapper{WrapperAge, WrapperGzip}) {
			t.Errorf("Wrappers = %v, want [age gzip]", result.Wrappers)
		}
		if result.Format != FormatPostgresCustom || result.Engine != config.PostgreSQL {
			t.Errorf("Format/Engine = %q/%q, want PostgreSQL custom", result.Format, result.Engine)
		}
	})

	t.Run("armored age is recognized", func(t *testing.T) {
		armored, armoredIdentity := ageFixture(t, payload, true)
		armoredPath := writeInspectionFixture(t, "backup.dump.age", armored)
		identityPath := writeInspectionFixture(t, "identity.txt", []byte(armoredIdentity.String()+"\n"))
		result, err := Inspect(context.Background(), armoredPath, InspectOptions{AgeIdentityPath: identityPath})
		if err != nil {
			t.Fatalf("Inspect() error = %v", err)
		}
		if result.Format != FormatPostgresCustom || !reflect.DeepEqual(result.Wrappers, []Wrapper{WrapperAge}) {
			t.Errorf("Format/Wrappers = %q/%v, want postgres_custom/[age]", result.Format, result.Wrappers)
		}
	})
}

func TestInspectRejectsMalformedOrUnsafeWrappers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filename string
		data     func(*testing.T) []byte
		opts     InspectOptions
		wantErr  string
	}{
		{
			name:     "invalid gzip",
			filename: "backup.gz",
			data:     func(*testing.T) []byte { return []byte{0x1f, 0x8b, 0x08, 0x00} },
			wantErr:  "invalid gzip backup",
		},
		{
			name:     "invalid zstd",
			filename: "backup.zst",
			data:     func(*testing.T) []byte { return []byte{0x28, 0xb5, 0x2f, 0xfd, 0x00} },
			wantErr:  "decode zstd backup",
		},
		{
			name:     "ZIP with two entries",
			filename: "backup.zip",
			data: func(t *testing.T) []byte {
				return zipFixture(t, []zipEntry{{name: "one.sql", data: []byte("SELECT 1;")}, {name: "two.sql", data: []byte("SELECT 2;")}})
			},
			wantErr: "exactly one regular file",
		},
		{
			name:     "ZIP symlink entry",
			filename: "backup.zip",
			data: func(t *testing.T) []byte {
				return zipFixture(t, []zipEntry{{name: "dump.sql", data: []byte("elsewhere.sql"), mode: os.ModeSymlink | 0o777}})
			},
			wantErr: "exactly one regular file",
		},
		{
			name:     "empty decoded payload",
			filename: "backup.gz",
			data:     func(t *testing.T) []byte { return gzipFixture(t, nil) },
			wantErr:  "decoded backup payload is empty",
		},
		{
			name:     "decoded size limit",
			filename: "backup.gz",
			data:     func(t *testing.T) []byte { return gzipFixture(t, bytes.Repeat([]byte("x"), 65)) },
			opts:     InspectOptions{MaxDecodedBytes: 64},
			wantErr:  "configured limit of 64 bytes",
		},
		{
			name:     "wrapper nesting depth",
			filename: "backup.dump.gz.gz.gz.gz",
			data: func(t *testing.T) []byte {
				data := append([]byte("PGDMP"), 1, 15, 0, 4)
				for range 4 {
					data = gzipFixture(t, data)
				}
				return data
			},
			wantErr: "maximum depth of 3",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := writeInspectionFixture(t, test.filename, test.data(t))
			_, err := Inspect(context.Background(), path, test.opts)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("Inspect() error = %v, want error containing %q", err, test.wantErr)
			}
		})
	}
}

func TestInspectRejectsWrongAgeIdentity(t *testing.T) {
	t.Parallel()

	ciphertext, _ := ageFixture(t, []byte("SELECT 1;"), false)
	wrongIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity() error = %v", err)
	}
	identityPath := writeInspectionFixture(t, "wrong-identity.txt", []byte(wrongIdentity.String()+"\n"))
	path := writeInspectionFixture(t, "backup.sql.age", ciphertext)
	_, err = Inspect(context.Background(), path, InspectOptions{AgeIdentityPath: identityPath})
	if err == nil || !strings.Contains(err.Error(), "decrypt age backup") {
		t.Fatalf("Inspect() error = %v, want age decryption error", err)
	}
}

func TestInspectSourceValidationAndCancellation(t *testing.T) {
	t.Parallel()

	t.Run("path is required", func(t *testing.T) {
		_, err := Inspect(context.Background(), " \t ", InspectOptions{})
		if err == nil || !strings.Contains(err.Error(), "path is required") {
			t.Fatalf("Inspect() error = %v", err)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		_, err := Inspect(context.Background(), filepath.Join(t.TempDir(), "missing.dump"), InspectOptions{})
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("Inspect() error = %v", err)
		}
	})

	t.Run("directory is not a regular file", func(t *testing.T) {
		_, err := Inspect(context.Background(), t.TempDir(), InspectOptions{})
		if err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("Inspect() error = %v", err)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		path := writeInspectionFixture(t, "empty.dump", nil)
		_, err := Inspect(context.Background(), path, InspectOptions{})
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("Inspect() error = %v", err)
		}
	})

	t.Run("negative decoded limit", func(t *testing.T) {
		path := writeInspectionFixture(t, "backup.sql", []byte("SELECT 1;"))
		_, err := Inspect(context.Background(), path, InspectOptions{MaxDecodedBytes: -1})
		if err == nil || !strings.Contains(err.Error(), "cannot be negative") {
			t.Fatalf("Inspect() error = %v", err)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		path := writeInspectionFixture(t, "backup.sql", []byte("SELECT 1;"))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Inspect(ctx, path, InspectOptions{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Inspect() error = %v, want context.Canceled", err)
		}
	})

	t.Run("spaces in a real filename are preserved", func(t *testing.T) {
		path := writeInspectionFixture(t, " backup.dump ", append([]byte("PGDMP"), 1, 15, 0, 4))
		result, err := Inspect(context.Background(), path, InspectOptions{})
		if err != nil {
			t.Fatalf("Inspect() error = %v", err)
		}
		if result.Path != path || result.Format != FormatPostgresCustom {
			t.Fatalf("Path/Format = %q/%q, want %q/%q", result.Path, result.Format, path, FormatPostgresCustom)
		}
	})
}

func TestInspectThreeWrappersIsAllowed(t *testing.T) {
	t.Parallel()

	data := append([]byte("PGDMP"), 1, 15, 0, 4)
	for range 3 {
		data = gzipFixture(t, data)
	}
	result, err := Inspect(context.Background(), writeInspectionFixture(t, "backup.dump.gz.gz.gz", data), InspectOptions{})
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if result.Format != FormatPostgresCustom || !reflect.DeepEqual(result.Wrappers, []Wrapper{WrapperGzip, WrapperGzip, WrapperGzip}) {
		t.Fatalf("Format/Wrappers = %q/%v", result.Format, result.Wrappers)
	}
}

func writeInspectionFixture(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture %q: %v", path, err)
	}
	return path
}

func sqliteFixture(pageSize int) []byte {
	data := make([]byte, pageSize)
	copy(data, "SQLite format 3\x00")
	if pageSize == 65536 {
		binary.BigEndian.PutUint16(data[16:18], 1)
	} else {
		binary.BigEndian.PutUint16(data[16:18], uint16(pageSize))
	}
	data[18], data[19] = 1, 1
	data[21], data[22], data[23] = 64, 32, 32
	binary.BigEndian.PutUint32(data[28:32], 1)
	binary.BigEndian.PutUint32(data[44:48], 4)
	binary.BigEndian.PutUint32(data[56:60], 1)
	return data
}

func postgresTarFixture(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	payload := append([]byte("PGDMP"), 1, 15, 0, 4)
	if err := writer.WriteHeader(&tar.Header{Name: "toc.dat", Mode: 0o600, Size: int64(len(payload))}); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write tar payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar fixture: %v", err)
	}
	return output.Bytes()
}

func gzipFixture(t *testing.T, payload []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := gzip.NewWriter(&output)
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write gzip fixture: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close gzip fixture: %v", err)
	}
	return output.Bytes()
}

func zstdFixture(t *testing.T, payload []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer, err := zstd.NewWriter(&output)
	if err != nil {
		t.Fatalf("create zstd fixture: %v", err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatalf("write zstd fixture: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zstd fixture: %v", err)
	}
	return output.Bytes()
}

type zipEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func zipFixture(t *testing.T, entries []zipEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		mode := entry.mode
		if mode == 0 {
			mode = 0o600
		}
		header.SetMode(mode)
		entryWriter, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create ZIP fixture entry: %v", err)
		}
		if _, err := entryWriter.Write(entry.data); err != nil {
			t.Fatalf("write ZIP fixture entry: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close ZIP fixture: %v", err)
	}
	return output.Bytes()
}

func ageFixture(t *testing.T, payload []byte, armored bool) ([]byte, *age.X25519Identity) {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate age identity: %v", err)
	}
	var output bytes.Buffer
	var destination io.Writer = &output
	var armorWriter io.WriteCloser
	if armored {
		armorWriter = armor.NewWriter(&output)
		destination = armorWriter
	}
	encryptWriter, err := age.Encrypt(destination, identity.Recipient())
	if err != nil {
		t.Fatalf("create age fixture: %v", err)
	}
	if _, err := encryptWriter.Write(payload); err != nil {
		t.Fatalf("write age fixture: %v", err)
	}
	if err := encryptWriter.Close(); err != nil {
		t.Fatalf("close age fixture: %v", err)
	}
	if armorWriter != nil {
		if err := armorWriter.Close(); err != nil {
			t.Fatalf("close age armor fixture: %v", err)
		}
	}
	return output.Bytes(), identity
}

func containsString(values []string, substring string) bool {
	for _, value := range values {
		if strings.Contains(value, substring) {
			return true
		}
	}
	return false
}
