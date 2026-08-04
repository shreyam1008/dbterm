package main

import (
	"fmt"
	"time"

	backupcore "github.com/shreyam1008/dbterm/internal/backup"
)

const backupAgentExitTimeout = 15 * time.Second

func ensureBackupAgentProcessStopped(action string) error {
	running, err := backupcore.AgentProcessRunning()
	if err != nil {
		return fmt.Errorf("check the backup agent process before %s: %w", action, err)
	}
	if running {
		return fmt.Errorf("cannot %s while a foreground backup agent is running; stop `dbterm backup agent` with Ctrl+C, or stop its native service, then retry", action)
	}
	return nil
}

func waitForBackupAgentProcessExit(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		running, err := backupcore.AgentProcessRunning()
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("backup agent did not exit within %s; a foreground agent or active native task may still be running", timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
