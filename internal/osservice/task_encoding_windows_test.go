//go:build windows

package osservice

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func decodeTaskFileForTest(t *testing.T, encoded []byte) string {
	t.Helper()
	if !bytes.HasPrefix(encoded, []byte{0xff, 0xfe}) || len(encoded)%2 != 0 {
		t.Fatalf("task file is not BOM-prefixed UTF-16LE")
	}
	units := make([]uint16, (len(encoded)-2)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(encoded[2+index*2:])
	}
	return string(utf16.Decode(units))
}

func TestWindowsTaskFilePreservesUnicodeAndEscapedPaths(t *testing.T) {
	options := Options{Executable: `C:\Renée & Ops\备份😀\dbterm.exe`, ConfigDir: `C:\डेटा\config`, LogDir: `C:\logs & records`}
	for _, scope := range []Scope{ScopeUser, ScopeSystem} {
		t.Run(string(scope), func(t *testing.T) {
			options.Scope = scope
			manager := windowsManager{options: options, userID: "S-1-5-21-1000"}
			payload, err := manager.renderTaskXML()
			if err != nil {
				t.Fatal(err)
			}
			decoded := decodeTaskFileForTest(t, encodeWindowsTaskFile(payload))
			want := strings.Replace(string(payload), `encoding="UTF-8"`, `encoding="UTF-16"`, 1)
			if decoded != want {
				t.Fatal("task file changed Unicode paths or XML escaping")
			}
			var definition windowsTaskDefinition
			if err := xml.Unmarshal([]byte(strings.Replace(decoded, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)), &definition); err != nil {
				t.Fatal(err)
			}
			if definition.Actions.Exec.Command != options.Executable || definition.Actions.Exec.Arguments != windowsJoinArguments(agentCommand(options)[1:]) {
				t.Fatalf("task action did not round-trip: %+v", definition.Actions.Exec)
			}
		})
	}
}

type taskEncodingRunner struct {
	fakeCommandRunner
	registered []byte
}

func (runner *taskEncodingRunner) Run(ctx context.Context, name string, args ...string) (commandResult, error) {
	if name == "schtasks.exe" && containsArgument(args, "/Create") {
		for index, argument := range args {
			if argument == "/XML" && index+1 < len(args) {
				var err error
				runner.registered, err = os.ReadFile(args[index+1])
				if err != nil {
					return commandResult{}, err
				}
			}
		}
	}
	return runner.fakeCommandRunner.Run(ctx, name, args...)
}

func TestWindowsInstallPassesEncodedFileToScheduler(t *testing.T) {
	options := Options{Executable: filepath.Join(t.TempDir(), "dbterm.exe"), LogDir: filepath.Join(t.TempDir(), "logs")}
	payload, err := renderWindowsTaskXML(options, "S-1-5-21-1000")
	if err != nil {
		t.Fatal(err)
	}
	runner := &taskEncodingRunner{fakeCommandRunner: fakeCommandRunner{responses: []fakeCommandResponse{
		{result: commandResult{Output: string(payload)}},
		{result: commandResult{Output: "3"}},
	}}}
	manager := windowsManager{options: options, userID: "S-1-5-21-1000", runner: runner}
	if err := manager.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	decoded := decodeTaskFileForTest(t, runner.registered)
	if !strings.HasPrefix(decoded, `<?xml version="1.0" encoding="UTF-16"?>`) {
		t.Fatal("registration file declaration does not match its encoding")
	}
}

func TestWindowsTaskEnabledReadsSchedulerEncodings(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		payload := []byte(xml.Header + fmt.Sprintf("<Task><Settings><Enabled>%t</Enabled></Settings></Task>", enabled))
		littleEndian := encodeWindowsTaskFile(payload)
		bigEndian := append([]byte(nil), littleEndian...)
		for index := 0; index < len(bigEndian); index += 2 {
			bigEndian[index], bigEndian[index+1] = bigEndian[index+1], bigEndian[index]
		}
		for _, output := range []string{string(payload), decodeTaskFileForTest(t, littleEndian), string(littleEndian), string(bigEndian)} {
			got, err := windowsTaskEnabled(output)
			if err != nil || got != enabled {
				t.Fatalf("Windows task enabled = %v, %v; want %v", got, err, enabled)
			}
		}
	}
	if _, err := windowsTaskEnabled("\xff\xfe<"); err == nil {
		t.Fatal("accepted truncated UTF-16 output")
	}
}

// Explicitly opt in: this creates one uniquely named, disabled test task, reads
// it back, then removes it. It never runs a command or modifies dbterm's agent.
func TestWindowsTaskRegistrationIntegration(t *testing.T) {
	if os.Getenv("DBTERM_TEST_TASK_SCHEDULER") != "1" {
		t.Skip("set DBTERM_TEST_TASK_SCHEDULER=1 for native registration and readback")
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	options := Options{Executable: filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe"), ConfigDir: `C:\Renée & Ops\备份😀\config`, LogDir: t.TempDir()}
	payload, err := renderWindowsTaskXML(options, current.Uid)
	if err != nil {
		t.Fatal(err)
	}
	disabled := strings.ReplaceAll(string(payload), "<Enabled>true</Enabled>", "<Enabled>false</Enabled>")
	path, err := writePrivateTempFile(t.TempDir(), "task-*.xml", encodeWindowsTaskFile([]byte(disabled)))
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("dbterm Encoding QA %d-%d", os.Getpid(), time.Now().UnixNano())
	runner := execCommandRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := runRequired(ctx, runner, "register disabled test task", "schtasks.exe", "/Create", "/TN", name, "/XML", path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := runRequired(ctx, runner, "remove test task", "schtasks.exe", "/Delete", "/TN", name, "/F"); err != nil {
			t.Error(err)
		}
	})
	result, err := runRequired(ctx, runner, "read registered test task", "schtasks.exe", "/Query", "/TN", name, "/XML")
	if err != nil {
		t.Fatal(err)
	}
	if enabled, err := windowsTaskEnabled(result.Output); err != nil || enabled {
		t.Fatalf("native readback: enabled=%v error=%v", enabled, err)
	}
	t.Log("Windows accepted the generated file; native status readback confirmed the task is disabled.")
}
