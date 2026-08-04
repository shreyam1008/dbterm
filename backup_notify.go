package main

import (
	"context"
	"fmt"
	"time"

	backupcore "github.com/shreyam1008/dbterm/internal/backup"
)

func backupNotifyTestCommand(args []string) error {
	if len(args) != 1 || isHelpArg(args[0]) {
		return fmt.Errorf("usage: dbterm backup notify-test <job-id-or-name>")
	}
	store, err := backupcore.OpenDefaultStore()
	if err != nil {
		return err
	}
	defer store.Close()
	job, err := store.GetJob(context.Background(), args[0])
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := backupcore.TestEmailNotification(ctx, job.Notification); err != nil {
		return fmt.Errorf("test email for backup job %q: %w", job.Name, err)
	}
	fmt.Printf("Test email sent for backup job %q. No backup or job setting was changed.\n", job.Name)
	return nil
}
