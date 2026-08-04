package backup

import (
	"math"
	"path/filepath"
	"testing"
)

func TestDestinationDiskUsageUsesNearestExistingParent(t *testing.T) {
	requested := filepath.Join(t.TempDir(), "not-created", "backups")
	usage, err := DestinationDiskUsage(requested)
	if err != nil {
		t.Fatal(err)
	}
	wantPath, err := filepath.Abs(requested)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Path != wantPath {
		t.Fatalf("usage path = %q, want %q", usage.Path, wantPath)
	}
	if usage.CapacityBytes == 0 || usage.AvailableBytes > usage.CapacityBytes || usage.FreeBytes > usage.CapacityBytes {
		t.Fatalf("implausible disk usage: %#v", usage)
	}
	if usage.Volume == "" {
		t.Fatalf("disk usage omitted volume: %#v", usage)
	}
}

func TestFormatByteSize(t *testing.T) {
	tests := map[uint64]string{
		0:                      "0 B",
		1023:                   "1023 B",
		1024:                   "1.0 KiB",
		1536:                   "1.5 KiB",
		5 * 1024 * 1024:        "5.0 MiB",
		3 * 1024 * 1024 * 1024: "3.0 GiB",
	}
	for input, want := range tests {
		if got := FormatByteSize(input); got != want {
			t.Errorf("FormatByteSize(%d) = %q, want %q", input, got, want)
		}
	}
}

func TestSaturatingBlockBytes(t *testing.T) {
	if got := saturatingBlockBytes(math.MaxUint64, 2); got != math.MaxUint64 {
		t.Fatalf("saturatingBlockBytes overflow = %d, want MaxUint64", got)
	}
}
