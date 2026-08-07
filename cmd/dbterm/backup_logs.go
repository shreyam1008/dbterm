package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/shreyam1008/dbterm/internal/appdirs"
)

const (
	backupAgentLogFilename = "dbterm-backup-agent.log"
	backupLogTailMaxBytes  = int64(2 * 1024 * 1024)
)

func backupLogsCommand(args []string) error {
	fs := flag.NewFlagSet("backup logs", flag.ContinueOnError)
	lineLimit := fs.Int("lines", 200, "number of recent log lines to print (1-5000)")
	previous := fs.Bool("previous", false, "show the previous rotated agent log")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: dbterm backup logs [--lines 200] [--previous]")
	}
	if *lineLimit < 1 || *lineLimit > 5000 {
		return fmt.Errorf("--lines must be between 1 and 5000")
	}
	logDir, err := appdirs.LogDir()
	if err != nil {
		return err
	}
	path := filepath.Join(logDir, backupAgentLogFilename)
	if *previous {
		path += ".1"
	}
	contents, err := readRegularFileTail(path, *lineLimit, backupLogTailMaxBytes)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Printf("Agent log: %s\nNo agent log exists yet. Start or run the backup agent, then try again.\n", path)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read backup agent log %s: %w", path, err)
	}
	fmt.Printf("Agent log: %s\n", path)
	if len(contents) == 0 {
		fmt.Println("The agent log is empty.")
		return nil
	}
	_, err = os.Stdout.Write(contents)
	return err
}

// readRegularFileTail returns at most byteLimit bytes and lineLimit complete
// recent lines. It refuses symlinks and rechecks the opened file so a log
// viewer cannot be redirected to an unrelated file between inspection/open.
func readRegularFileTail(path string, lineLimit int, byteLimit int64) ([]byte, error) {
	if lineLimit < 1 {
		return nil, fmt.Errorf("line limit must be positive")
	}
	if byteLimit < 1 {
		return nil, fmt.Errorf("byte limit must be positive")
	}
	initial, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if initial.Mode()&os.ModeSymlink != 0 || !initial.Mode().IsRegular() {
		return nil, fmt.Errorf("log path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(initial, opened) {
		return nil, fmt.Errorf("log file changed while opening")
	}

	start := int64(0)
	if opened.Size() > byteLimit {
		start = opened.Size() - byteLimit
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	payload, err := io.ReadAll(io.LimitReader(file, byteLimit))
	if err != nil {
		return nil, err
	}
	if start > 0 {
		if newline := bytes.IndexByte(payload, '\n'); newline >= 0 {
			payload = payload[newline+1:]
		} else {
			return nil, nil
		}
	}
	payload = bytes.TrimRight(payload, "\r\n")
	if len(payload) == 0 {
		return nil, nil
	}
	lines := bytes.Split(payload, []byte{'\n'})
	if len(lines) > lineLimit {
		lines = lines[len(lines)-lineLimit:]
	}
	result := bytes.Join(lines, []byte{'\n'})
	return append(result, '\n'), nil
}
