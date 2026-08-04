package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	backupcore "github.com/shreyam1008/dbterm/internal/backup"
)

func backupPruneCommand(args []string) error {
	fs := flag.NewFlagSet("backup prune", flag.ContinueOnError)
	confirmed := fs.Bool("yes", false, "confirm deletion of eligible recorded artifacts")
	if err := fs.Parse(args); err != nil {
		return ignoreFlagHelp(err)
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: dbterm backup prune --yes <job-id-or-name>")
	}
	if !*confirmed {
		return fmt.Errorf("retention pruning deletes eligible backup files; review the job policy, then repeat with --yes")
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	job, err := store.GetJob(context.Background(), fs.Arg(0))
	if err != nil {
		return err
	}
	removed, err := backupcore.ApplyRetention(context.Background(), store, job, time.Now().UTC())
	if err != nil {
		if len(removed) > 0 {
			return fmt.Errorf("retention safely removed %d artifact(s) before stopping: %w", len(removed), err)
		}
		return err
	}
	if len(removed) == 0 {
		fmt.Printf("Retention for %q is already satisfied; no artifact was removed.\n", job.Name)
		return nil
	}
	fmt.Printf("Retention removed %d artifact(s) for %q:\n", len(removed), job.Name)
	for _, path := range removed {
		fmt.Printf("  %s\n", filepath.Clean(strings.TrimSpace(path)))
	}
	return nil
}
