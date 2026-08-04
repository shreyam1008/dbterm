package processinfo

import (
	"os"
	"testing"
)

func TestReadRejectsInvalidPID(t *testing.T) {
	metrics, err := Read(0)
	if err == nil {
		t.Fatal("Read(0) expected an error")
	}
	if metrics.PID != 0 || metrics.Alive {
		t.Fatalf("Read(0) metrics = %#v", metrics)
	}
}

func TestReadCurrentProcess(t *testing.T) {
	metrics, err := Read(os.Getpid())
	if err != nil {
		t.Fatalf("Read(current PID) error = %v", err)
	}
	if !metrics.Alive || metrics.PID != os.Getpid() {
		t.Fatalf("Read(current PID) = %#v, want a live process", metrics)
	}
	if metrics.Name == "" || metrics.RSSBytes == 0 || metrics.StartTime.IsZero() {
		t.Fatalf("Read(current PID) missing native fields: %#v", metrics)
	}
	if metrics.Uptime < 0 {
		t.Fatalf("Read(current PID) uptime = %v", metrics.Uptime)
	}
}
