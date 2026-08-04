package osservice

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceDefinitionsMatchGoldenFiles(t *testing.T) {
	tests := []struct {
		name   string
		golden string
		render func(t *testing.T) []byte
	}{
		{
			name:   "systemd",
			golden: "systemd.service.golden",
			render: func(*testing.T) []byte {
				return renderSystemdUnit(Options{
					Executable: "/opt/DB Term/dbterm%prod",
					ConfigDir:  "/home/test/.config/dbterm",
					StateDir:   "/home/test/.local/state/dbterm",
					LogDir:     "/var/log/DB Term",
				})
			},
		},
		{
			name:   "systemd system",
			golden: "systemd-system.service.golden",
			render: func(*testing.T) []byte {
				return renderSystemdSystemUnit(Options{
					Executable: "/opt/DB Term/dbterm%prod",
					ConfigDir:  "/home/test/.config/dbterm",
					StateDir:   "/home/test/.local/state/dbterm",
					LogDir:     "/var/log/DB Term",
					Scope:      ScopeSystem,
				}, "test")
			},
		},
		{
			name:   "launchd",
			golden: "launchd.plist.golden",
			render: func(*testing.T) []byte {
				return renderLaunchdPlist(Options{
					Executable: "/Applications/DBTerm & Tools/dbterm",
					ConfigDir:  "/Users/test/Library/Application Support/dbterm",
					StateDir:   "/Users/test/Library/Application Support/dbterm/state",
					LogDir:     "/Users/test/Library/Logs/DBTerm & Logs",
				})
			},
		},
		{
			name:   "launchd system",
			golden: "launchd-system.plist.golden",
			render: func(*testing.T) []byte {
				return renderLaunchdSystemPlist(Options{
					Executable: "/Applications/DBTerm & Tools/dbterm",
					ConfigDir:  "/Users/test/Library/Application Support/dbterm",
					StateDir:   "/Users/test/Library/Application Support/dbterm/state",
					LogDir:     "/Users/test/Library/Logs/DBTerm & Logs",
					Scope:      ScopeSystem,
				}, "test")
			},
		},
		{
			name:   "windows",
			golden: "windows-task.xml.golden",
			render: func(t *testing.T) []byte {
				t.Helper()
				payload, err := renderWindowsTaskXML(Options{
					Executable: `C:\Program Files\DBTerm & Tools\dbterm.exe`,
					ConfigDir:  `C:\Users\Test & Ops\AppData\Roaming\dbterm`,
					StateDir:   `C:\Users\Test & Ops\AppData\Local\dbterm`,
					LogDir:     `C:\Users\Test & Ops\Logs`,
				}, "S-1-5-21-1000")
				if err != nil {
					t.Fatalf("renderWindowsTaskXML() error = %v", err)
				}
				return payload
			},
		},
		{
			name:   "windows system",
			golden: "windows-system-task.xml.golden",
			render: func(t *testing.T) []byte {
				t.Helper()
				payload, err := renderWindowsSystemTaskXML(Options{
					Executable: `C:\Program Files\DBTerm & Tools\dbterm.exe`,
					ConfigDir:  `C:\Users\Test & Ops\AppData\Roaming\dbterm`,
					StateDir:   `C:\Users\Test & Ops\AppData\Local\dbterm`,
					LogDir:     `C:\Users\Test & Ops\Logs`,
					Scope:      ScopeSystem,
				})
				if err != nil {
					t.Fatalf("renderWindowsSystemTaskXML() error = %v", err)
				}
				return payload
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertGolden(t, test.golden, test.render(t))
		})
	}
}

func TestLaunchdDefinitionIsValidXMLAndEscapesPaths(t *testing.T) {
	payload := renderLaunchdPlist(Options{
		Executable: `/Applications/A & B/<dbterm>`,
		LogDir:     `/Users/test/Logs/A & B`,
	})
	if err := validateXML(payload); err != nil {
		t.Fatalf("launchd definition is invalid XML: %v\n%s", err, payload)
	}
}

func TestWindowsDefinitionRoundTripsDynamicValues(t *testing.T) {
	options := Options{
		Executable: `C:\Program Files\A & B\dbterm.exe`,
		LogDir:     `C:\Users\Renée & Ops\Logs`,
	}
	const userID = "S-1-5-21-123&456"
	payload, err := renderWindowsTaskXML(options, userID)
	if err != nil {
		t.Fatalf("renderWindowsTaskXML() error = %v", err)
	}

	var decoded windowsTaskDefinition
	if err := xml.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode rendered task XML: %v\n%s", err, payload)
	}
	if decoded.Actions.Exec.Command != options.Executable {
		t.Fatalf("command = %q, want %q", decoded.Actions.Exec.Command, options.Executable)
	}
	if decoded.Actions.Exec.WorkingDirectory != options.LogDir {
		t.Fatalf("working directory = %q, want %q", decoded.Actions.Exec.WorkingDirectory, options.LogDir)
	}
	if decoded.Principals.Principal.UserID != userID || decoded.Triggers.Logon.UserID != userID {
		t.Fatalf("user SID did not round-trip: principal=%q trigger=%q", decoded.Principals.Principal.UserID, decoded.Triggers.Logon.UserID)
	}
	wantArguments := windowsJoinArguments(agentCommand(options)[1:])
	if decoded.Actions.Exec.Arguments != wantArguments {
		t.Fatalf("arguments = %q, want %q", decoded.Actions.Exec.Arguments, wantArguments)
	}
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden file %s: %v", path, err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if string(got) != string(want) {
		t.Fatalf("rendered definition differs from %s\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}

func validateXML(payload []byte) error {
	decoder := xml.NewDecoder(bytes.NewReader(payload))
	for {
		if _, err := decoder.Token(); err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
