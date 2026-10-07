//go:build windows

package ui

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rivo/tview"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func TestWindowsDatabaseServiceMatches(t *testing.T) {
	for _, tc := range []struct {
		display, name, binary string
		want                  bool
	}{
		{"MySQL", "MySQL84", "", true},
		{"MySQL", "MariaDB", "", true},
		{"MySQL", "MySQLRouter", "", false},
		{"MySQL", "MySQLRouter", `C:\tools\mysqlrouter.exe`, false},
		{"MySQL", "CompanyDatabase", `"C:\Program Files\MySQL\bin\mysqld.exe" --defaults-file=C:\db.ini`, true},
		{"MySQL", "MariaDB", `"C:\Program Files\MariaDB\bin\mariadbd.exe"`, true},
		{"PostgreSQL", "postgresql-x64-18", "", true},
		{"PostgreSQL", "CustomPG", `"C:\Program Files\PostgreSQL\18\bin\pg_ctl.exe" runservice`, true},
		{"PostgreSQL", "CustomPG", `C:\tools\postgres.exe`, true},
		{"MySQL", "MySQL84", `C:\tools\other.exe --log=C:\mysql.log`, false},
		{"MySQL", "postgresql-x64-18", "", false},
	} {
		if got := windowsDatabaseServiceMatches(tc.display, tc.name, tc.binary); got != tc.want {
			t.Errorf("%+v: got %v", tc, got)
		}
	}
}

func TestWindowsServiceChildListeners(t *testing.T) {
	// Child entries can precede parents in the process snapshot.
	owners := windowsServiceDescendants(12, []windows.ProcessEntry32{
		{ProcessID: 14, ParentProcessID: 13},
		{ProcessID: 13, ParentProcessID: 12},
		{ProcessID: 15, ParentProcessID: 99},
	})
	if !reflect.DeepEqual(owners, map[uint32]bool{12: true, 13: true, 14: true}) {
		t.Fatalf("descendants = %v", owners)
	}
	output := "TCP 0.0.0.0:3306 0.0.0.0:0 LISTENING 13\nTCP [::]:3306 [::]:0 LISTENING 13\nTCP 0.0.0.0:33060 0.0.0.0:0 LISTENING 14\nTCP 127.0.0.1:5555 1.1.1.1:22 ESTABLISHED 13\nTCP 127.0.0.1:5432 0.0.0.0:0 LISTENING 15"
	if got := windowsServicePorts(output, owners); !reflect.DeepEqual(got, []string{"3306", "33060"}) {
		t.Fatalf("ports = %v", got)
	}
}

func TestWindowsServiceToggleUsesNativeConfirmation(t *testing.T) {
	a := &App{app: tview.NewApplication(), pages: tview.NewPages()}
	a.toggleService(&serviceInfo{name: "MySQL", unit: "MySQL84", installed: true, active: true, state: "Running"})
	if !a.pages.HasPage("serviceConfirm") || a.pages.HasPage("sudoPrompt") {
		t.Fatal("Windows toggle did not use native service confirmation")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runPlatformServiceCmd(ctx, "start", "must-not-be-opened", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request = %v", err)
	}
}

func TestWindowsServiceDashboardLive(t *testing.T) {
	name := os.Getenv("DBTERM_TEST_WINDOWS_SERVICE")
	if name == "" {
		t.Skip("set DBTERM_TEST_WINDOWS_SERVICE to verify an existing running MySQL service without modifying it")
	}
	info := getServiceInfo("MySQL", "mysql", "mysqld", "mysql")
	if info.probeError != "" || !info.installed || !info.active || info.unit != name || info.pid == "—" || !strings.Contains(info.port, "(listening)") {
		t.Fatalf("unexpected live service: %+v", info)
	}
	var rendered strings.Builder
	writeServiceSection(&rendered, info)
	if !strings.Contains(rendered.String(), name) || !strings.Contains(rendered.String(), "Running") || !strings.Contains(getQuickStatus("mysql"), "[green]") {
		t.Fatalf("live service not rendered correctly: %s", rendered.String())
	}
	t.Logf("Detected %s running, PID %s, ports %s, version %s without client on PATH", info.unit, info.pid, info.port, info.version)
}

type testWindowsService struct{}

func (testWindowsService) Execute(_ []string, requests <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return false, 1
	}
	defer listener.Close()
	status := svc.Status{State: svc.Running, Accepts: svc.AcceptStop}
	changes <- status
	for request := range requests {
		switch request.Cmd {
		case svc.Interrogate:
			changes <- status
		case svc.Stop:
			changes <- svc.Status{State: svc.StopPending}
			return false, 0
		}
	}
	return false, 0
}

