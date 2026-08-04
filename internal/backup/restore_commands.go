package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shreyam1008/dbterm/config"
)

const restoreOutputTailBytes = 64 << 10

type restoreInvocation struct {
	label     string
	toolPath  string
	args      []string
	env       []string
	inputPath string
}

var runRestoreInvocation = defaultRunRestoreInvocation

func executePostgresArchiveRestore(ctx context.Context, plan *RestorePlan, payloadPath string, emit func(string)) error {
	tool, err := requireClientTool("pg_restore")
	if err != nil {
		return err
	}
	passwordFile, cleanup, err := writePGPassFile(filepath.Dir(payloadPath), &plan.Target)
	if err != nil {
		return err
	}
	defer cleanup()

	args := postgresArchiveRestoreArgs(plan)
	args = append(args, payloadPath)
	environment := postgresRestoreEnvironment(&plan.Target, passwordFile)
	emitRestore(emit, "Running pg_restore with the verified staged archive")
	return runRestoreInvocation(ctx, restoreInvocation{
		label: "pg_restore", toolPath: tool, args: args, env: environment,
	})
}

func executePostgresSQLRestore(ctx context.Context, plan *RestorePlan, payloadPath string, emit func(string)) error {
	if err := validatePostgresSQLForRestore(payloadPath, plan.Target.Database); err != nil {
		return err
	}
	tool, err := requireClientTool("psql")
	if err != nil {
		return err
	}
	passwordFile, cleanup, err := writePGPassFile(filepath.Dir(payloadPath), &plan.Target)
	if err != nil {
		return err
	}
	defer cleanup()

	args := postgresSQLRestoreArgs(plan, payloadPath)
	environment := postgresRestoreEnvironment(&plan.Target, passwordFile)
	emitRestore(emit, "Running psql with the verified staged SQL file")
	return runRestoreInvocation(ctx, restoreInvocation{
		label: "psql", toolPath: tool, args: args, env: environment,
	})
}

func executeMySQLRestore(ctx context.Context, plan *RestorePlan, payloadPath string, emit func(string)) error {
	if err := validateMySQLSQLForRestore(payloadPath, plan.Target.Database); err != nil {
		return err
	}
	tool, err := requireClientTool("mysql")
	if err != nil {
		return err
	}
	defaultsFile, cleanup, err := writeMySQLDefaultsFile(filepath.Dir(payloadPath), plan.Target.Password)
	if err != nil {
		return err
	}
	defer cleanup()

	args := mysqlRestoreArgs(plan, defaultsFile)
	environment := environmentWithout(os.Environ(), "MYSQL_PWD")
	environment = append(environment, "LC_ALL=C")
	emitRestore(emit, "Running mysql with the verified staged SQL file")
	return runRestoreInvocation(ctx, restoreInvocation{
		label: "mysql", toolPath: tool, args: args, env: environment, inputPath: payloadPath,
	})
}

func postgresArchiveRestoreArgs(plan *RestorePlan) []string {
	target := &plan.Target
	args := []string{
		"--host", target.Host,
		"--port", target.Port,
		"--username", target.User,
		"--dbname", target.Database,
		"--no-password",
		"--no-owner",
		"--no-privileges",
	}
	if plan.Options.StopOnError {
		args = append(args, "--exit-on-error")
	}
	if plan.Options.SingleTransaction {
		args = append(args, "--single-transaction")
	}
	if plan.Options.Mode == RestoreModeClean {
		args = append(args, "--clean", "--if-exists")
	}
	return args
}

func postgresSQLRestoreArgs(plan *RestorePlan, payloadPath string) []string {
	target := &plan.Target
	args := []string{
		"--host", target.Host,
		"--port", target.Port,
		"--username", target.User,
		"--dbname", target.Database,
		"--no-password",
		"--no-psqlrc",
	}
	if plan.Options.StopOnError {
		args = append(args, "--set", "ON_ERROR_STOP=on")
	}
	if plan.Options.SingleTransaction {
		args = append(args, "--single-transaction")
	}
	return append(args, "--file", payloadPath)
}

func mysqlRestoreArgs(plan *RestorePlan, defaultsFile string) []string {
	target := &plan.Target
	args := make([]string, 0, 12)
	if defaultsFile != "" {
		// The mysql client only recognizes this option before every other flag.
		args = append(args, "--defaults-extra-file="+defaultsFile)
	}
	args = append(args,
		"--batch",
		"--binary-mode",
		"--protocol=TCP",
		"--host="+target.Host,
		"--port="+target.Port,
		"--user="+target.User,
		"--database="+target.Database,
		"--default-character-set=utf8mb4",
		"--local-infile=0",
		"--skip-auto-rehash",
		"--disable-pager",
	)
	if !plan.Options.StopOnError {
		args = append(args, "--force")
	}
	return args
}

func postgresRestoreEnvironment(target *config.ConnectionConfig, passwordFile string) []string {
	environment := environmentWithout(os.Environ(), "PGPASSWORD", "PGPASSFILE", "PGSSLMODE")
	environment = append(environment, "LC_ALL=C")
	if passwordFile != "" {
		environment = append(environment, "PGPASSFILE="+passwordFile)
	}
	if strings.TrimSpace(target.SSLMode) != "" {
		environment = append(environment, "PGSSLMODE="+strings.TrimSpace(target.SSLMode))
	}
	return environment
}

func environmentWithout(environment []string, names ...string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		remove := false
		if found {
			for _, name := range names {
				if strings.EqualFold(key, name) {
					remove = true
					break
				}
			}
		}
		if !remove {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func defaultRunRestoreInvocation(ctx context.Context, invocation restoreInvocation) error {
	cmd := exec.CommandContext(ctx, invocation.toolPath, invocation.args...)
	cmd.Env = invocation.env
	if invocation.inputPath != "" {
		input, err := os.Open(invocation.inputPath)
		if err != nil {
			return fmt.Errorf("open verified SQL input: %w", err)
		}
		defer input.Close()
		cmd.Stdin = input
	}
	tail := &restoreTailBuffer{limit: restoreOutputTailBytes}
	cmd.Stdout = tail
	cmd.Stderr = tail
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("%s restore canceled: %w", invocation.label, context.Canceled)
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s restore timed out: %w", invocation.label, context.DeadlineExceeded)
	}
	detail := strings.TrimSpace(tail.String())
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("%s restore failed: %s", invocation.label, detail)
}

type restoreTailBuffer struct {
	data  []byte
	limit int
}

func (buffer *restoreTailBuffer) Write(data []byte) (int, error) {
	written := len(data)
	if buffer.limit <= 0 || written == 0 {
		return written, nil
	}
	if len(data) >= buffer.limit {
		buffer.data = append(buffer.data[:0], data[len(data)-buffer.limit:]...)
		return written, nil
	}
	overflow := len(buffer.data) + len(data) - buffer.limit
	if overflow > 0 {
		copy(buffer.data, buffer.data[overflow:])
		buffer.data = buffer.data[:len(buffer.data)-overflow]
	}
	buffer.data = append(buffer.data, data...)
	return written, nil
}

func (buffer *restoreTailBuffer) String() string {
	return string(buffer.data)
}

var _ io.Writer = (*restoreTailBuffer)(nil)
