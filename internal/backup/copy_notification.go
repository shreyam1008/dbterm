package backup

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func (notification EmailNotification) ShouldNotifyCopyRun(run CopyRun) bool {
	policy := NotificationPolicy(strings.ToLower(strings.TrimSpace(string(notification.Policy))))
	succeededWithWarning := run.Status == RunSucceeded && (strings.TrimSpace(run.RetentionError) != "" || len(run.Warnings) > 0)
	switch policy {
	case NotificationSuccess:
		return run.Status == RunSucceeded
	case NotificationFailure:
		return run.Status == RunFailed || run.Status == RunCanceled || succeededWithWarning
	case NotificationBoth:
		return run.Status == RunSucceeded || run.Status == RunFailed || run.Status == RunCanceled
	default:
		return false
	}
}

// SendCopyRunNotification reports copy health without changing the validity of
// either the producer backup or a verified vault copy.
func SendCopyRunNotification(ctx context.Context, job CopyJob, run CopyRun) (err error) {
	notification := job.Notification
	notification.applyDefaults()
	defer func() { err = redactSMTPError(err, notification) }()
	if err := notification.Validate(); err != nil {
		return err
	}
	if !notification.ShouldNotifyCopyRun(run) {
		return nil
	}
	if run.Status == RunRunning || run.FinishedAt.IsZero() {
		return fmt.Errorf("email notification requires a durably completed copy run")
	}
	from, recipients, message, err := buildCopyRunNotification(job, run, notification)
	if err != nil {
		return err
	}
	if err := sendSMTPMessage(ctx, notification, from, recipients, message, nil); err != nil {
		if ctx != nil && ctx.Err() != nil {
			return fmt.Errorf("SMTP copy notification stopped: %w", ctx.Err())
		}
		return err
	}
	return nil
}

func buildCopyRunNotification(job CopyJob, run CopyRun, notification EmailNotification) (string, []string, []byte, error) {
	from, err := parseSingleMailbox(notification.From)
	if err != nil {
		return "", nil, nil, fmt.Errorf("parse notification sender: %w", err)
	}
	recipients, err := parseRecipientMailboxes(notification.Recipients)
	if err != nil {
		return "", nil, nil, err
	}
	recipientAddresses := make([]string, len(recipients))
	recipientHeaders := make([]string, len(recipients))
	for index, recipient := range recipients {
		recipientAddresses[index] = recipient.Address
		recipientHeaders[index] = recipient.String()
	}
	status := strings.ToLower(string(run.Status))
	if strings.TrimSpace(run.RetentionError) != "" || len(run.Warnings) > 0 {
		status = "warning"
	}
	finishedAt := run.FinishedAt.UTC()
	duration := run.FinishedAt.Sub(run.StartedAt)
	if duration < 0 {
		duration = 0
	}
	source := job.Source.Location
	if strings.TrimSpace(source) == "" && strings.TrimSpace(job.SourceBackupJobID) != "" {
		source = "local backup job " + job.SourceBackupJobID
	}
	var body strings.Builder
	fmt.Fprintf(&body, "dbterm copy run %s\r\n\r\n", strings.ToUpper(string(run.Status)))
	fmt.Fprintf(&body, "Job: %s\r\nRun ID: %s\r\nMode: %s\r\nTrigger: %s\r\nSource: %s\r\nDestination: %s\r\n",
		job.Name, run.ID, job.Mode, run.Trigger, source, job.Destination.Location)
	fmt.Fprintf(&body, "Started: %s\r\nFinished: %s\r\nDuration: %s\r\nVerification required: %s\r\n",
		run.StartedAt.UTC().Format(time.RFC3339), finishedAt.Format(time.RFC3339), duration.Round(time.Millisecond), run.RequiredVerification)
	fmt.Fprintf(&body, "Discovered: %d\r\nNew copies: %d\r\nAlready present: %d\r\nBytes copied: %d\r\n",
		run.Discovered, len(run.Artifacts), run.AlreadyPresent, run.BytesCopied)
	if run.Error != "" {
		fmt.Fprintf(&body, "Error: %s\r\n", copyNotificationBodyText(run.Error))
	}
	if run.RetentionError != "" {
		fmt.Fprintf(&body, "Retention warning: %s\r\nThe verified copy remains valid; cleanup needs attention.\r\n", copyNotificationBodyText(run.RetentionError))
	}
	for _, warning := range run.Warnings {
		fmt.Fprintf(&body, "Warning: %s\r\n", copyNotificationBodyText(warning))
	}
	for index, artifact := range run.Artifacts {
		if index >= 10 {
			fmt.Fprintf(&body, "Additional copied artifacts: %d\r\n", len(run.Artifacts)-index)
			break
		}
		checksum := artifact.SHA256
		if len(checksum) > 12 {
			checksum = checksum[:12]
		}
		fmt.Fprintf(&body, "Copy: %s | %d bytes | SHA-256 %s | %s\r\n", artifact.Destination, artifact.SizeBytes, checksum, artifact.PublicationState)
	}
	message := strings.Join([]string{
		"Date: " + finishedAt.Format(time.RFC1123Z),
		"From: " + from.String(),
		"To: " + strings.Join(recipientHeaders, ", "),
		"Subject: [dbterm] Copy " + status + ": " + sanitizeMailHeader(job.Name),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		body.String(),
	}, "\r\n")
	return from.Address, recipientAddresses, []byte(message), nil
}

func copyNotificationBodyText(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	return strings.ReplaceAll(strings.TrimSpace(value), "\n", "\r\n")
}
