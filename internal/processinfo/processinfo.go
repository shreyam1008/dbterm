// Package processinfo reads a small, command-free snapshot of an operating
// system process. It is intended for status displays, so an already-exited PID
// is reported as Alive=false rather than as a fatal error.
package processinfo

import (
	"errors"
	"fmt"
	"time"
)

// ErrUnsupported indicates that native process metrics are unavailable on the
// current operating system.
var ErrUnsupported = errors.New("native process metrics are not supported on this operating system")

// Metrics is a read-only point-in-time process snapshot.
type Metrics struct {
	PID       int           `json:"pid"`
	Name      string        `json:"name"`
	RSSBytes  uint64        `json:"rss_bytes"`
	StartTime time.Time     `json:"start_time"`
	Uptime    time.Duration `json:"uptime_ns"`
	Alive     bool          `json:"alive"`
}

// Read returns native metrics for pid without invoking an external command.
// A PID that has already exited returns Metrics{Alive:false} and a nil error.
// Permission and malformed operating-system data errors remain visible so a
// caller can show actionable diagnostics without treating them as app-fatal.
func Read(pid int) (Metrics, error) {
	metrics := Metrics{PID: pid}
	if pid <= 0 {
		return metrics, fmt.Errorf("process PID must be positive: %d", pid)
	}

	observed, err := readPlatform(pid)
	if observed.PID == 0 {
		observed.PID = pid
	}
	if err != nil {
		return observed, fmt.Errorf("read native metrics for process %d: %w", pid, err)
	}
	if observed.Alive && !observed.StartTime.IsZero() {
		observed.Uptime = time.Since(observed.StartTime)
		if observed.Uptime < 0 {
			observed.Uptime = 0
		}
	}
	return observed, nil
}
