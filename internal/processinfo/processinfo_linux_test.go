//go:build linux

package processinfo

import (
	"testing"
	"time"
)

func TestParseLinuxProcStatHandlesSpacesAndParentheses(t *testing.T) {
	payload := "42 (dbterm backup (agent)) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 1900 20"
	name, state, ticks, err := parseLinuxProcStat(payload)
	if err != nil {
		t.Fatalf("parseLinuxProcStat() error = %v", err)
	}
	if name != "dbterm backup (agent)" || state != "S" || ticks != 1900 {
		t.Fatalf("parseLinuxProcStat() = name %q, state %q, ticks %d", name, state, ticks)
	}
}

func TestParseLinuxRSS(t *testing.T) {
	rss, err := parseLinuxRSS("Name:\tdbterm\nVmRSS:\t  1536 kB\n")
	if err != nil {
		t.Fatalf("parseLinuxRSS() error = %v", err)
	}
	if rss != 1536*1024 {
		t.Fatalf("parseLinuxRSS() = %d", rss)
	}
}

func TestParseLinuxBootTime(t *testing.T) {
	got, err := parseLinuxBootTime("cpu 1 2 3\nbtime 1700000000\n")
	if err != nil {
		t.Fatalf("parseLinuxBootTime() error = %v", err)
	}
	if want := time.Unix(1700000000, 0); !got.Equal(want) {
		t.Fatalf("parseLinuxBootTime() = %v, want %v", got, want)
	}
}
