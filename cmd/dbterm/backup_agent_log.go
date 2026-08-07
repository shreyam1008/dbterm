package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const backupAgentLogMaxBytes int64 = 5 * 1024 * 1024

// rollingLogWriter retains one bounded previous log and one bounded current
// log. Rotation closes the active handle before renaming so it also works on
// Windows, where an open file cannot normally be renamed.
type rollingLogWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	file     *os.File
	size     int64
	closed   bool
}

func openRollingLogWriter(path string, maxBytes int64) (*rollingLogWriter, error) {
	path = filepath.Clean(path)
	if path == "." || path == "" {
		return nil, fmt.Errorf("rolling log path is required")
	}
	if maxBytes <= 0 {
		return nil, fmt.Errorf("rolling log maximum size must be positive")
	}
	if err := capExistingLogTail(path+".1", maxBytes); err != nil {
		return nil, fmt.Errorf("bound previous backup agent log: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect backup agent log: %w", err)
	}
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("backup agent log must be a regular file: %s", path)
		}
		if info.Size() >= maxBytes {
			if err := rotateClosedLog(path, maxBytes); err != nil {
				return nil, err
			}
		}
	}
	file, size, err := openActiveLog(path)
	if err != nil {
		return nil, err
	}
	return &rollingLogWriter{path: path, maxBytes: maxBytes, file: file, size: size}, nil
}

func (writer *rollingLogWriter) Write(payload []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return 0, os.ErrClosed
	}
	if writer.file == nil {
		file, size, err := openActiveLog(writer.path)
		if err != nil {
			return 0, err
		}
		writer.file = file
		writer.size = size
	}
	originalLength := len(payload)
	if int64(len(payload)) > writer.maxBytes {
		// A single pathological error line must not defeat the bound. The tail
		// contains the most useful diagnostics and is treated as the retained
		// portion of an otherwise fully-consumed rolling log write.
		payload = payload[int64(len(payload))-writer.maxBytes:]
	}
	var rotationWarning error
	if writer.size > 0 && writer.size+int64(len(payload)) > writer.maxBytes {
		rotationWarning = writer.rotateLocked()
		if writer.file == nil {
			if rotationWarning == nil {
				rotationWarning = fmt.Errorf("backup agent log rotation left no writable active log")
			}
			return 0, rotationWarning
		}
	}
	written, writeErr := writer.file.Write(payload)
	writer.size += int64(written)
	if writeErr != nil {
		if rotationWarning != nil {
			return written, errors.Join(rotationWarning, writeErr)
		}
		return written, writeErr
	}
	if written != len(payload) {
		if rotationWarning != nil {
			return written, errors.Join(rotationWarning, io.ErrShortWrite)
		}
		return written, io.ErrShortWrite
	}
	return originalLength, rotationWarning
}

func (writer *rollingLogWriter) Close() error {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.closed {
		return nil
	}
	writer.closed = true
	if writer.file == nil {
		return nil
	}
	err := writer.file.Close()
	writer.file = nil
	return err
}

func (writer *rollingLogWriter) rotateLocked() error {
	closeErr := writer.file.Close()
	writer.file = nil
	if closeErr != nil {
		return writer.resetAfterRotationFailure(fmt.Errorf("close backup agent log before rotation: %w", closeErr))
	}
	previous := writer.path + ".1"
	if err := os.Remove(previous); err != nil && !os.IsNotExist(err) {
		return writer.resetAfterRotationFailure(fmt.Errorf("remove previous backup agent log: %w", err))
	}
	if err := os.Rename(writer.path, previous); err != nil {
		return writer.resetAfterRotationFailure(fmt.Errorf("rotate backup agent log: %w", err))
	}
	if err := os.Chmod(previous, 0o600); err != nil {
		warning := fmt.Errorf("protect rotated backup agent log: %w", err)
		file, size, openErr := openActiveLog(writer.path)
		if openErr != nil {
			return errors.Join(warning, openErr)
		}
		writer.file = file
		writer.size = size
		return warning
	}
	file, size, err := openActiveLog(writer.path)
	if err != nil {
		return err
	}
	writer.file = file
	writer.size = size
	return nil
}

func (writer *rollingLogWriter) resetAfterRotationFailure(rotationErr error) error {
	file, size, resetErr := resetActiveLog(writer.path)
	if resetErr != nil {
		return errors.Join(rotationErr, fmt.Errorf("reset active backup agent log to preserve its size bound: %w", resetErr))
	}
	writer.file = file
	writer.size = size
	return rotationErr
}

func openActiveLog(path string) (*os.File, int64, error) {
	return openValidatedLog(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY)
}

func resetActiveLog(path string) (*os.File, int64, error) {
	return openValidatedLog(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY)
}

