package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const agentLockFileName = "agent.lock"

type agentProcessLock struct {
	file *os.File
}

func acquireAgentLock(store *Store) (*agentProcessLock, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return nil, fmt.Errorf("backup store path is unavailable for the agent lock")
	}
	path := filepath.Join(filepath.Dir(store.path), agentLockFileName)
	file, err := openAgentLockFile(path)
	if err != nil {
		return nil, err
	}
	locked, err := tryLockAgentFile(file)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock backup agent state %s: %w", path, err)
	}
	if !locked {
		detail := readAgentLockDetail(file)
		_ = file.Close()
		if detail != "" {
			return nil, fmt.Errorf("another dbterm backup agent is already running (%s); stop it before starting a second agent", detail)
		}
		return nil, fmt.Errorf("another dbterm backup agent is already running; stop it before starting a second agent")
	}
	if err := writeAgentLockDetail(file); err != nil {
		_ = unlockAgentFile(file)
		_ = file.Close()
		return nil, err
	}
	return &agentProcessLock{file: file}, nil
}

func openAgentLockFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create backup agent state directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open backup agent lock %s: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil && !os.IsPermission(err) {
		_ = file.Close()
		return nil, fmt.Errorf("protect backup agent lock %s: %w", path, err)
	}
	return file, nil
}

func writeAgentLockDetail(file *os.File) error {
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("reset backup agent lock detail: %w", err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("rewind backup agent lock detail: %w", err)
	}
	if _, err := fmt.Fprintf(file, "pid=%d\n", os.Getpid()); err != nil {
		return fmt.Errorf("write backup agent lock detail: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync backup agent lock detail: %w", err)
	}
	return nil
}

func readAgentLockDetail(file *os.File) string {
	if _, err := file.Seek(0, 0); err != nil {
		return ""
	}
	buffer := make([]byte, 128)
	count, _ := file.Read(buffer)
	value := strings.TrimSpace(string(buffer[:count]))
	if strings.HasPrefix(value, "pid=") {
		if pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(value, "pid="))); err == nil && pid > 0 {
			return fmt.Sprintf("pid %d", pid)
		}
	}
	return ""
}

func (lock *agentProcessLock) release() {
	if lock == nil || lock.file == nil {
		return
	}
	_ = unlockAgentFile(lock.file)
	_ = lock.file.Close()
	lock.file = nil
}

// AgentProcessRunning reports whether the default backup catalog's scheduler
// lock is currently held. The lock file itself is persistent; only the kernel
// lock is authoritative, so a crash cannot strand the agent in a running state.
func AgentProcessRunning() (bool, error) {
	storePath, err := DefaultStorePath()
	if err != nil {
		return false, err
	}
	path := filepath.Join(filepath.Dir(storePath), agentLockFileName)
	file, err := openAgentLockFile(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	locked, err := tryLockAgentFile(file)
	if err != nil {
		return false, fmt.Errorf("probe backup agent lock %s: %w", path, err)
	}
	if !locked {
		return true, nil
	}
	if err := unlockAgentFile(file); err != nil {
		return false, fmt.Errorf("release backup agent lock probe %s: %w", path, err)
	}
	return false, nil
}
