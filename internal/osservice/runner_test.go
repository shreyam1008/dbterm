package osservice

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

type recordedCommand struct {
	name string
	args []string
}

type fakeCommandResponse struct {
	result commandResult
	err    error
}

type fakeCommandRunner struct {
	responses []fakeCommandResponse
	calls     []recordedCommand
}

func (f *fakeCommandRunner) Run(_ context.Context, name string, args ...string) (commandResult, error) {
	f.calls = append(f.calls, recordedCommand{name: name, args: append([]string(nil), args...)})
	if len(f.responses) == 0 {
		return commandResult{}, nil
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response.result, response.err
}

func assertCommands(t *testing.T, got []recordedCommand, want ...recordedCommand) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func containsArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestRunRequiredIncludesCommandOutput(t *testing.T) {
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{{
		result: commandResult{ExitCode: 7, Output: "manager refused the operation"},
	}}}
	_, err := runRequired(context.Background(), runner, "start service", "manager", "start", "service name")
	if err == nil {
		t.Fatal("runRequired() expected an error")
	}
	want := `start service: "manager" "start" "service name" exited with code 7: manager refused the operation`
	if err.Error() != want {
		t.Fatalf("runRequired() error = %q, want %q", err, want)
	}
}

func TestRunRequiredWrapsRunnerError(t *testing.T) {
	runner := &fakeCommandRunner{responses: []fakeCommandResponse{{err: fmt.Errorf("binary unavailable")}}}
	_, err := runRequired(context.Background(), runner, "query service", "manager", "status")
	if err == nil || err.Error() != "query service: binary unavailable" {
		t.Fatalf("runRequired() error = %v", err)
	}
}