func openValidatedLog(path string, flags int) (*os.File, int64, error) {
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return nil, 0, fmt.Errorf("open backup agent log: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, 0, fmt.Errorf("inspect open backup agent log: %w", err)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		_ = file.Close()
		return nil, 0, fmt.Errorf("inspect backup agent log path after open: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || !pathInfo.Mode().IsRegular() || !os.SameFile(info, pathInfo) {
		_ = file.Close()
		return nil, 0, fmt.Errorf("backup agent log must remain the same regular file while opening: %s", path)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, 0, fmt.Errorf("protect backup agent log: %w", err)
	}
	return file, info.Size(), nil
}

func prepareBackupAgentLogDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create backup agent log directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect backup agent log directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("backup agent log directory must be a real directory: %s", path)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("protect backup agent log directory: %w", err)
	}
	return nil
}

func formatBackupAgentLogRecord(now time.Time, line string, maxBytes int64) []byte {
	if maxBytes <= 0 {
		return nil
	}
	prefix := now.Format(time.RFC3339) + "  "
	line = strings.TrimRight(line, "\r\n")
	record := []byte(prefix + line + "\n")
	if int64(len(record)) <= maxBytes {
		return record
	}
	marker := "[truncated] "
	available := maxBytes - int64(len(prefix)+len(marker)+1)
	if available <= 0 {
		return record[int64(len(record))-maxBytes:]
	}
	lineBytes := []byte(line)
	if int64(len(lineBytes)) > available {
		lineBytes = lineBytes[int64(len(lineBytes))-available:]
	}
	return []byte(prefix + marker + string(lineBytes) + "\n")
}

func newBackupAgentLogCallbacks(file *rollingLogWriter, mirror io.Writer, now func() time.Time, warnings io.Writer) (func(string), func()) {
	if now == nil {
		now = time.Now
	}
	var emitMu sync.Mutex
	var reportOnce sync.Once
	reportLogFailure := func(err error) {
		if err == nil || warnings == nil {
			return
		}
		reportOnce.Do(func() {
			_, _ = fmt.Fprintf(warnings, "dbterm backup agent log warning: %v\n", err)
		})
	}
	emit := func(line string) {
		emitMu.Lock()
		defer emitMu.Unlock()
		record := formatBackupAgentLogRecord(now(), line, file.maxBytes)
		_, logErr := file.Write(record)
		// The durable rolling file is always written first. A closed or broken
		// foreground stdout must not suppress the persistent diagnostic.
		if mirror != nil {
			_, mirrorErr := mirror.Write(record)
			if logErr == nil {
				logErr = mirrorErr
			}
		}
		reportLogFailure(logErr)
	}
	closeLog := func() {
		emitMu.Lock()
		err := file.Close()
		emitMu.Unlock()
		reportLogFailure(err)
	}
	return emit, closeLog
}

func rotateClosedLog(path string, maxBytes int64) error {
	previous := path + ".1"
	if err := os.Remove(previous); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove previous backup agent log: %w", err)
	}
	if err := os.Rename(path, previous); err != nil {
		return fmt.Errorf("rotate backup agent log: %w", err)
	}
	if err := os.Chmod(previous, 0o600); err != nil {
		return fmt.Errorf("protect rotated backup agent log: %w", err)
	}
	if err := capExistingLogTail(previous, maxBytes); err != nil {
		return fmt.Errorf("bound rotated backup agent log: %w", err)
	}
	return nil
}

// capExistingLogTail compacts oversized legacy logs in place using a small
// fixed buffer. Source bytes always sit after destination bytes, so forward
// copying cannot overwrite data that has not yet been read.
func capExistingLogTail(path string, maxBytes int64) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("log must be a regular file: %s", path)
	}
	if info.Size() <= maxBytes {
		return os.Chmod(path, 0o600)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return err
	}
	currentInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if currentInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(openedInfo, currentInfo) {
		return fmt.Errorf("log changed while opening: %s", path)
	}
	const bufferSize = 64 * 1024
	buffer := make([]byte, bufferSize)
	sourceStart := info.Size() - maxBytes
	for destination := int64(0); destination < maxBytes; {
		remaining := maxBytes - destination
		chunk := int64(len(buffer))
		if remaining < chunk {
			chunk = remaining
		}
		read, err := file.ReadAt(buffer[:chunk], sourceStart+destination)
		if err != nil && err != io.EOF {
			return err
		}
		if read == 0 {
			return io.ErrUnexpectedEOF
		}
		written, err := file.WriteAt(buffer[:read], destination)
		if err != nil {
			return err
		}
		if written != read {
			return io.ErrShortWrite
		}
		destination += int64(read)
	}
	if err := file.Truncate(maxBytes); err != nil {
		return err
	}
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	return file.Sync()
}
