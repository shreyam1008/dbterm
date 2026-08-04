package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type failingLogMirror struct{ err error }

func (mirror failingLogMirror) Write([]byte) (int, error) { return 0, mirror.err }

func TestRollingLogWriterRotatesDuringLongLivedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	writer, err := openRollingLogWriter(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	first := bytes.Repeat([]byte("a"), 40)
	second := bytes.Repeat([]byte("b"), 40)
	if written, err := writer.Write(first); err != nil || written != len(first) {
		t.Fatalf("first Write() = %d, %v", written, err)
	}
	if written, err := writer.Write(second); err != nil || written != len(second) {
		t.Fatalf("second Write() = %d, %v", written, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if got := mustReadTestFile(t, path); !bytes.Equal(got, second) {
		t.Fatalf("active log = %q, want second write", got)
	}
	if got := mustReadTestFile(t, path+".1"); !bytes.Equal(got, first) {
		t.Fatalf("previous log = %q, want first write", got)
	}
}

func TestRollingLogWriterBoundsOversizedWritesAndLegacyLogs(t *testing.T) {
	directory := t.TempDir()
	legacyPrevious := filepath.Join(directory, "legacy.log.1")
	if err := os.WriteFile(legacyPrevious, bytes.Repeat([]byte("p"), 100), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy, err := openRollingLogWriter(filepath.Join(directory, "legacy.log"), 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(legacyPrevious); err != nil || info.Size() != 64 {
		t.Fatalf("bounded legacy previous log info = %#v, %v", info, err)
	}

	path := filepath.Join(directory, "active.log")
	writer, err := openRollingLogWriter(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("0123456789"), 10)
	if written, err := writer.Write(payload); err != nil || written != len(payload) {
		t.Fatalf("oversized Write() = %d, %v", written, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	want := payload[len(payload)-64:]
	if got := mustReadTestFile(t, path); !bytes.Equal(got, want) {
		t.Fatalf("oversized active log retained %q, want tail %q", got, want)
	}
}

func TestRollingLogWriterExactBoundaryAndRotationFailureStayBounded(t *testing.T) {
	t.Run("exact boundary", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "agent.log")
		writer, err := openRollingLogWriter(path, 64)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(bytes.Repeat([]byte("x"), 64)); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
			t.Fatalf("exact-boundary write rotated early: %v", err)
		}
		if _, err := writer.Write([]byte("y")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(path + ".1"); err != nil || info.Size() != 64 {
			t.Fatalf("previous log info = %#v, %v", info, err)
		}
		if got := mustReadTestFile(t, path); string(got) != "y" {
			t.Fatalf("active log = %q", got)
		}
	})

	t.Run("blocked archive replacement", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "agent.log")
		writer, err := openRollingLogWriter(path, 64)
		if err != nil {
			t.Fatal(err)
		}
		first := bytes.Repeat([]byte("a"), 40)
		second := bytes.Repeat([]byte("b"), 40)
		if _, err := writer.Write(first); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path+".1", 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path+".1", "blocker"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		written, err := writer.Write(second)
		if err == nil || written != len(second) {
			t.Fatalf("rotation fallback Write() = %d, %v", written, err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if got := mustReadTestFile(t, path); !bytes.Equal(got, second) || int64(len(got)) > 64 {
			t.Fatalf("bounded fallback active log = %q", got)
		}
	})
}

func TestRollingLogWriterSerializesConcurrentWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	const maxBytes = int64(512)
	writer, err := openRollingLogWriter(path, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for worker := 0; worker < 12; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := 0; index < 100; index++ {
				if _, err := writer.Write([]byte("one complete diagnostic line\n")); err != nil {
					t.Errorf("Write() error = %v", err)
					return
				}
			}
		}()
	}
	group.Wait()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{path, path + ".1"} {
		info, err := os.Stat(candidate)
		if err != nil {
			t.Fatalf("stat %s: %v", candidate, err)
		}
		if info.Size() > maxBytes {
			t.Fatalf("%s size = %d, exceeds %d", candidate, info.Size(), maxBytes)
		}
	}
	if _, err := writer.Write([]byte("after close")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("Write() after Close error = %v, want os.ErrClosed", err)
	}
}

func TestOpenBackupAgentLoggerRotatesWhileProcessRemainsRunning(t *testing.T) {
	logDirectory := t.TempDir()
	t.Setenv("DBTERM_LOG_DIR", logDirectory)
	path := filepath.Join(logDirectory, "dbterm-backup-agent.log")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(backupAgentLogMaxBytes - 8); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	emit, closeLog, err := openBackupAgentLogger(true)
	if err != nil {
		t.Fatal(err)
	}
	emit("this line crosses the active log limit")
	closeLog()
	previous, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if previous.Size() != backupAgentLogMaxBytes-8 {
		t.Fatalf("previous log size = %d", previous.Size())
	}
	current, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if current.Size() == 0 || current.Size() > backupAgentLogMaxBytes {
		t.Fatalf("current log size = %d", current.Size())
	}
}

func TestBackupAgentLogCallbacksPersistBeforeFailingMirror(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	writer, err := openRollingLogWriter(path, 256)
	if err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("mirror closed")
	var warnings bytes.Buffer
	now := func() time.Time { return time.Date(2026, time.August, 4, 10, 11, 12, 0, time.UTC) }
	emit, closeLog := newBackupAgentLogCallbacks(writer, failingLogMirror{err: wantErr}, now, &warnings)
	emit("first diagnostic")
	emit("second diagnostic")
	closeLog()
	contents := string(mustReadTestFile(t, path))
	if !strings.Contains(contents, "2026-08-04T10:11:12Z  first diagnostic\n") || !strings.Contains(contents, "second diagnostic") {
		t.Fatalf("persistent log contents = %q", contents)
	}
	if got := strings.Count(warnings.String(), "mirror closed"); got != 1 {
		t.Fatalf("warning count = %d; warnings = %q", got, warnings.String())
	}
}

func TestFormatBackupAgentLogRecordMarksOversizedMessage(t *testing.T) {
	now := time.Date(2026, time.August, 4, 10, 11, 12, 0, time.UTC)
	record := formatBackupAgentLogRecord(now, strings.Repeat("x", 200), 80)
	if len(record) > 80 || !bytes.Contains(record, []byte("[truncated]")) || record[len(record)-1] != '\n' {
		t.Fatalf("bounded record = %q (%d bytes)", record, len(record))
	}
}

func TestBackupAgentLogPathsArePrivateAndRejectSymlinks(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "logs")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareBackupAgentLogDirectory(directory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "agent.log")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	writer, err := openRollingLogWriter(path, 256)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(directory)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("log directory mode = %#o", info.Mode().Perm())
		}
		info, err = os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("log file mode = %#o", info.Mode().Perm())
		}
	}

	if runtime.GOOS == "windows" {
		return
	}
	target := filepath.Join(directory, "target.log")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "linked.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := openRollingLogWriter(link, 256); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("openRollingLogWriter(symlink) error = %v", err)
	}
}

func mustReadTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
