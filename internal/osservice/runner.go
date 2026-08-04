package osservice

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type commandResult struct {
	Output   string
	ExitCode int
}

type commandRunner interface {
	Run(context.Context, string, ...string) (commandResult, error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, name string, args ...string) (commandResult, error) {
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	result := commandResult{Output: strings.TrimSpace(string(output))}
	if err == nil {
		return result, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, ctxErr
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		return result, nil
	}
	return result, fmt.Errorf("execute %s: %w", formatCommand(name, args), err)
}

func runRequired(ctx context.Context, runner commandRunner, action, name string, args ...string) (commandResult, error) {
	result, err := runner.Run(ctx, name, args...)
	if err != nil {
		return result, fmt.Errorf("%s: %w", action, err)
	}
	if result.ExitCode != 0 {
		return result, commandResultError(action, name, args, result)
	}
	return result, nil
}

func commandResultError(action, name string, args []string, result commandResult) error {
	detail := strings.TrimSpace(result.Output)
	if detail == "" {
		detail = "no command output"
	}
	return fmt.Errorf("%s: %s exited with code %d: %s", action, formatCommand(name, args), result.ExitCode, detail)
}

func formatCommand(name string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, strconv.Quote(name))
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}
