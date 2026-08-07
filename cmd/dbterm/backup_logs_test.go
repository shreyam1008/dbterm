package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadRegularFileTailBoundsLinesAndBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(path, []byte("one\ntwo\nthree\nfour\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readRegularFileTail(path, 2, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "three\nfour\n" {
		t.Fatalf("tail = %q", got)
	}

	large := filepath.Join(t.TempDir(), "large.log")
	payload := []byte("discarded-part\nkept-one\nkept-two\n")
	if err := os.WriteFile(large, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = readRegularFileTail(large, 20, int64(len("ed-part\nkept-one\nkept-two\n")))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "kept-one\nkept-two\n" {
		t.Fatalf("byte-bounded tail = %q", got)
	}
}

func TestReadRegularFileTailValidatesLimitsAndFileType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegularFileTail(path, 0, 32); err == nil || !strings.Contains(err.Error(), "line limit") {
		t.Fatalf("zero line limit error = %v", err)
	}
	if _, err := readRegularFileTail(path, 1, 0); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("zero byte limit error = %v", err)
	}
	if _, err := readRegularFileTail(filepath.Dir(path), 1, 32); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory error = %v", err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	link := filepath.Join(filepath.Dir(path), "linked.log")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readRegularFileTail(link, 1, 32); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("symlink error = %v", err)
	}
}