// The copied test executable acts only as a disposable SCM fixture. No database
// server is installed, connected to, started or stopped by this test.
func TestWindowsServiceHelper(t *testing.T) {
	for _, arg := range os.Args {
		if name, ok := strings.CutPrefix(arg, "dbterm-test-service="); ok {
			if err := svc.Run(name, testWindowsService{}); err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}
}

func TestWindowsServiceLifecycle(t *testing.T) {
	m, err := mgr.Connect()
	if err != nil {
		if os.Getenv("DBTERM_TEST_SCM") == "1" || !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("connect for disposable SCM fixture: %v", err)
		}
		t.Skip("disposable SCM fixture requires administrator access; enforced in Windows CI")
	}
	defer m.Disconnect()
	for _, engine := range []struct{ display, binary, unit string }{
		{"MySQL", "mysqld.exe", "mysql"},
		{"PostgreSQL", "pg_ctl.exe", "postgresql"},
	} {
		t.Run(engine.display, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), engine.binary)
			if err := copyServiceTestExecutable(executable, path); err != nil {
				t.Fatal(err)
			}
			// Sort ahead of existing services, while matching by executable name.
			name := "000-dbterm-test-" + strconv.FormatInt(time.Now().UnixNano(), 10)
			s, err := m.CreateService(name, path, mgr.Config{StartType: mgr.StartManual}, "-test.run=^TestWindowsServiceHelper$", "--", "dbterm-test-service="+name)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				status, err := s.Query()
				if err == nil && status.State != svc.Stopped {
					if _, err := runPlatformServiceCmd(ctx, "stop", name, ""); err != nil {
						t.Errorf("clean up fixture: %v", err)
					}
				}
				if err := s.Delete(); err != nil {
					t.Errorf("delete fixture: %v", err)
				}
				s.Close()
			})
			for _, action := range []string{"start", "stop", "start", "stop"} {
				// SCM can report Stopped before the helper process releases its
				// executable. Wait for exit before restarting or removing the fixture.
				var process windows.Handle
				if action == "stop" {
					status, err := s.Query()
					if err != nil {
						t.Fatal(err)
					}
					process, err = windows.OpenProcess(windows.SYNCHRONIZE, false, status.ProcessId)
					if err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				_, err := runPlatformServiceCmd(ctx, action, name, "")
				cancel()
				if process != 0 {
					result, waitErr := windows.WaitForSingleObject(process, 5000)
					windows.CloseHandle(process)
					if waitErr != nil || result != windows.WAIT_OBJECT_0 {
						t.Fatalf("fixture did not exit: result %d, error %v", result, waitErr)
					}
				}
				if err != nil {
					t.Fatalf("%s fixture: %v", action, err)
				}
				status, err := s.Query()
				if err != nil || (action == "stop" && status.State != svc.Stopped) || (action == "start" && status.State != svc.Running) {
					t.Fatalf("%s fixture state: %+v, %v", action, status, err)
				}
				info := getServiceInfo(engine.display, "absent-dbterm-client", "unused", engine.unit)
				if action == "stop" && info.unit != name && info.active && info.probeError == "" {
					// A preinstalled running database correctly takes precedence.
					continue
				}
				if info.probeError != "" || !info.installed || info.unit != name || info.active != (action == "start") {
					t.Fatalf("%s discovery: %+v", action, info)
				}
				if action == "start" && (info.pid == "—" || !strings.Contains(info.port, "(listening)") || !strings.Contains(getQuickStatus(engine.unit), "[green]")) {
					t.Fatalf("running fixture missing PID, listener or dashboard status: %+v", info)
				}
				var rendered strings.Builder
				writeServiceSection(&rendered, info)
				if !strings.Contains(rendered.String(), name) || strings.Contains(rendered.String(), "sudo") || strings.Contains(rendered.String(), "Not Installed") {
					t.Fatalf("fixture rendering: %s", rendered.String())
				}
			}
		})
	}
}

func copyServiceTestExecutable(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(destination)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	return errors.Join(copyErr, closeErr)
}
