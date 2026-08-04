//go:build linux || darwin || windows

package processinfo

import "testing"

func TestReadExitedPIDIsNonFatal(t *testing.T) {
	metrics, err := Read(1 << 30)
	if err != nil {
		t.Fatalf("Read(nonexistent PID) error = %v", err)
	}
	if metrics.Alive || metrics.PID != 1<<30 {
		t.Fatalf("Read(nonexistent PID) = %#v", metrics)
	}
}
