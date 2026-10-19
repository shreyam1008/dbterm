package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreSQLSafetyScansHonorCancellation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "backup.sql")
	// A quoted value spanning several input buffers also exercises cancellation
	// inside quoted-data parsing, where a reader error could look like bad SQL.
	data := "CREATE TABLE notes(v TEXT); INSERT INTO notes VALUES ('" + strings.Repeat("x", 4*restoreSQLBufferSize) + "');\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, scan := range map[string]func(context.Context) error{
		"PostgreSQL": func(ctx context.Context) error { return validatePostgresSQLForRestore(ctx, path, "target") },
		"MySQL":      func(ctx context.Context) error { return validateMySQLSQLForRestore(ctx, path, "target") },
		"SQLite":     func(ctx context.Context) error { return validateSQLiteSQLForRestore(ctx, path) },
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := scan(ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled safety scan error = %v, want context.Canceled", err)
			}
			reading, stop := context.WithCancel(context.Background())
			defer stop()
			duringRead := &cancelDuringRestoreScan{Context: reading, cancel: stop}
			if err := scan(duringRead); !errors.Is(err, context.Canceled) {
				t.Fatalf("safety scan canceled during a quoted value: %v, want context.Canceled", err)
			}
			if err := scan(context.Background()); err != nil {
				t.Fatalf("uncanceled safety scan: %v", err)
			}
		})
	}
}

// Cancel at the second buffer fill rather than relying on wall-clock timing.
type cancelDuringRestoreScan struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (ctx *cancelDuringRestoreScan) Err() error {
	ctx.checks++
	if ctx.checks == 3 {
		ctx.cancel()
	}
	return ctx.Context.Err()
}
