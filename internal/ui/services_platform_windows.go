//go:build windows

package ui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func windowsServiceManager(rights uint32) (*mgr.Mgr, error) {
	h, err := windows.OpenSCManager(nil, nil, rights)
	if err != nil {
		return nil, err
	}
	return &mgr.Mgr{Handle: h}, nil
}

func openWindowsService(m *mgr.Mgr, name string, rights uint32) (*mgr.Service, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	h, err := windows.OpenService(m.Handle, n, rights)
	if err != nil {
		return nil, err
	}
	return &mgr.Service{Name: name, Handle: h}, nil
}

func windowsDatabaseServiceMatches(display, name, binary string) bool {
	args, _ := windows.DecomposeCommandLine(binary)
	if len(args) > 0 {
		exe := strings.ToLower(filepath.Base(args[0]))
		if display == "MySQL" {
			return exe == "mysqld.exe" || exe == "mariadbd.exe"
		}
		return exe == "pg_ctl.exe" || exe == "postgres.exe"
	}
	n := strings.ToLower(name)
	if display == "MySQL" {
		return !strings.Contains(n, "router") && (strings.HasPrefix(n, "mysql") || strings.HasPrefix(n, "mariadb"))
	}
	return strings.HasPrefix(n, "postgresql")
}

func windowsServiceState(state svc.State) string {
	switch state {
	case svc.Running:
		return "Running"
	case svc.Stopped:
		return "Stopped"
	case svc.StartPending:
		return "Starting"
	case svc.StopPending:
		return "Stopping"
	case svc.Paused:
		return "Paused"
	case svc.PausePending:
		return "Pausing"
	case svc.ContinuePending:
		return "Resuming"
	default:
		return "Unknown"
	}
}

// Discovery uses read-only SCM access, independent of client binaries on PATH.
func getPlatformServiceInfo(display string) *serviceInfo {
	info := &serviceInfo{name: display, unit: "—", version: "unknown", pid: "—", ram: "—", port: "3306 (default)", user: "root (default)"}
	if display == "PostgreSQL" {
		info.port, info.user = "5432 (default)", "postgres (default)"
	}
	m, err := windowsServiceManager(windows.SC_MANAGER_CONNECT | windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		info.probeError = "Cannot inspect Windows services: " + err.Error()
		return info
	}
	defer m.Disconnect()
	names, err := m.ListServices()
	if err != nil {
		info.probeError = "Cannot list Windows services: " + err.Error()
		return info
	}
	sort.Strings(names)
	var binary string
	for _, name := range names {
		s, err := openWindowsService(m, name, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS)
		var cfg mgr.Config
		var status svc.Status
		if err == nil {
			cfg, err = s.Config()
			if err == nil {
				status, err = s.Query()
			}
			s.Close()
		}
		if !windowsDatabaseServiceMatches(display, name, cfg.BinaryPathName) {
			continue
		}
		// Prefer a running instance, keeping its exact name as the control target.
		if info.installed && info.probeError == "" && (err != nil || status.State != svc.Running) {
			continue
		}
		info.installed, info.unit = true, name
		info.probeError = ""
		if err != nil {
			info.probeError = fmt.Sprintf("Cannot inspect %s: %v", name, err)
			continue
		}
		info.active = status.State == svc.Running
		info.state = windowsServiceState(status.State)
		if status.ProcessId != 0 {
			info.pid = strconv.FormatUint(uint64(status.ProcessId), 10)
		}
		binary = cfg.BinaryPathName
		if info.active {
			break
		}
	}
	if !info.installed || info.probeError != "" {
		return info
	}
	args, _ := windows.DecomposeCommandLine(binary)
	if len(args) > 0 {
		if version := runServiceProbeCmd(args[0], "--version"); version != "" {
			info.version = compactServiceVersion(display, version)
		}
	}
	if info.active {
		pid, _ := strconv.ParseUint(info.pid, 10, 32)
		owners := windowsServiceProcessIDs(uint32(pid))
		if ports := windowsServicePorts(runServiceProbeCmd("netstat.exe", "-ano", "-p", "tcp"), owners); len(ports) > 0 {
			info.port = strings.Join(ports, ", ") + " (listening)"
		}
	}
	return info
}

// MySQL's Windows service can supervise a child that owns the TCP listeners.
func windowsServiceProcessIDs(pid uint32) map[uint32]bool {
	owners := map[uint32]bool{pid: true}
	if pid == 0 {
		return map[uint32]bool{}
	}
	h, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return owners
	}
	defer windows.CloseHandle(h)
	var entries []windows.ProcessEntry32
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err := windows.Process32First(h, &entry); err == nil; err = windows.Process32Next(h, &entry) {
		entries = append(entries, entry)
	}
	return windowsServiceDescendants(pid, entries)
}

func windowsServiceDescendants(pid uint32, entries []windows.ProcessEntry32) map[uint32]bool {
	owners := map[uint32]bool{pid: true}
	for changed := true; changed; {
		changed = false
		for _, entry := range entries {
			if owners[entry.ParentProcessID] && !owners[entry.ProcessID] {
				owners[entry.ProcessID], changed = true, true
			}
		}
	}
	return owners
}

func windowsServicePorts(output string, owners map[uint32]bool) []string {
	seen := map[string]bool{}
	var ports []string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 5 || fields[0] != "TCP" || fields[3] != "LISTENING" {
			continue
		}
		pid, err := strconv.ParseUint(fields[4], 10, 32)
		if err != nil || !owners[uint32(pid)] {
			continue
		}
		_, port, err := net.SplitHostPort(fields[1])
		if err == nil && !seen[port] {
			seen[port] = true
			ports = append(ports, port)
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		a, _ := strconv.Atoi(ports[i])
		b, _ := strconv.Atoi(ports[j])
		return a < b
	})
	return ports
}

func runPlatformServiceCmd(ctx context.Context, action, unit, _ string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rights := uint32(windows.SERVICE_QUERY_STATUS)
	target := svc.Running
	switch action {
	case "start":
		rights |= windows.SERVICE_START
	case "stop":
		rights |= windows.SERVICE_STOP
		target = svc.Stopped
	default:
		return nil, fmt.Errorf("unsupported service action %q", action)
	}
	m, err := windowsServiceManager(windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, err
	}
	defer m.Disconnect()
	s, err := openWindowsService(m, unit, rights)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil, fmt.Errorf("Windows denied permission to %s %s. Use Services (services.msc) with administrator approval: %w", action, unit, err)
	}
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if action == "start" {
		err = s.Start()
	} else {
		_, err = s.Control(svc.Stop)
	}
	if err != nil {
		return nil, err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, err := s.Query()
		if err != nil {
			return nil, err
		}
		if status.State == target {
			return nil, nil
		}
		if target == svc.Running && status.State == svc.Stopped {
			return nil, fmt.Errorf("%s stopped before startup completed (Windows exit code %d, service exit code %d)", unit, status.Win32ExitCode, status.ServiceSpecificExitCode)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
