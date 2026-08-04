//go:build linux

package processinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Linux exposes start time in USER_HZ. The procfs ABI defines USER_HZ as 100
// independently of the kernel's internal scheduler tick rate.
const linuxProcUserHZ = 100

func readPlatform(pid int) (Metrics, error) {
	metrics := Metrics{PID: pid}
	processDir := filepath.Join("/proc", strconv.Itoa(pid))
	statPayload, err := os.ReadFile(filepath.Join(processDir, "stat"))
	if err != nil {
		if os.IsNotExist(err) {
			return metrics, nil
		}
		return metrics, fmt.Errorf("read /proc/%d/stat: %w", pid, err)
	}

	name, state, startTicks, err := parseLinuxProcStat(string(statPayload))
	if err != nil {
		return metrics, fmt.Errorf("parse /proc/%d/stat: %w", pid, err)
	}
	metrics.Name = name
	if state == "Z" || state == "X" {
		return metrics, nil
	}

	statusPayload, err := os.ReadFile(filepath.Join(processDir, "status"))
	if err != nil {
		if os.IsNotExist(err) {
			return Metrics{PID: pid}, nil
		}
		return metrics, fmt.Errorf("read /proc/%d/status: %w", pid, err)
	}
	rssBytes, err := parseLinuxRSS(string(statusPayload))
	if err != nil {
		return metrics, fmt.Errorf("parse /proc/%d/status: %w", pid, err)
	}

	procStat, err := os.ReadFile("/proc/stat")
	if err != nil {
		return metrics, fmt.Errorf("read /proc/stat for process boot time: %w", err)
	}
	bootTime, err := parseLinuxBootTime(string(procStat))
	if err != nil {
		return metrics, err
	}

	metrics.RSSBytes = rssBytes
	metrics.StartTime = bootTime.Add(time.Duration(startTicks) * time.Second / linuxProcUserHZ)
	metrics.Alive = true
	return metrics, nil
}

func parseLinuxProcStat(payload string) (name, state string, startTicks uint64, err error) {
	open := strings.IndexByte(payload, '(')
	close := strings.LastIndexByte(payload, ')')
	if open < 0 || close <= open {
		return "", "", 0, fmt.Errorf("missing parenthesized process name")
	}
	name = payload[open+1 : close]
	fields := strings.Fields(payload[close+1:])
	if len(fields) <= 19 {
		return "", "", 0, fmt.Errorf("expected at least 22 fields, found %d", len(fields)+2)
	}
	state = fields[0]
	startTicks, err = strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid process start ticks %q: %w", fields[19], err)
	}
	return name, state, startTicks, nil
}

func parseLinuxRSS(payload string) (uint64, error) {
	for _, line := range strings.Split(payload, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "VmRSS:" {
			continue
		}
		if len(fields) != 3 || !strings.EqualFold(fields[2], "kB") {
			return 0, fmt.Errorf("unexpected VmRSS value %q", strings.TrimSpace(line))
		}
		kilobytes, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid VmRSS value %q: %w", fields[1], err)
		}
		return kilobytes * 1024, nil
	}
	return 0, fmt.Errorf("VmRSS is missing")
}

func parseLinuxBootTime(payload string) (time.Time, error) {
	for _, line := range strings.Split(payload, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "btime" {
			continue
		}
		seconds, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse Linux boot time %q: %w", fields[1], err)
		}
		return time.Unix(seconds, 0), nil
	}
	return time.Time{}, fmt.Errorf("parse /proc/stat: btime is missing")
}
