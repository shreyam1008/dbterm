package backup

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	defaultSMTPHost         = "smtp.gmail.com"
	defaultSMTPStartTLSPort = 587
	defaultSMTPImplicitPort = 465
	defaultSMTPPlainPort    = 25
	smtpOperationTimeout    = 30 * time.Second
)

func (notification *EmailNotification) applyDefaults() {
	if notification == nil {
		return
	}
	notification.Policy = NotificationPolicy(strings.ToLower(strings.TrimSpace(string(notification.Policy))))
	if notification.Policy == "" {
		notification.Policy = NotificationNever
	}
	if notification.Policy == NotificationNever {
		return
	}
	notification.SMTPHost = strings.TrimSpace(notification.SMTPHost)
	if notification.SMTPHost == "" {
		notification.SMTPHost = defaultSMTPHost
	}
	notification.TLSMode = SMTPTLSMode(strings.ToLower(strings.TrimSpace(string(notification.TLSMode))))
	if notification.TLSMode == "" {
		notification.TLSMode = SMTPTLSStartTLS
	}
	if notification.SMTPPort == 0 {
		switch notification.TLSMode {
		case SMTPTLSImplicit:
			notification.SMTPPort = defaultSMTPImplicitPort
		case SMTPTLSNone:
			notification.SMTPPort = defaultSMTPPlainPort
		default:
			notification.SMTPPort = defaultSMTPStartTLSPort
		}
	}
	notification.Username = strings.TrimSpace(notification.Username)
	notification.From = strings.TrimSpace(notification.From)
	if notification.From == "" {
		notification.From = notification.Username
	}
	recipients := notification.Recipients[:0]
	for _, recipient := range notification.Recipients {
		if recipient = strings.TrimSpace(recipient); recipient != "" {
			recipients = append(recipients, recipient)
		}
	}
	notification.Recipients = recipients
}

func (notification EmailNotification) Validate() error {
	policy := NotificationPolicy(strings.ToLower(strings.TrimSpace(string(notification.Policy))))
	if policy == "" {
		policy = NotificationNever
	}
	switch policy {
	case NotificationNever:
		return nil
	case NotificationFailure, NotificationSuccess, NotificationBoth:
	default:
		return fmt.Errorf("unsupported email notification policy %q", notification.Policy)
	}
	copy := notification
	copy.Policy = policy
	copy.applyDefaults()
	if copy.SMTPHost == "" || strings.ContainsAny(copy.SMTPHost, "\x00\r\n\t /\\") {
		return fmt.Errorf("SMTP host must be a hostname or IP address without spaces or control characters")
	}
	if copy.SMTPPort < 1 || copy.SMTPPort > 65535 {
		return fmt.Errorf("SMTP port must be between 1 and 65535")
	}
	switch copy.TLSMode {
	case SMTPTLSStartTLS, SMTPTLSImplicit, SMTPTLSNone:
	default:
		return fmt.Errorf("unsupported SMTP TLS mode %q (use starttls, implicit, or none)", copy.TLSMode)
	}
	if strings.ContainsAny(copy.Username, "\x00\r\n") || strings.ContainsRune(copy.Password, '\x00') {
		return fmt.Errorf("SMTP credentials contain an unsupported control character")
	}
	if (copy.Username == "") != (copy.Password == "") {
		return fmt.Errorf("SMTP username and password must either both be set or both be empty")
	}
	if copy.TLSMode == SMTPTLSNone && copy.Username != "" && !isLocalSMTPHost(copy.SMTPHost) {
		return fmt.Errorf("SMTP authentication requires TLS unless the SMTP host is localhost")
	}
	if strings.TrimSpace(copy.From) == "" {
		return fmt.Errorf("email notification sender is required (set From or an email-shaped SMTP username)")
	}
	if _, err := parseSingleMailbox(copy.From); err != nil {
		return fmt.Errorf("email notification sender must be a valid mailbox address")
	}
	if _, err := parseRecipientMailboxes(copy.Recipients); err != nil {
		return err
	}
	return nil
}

func (notification EmailNotification) ShouldNotify(status RunStatus) bool {
	policy := NotificationPolicy(strings.ToLower(strings.TrimSpace(string(notification.Policy))))
	switch policy {
	case NotificationSuccess:
		return status == RunSucceeded
	case NotificationFailure:
		return status == RunFailed || status == RunCanceled
	case NotificationBoth:
		return status == RunSucceeded || status == RunFailed || status == RunCanceled
	default:
		return false
	}
}

func (notification EmailNotification) ShouldNotifyRun(run Run) bool {
	policy := NotificationPolicy(strings.ToLower(strings.TrimSpace(string(notification.Policy))))
	switch policy {
	case NotificationSuccess:
		return run.Status == RunSucceeded
	case NotificationFailure:
		return run.Status == RunFailed || run.Status == RunCanceled || strings.TrimSpace(run.RetentionError) != ""
	case NotificationBoth:
		return run.Status == RunSucceeded || run.Status == RunFailed || run.Status == RunCanceled || strings.TrimSpace(run.RetentionError) != ""
	default:
		return false
	}
}

// SendRunNotification sends a terminal run result according to the job policy.
// It never includes SMTP credentials in the message and redacts them from all
// returned errors, including errors returned by the remote SMTP server.
func SendRunNotification(ctx context.Context, job Job, run Run) (err error) {
	notification := job.Notification
	notification.applyDefaults()
	defer func() { err = redactSMTPError(err, notification) }()
	if err := notification.Validate(); err != nil {
		return err
	}
	if !notification.ShouldNotifyRun(run) {
		return nil
	}
	if run.Status == RunRunning || run.FinishedAt.IsZero() {
		return fmt.Errorf("email notification requires a durably completed backup run")
	}
	from, recipients, message, err := buildRunNotification(job, run, notification)
	if err != nil {
		return err
	}
	if err := sendSMTPMessage(ctx, notification, from, recipients, message, nil); err != nil {
		if ctx != nil && ctx.Err() != nil {
			return fmt.Errorf("SMTP notification stopped: %w", ctx.Err())
		}
		return err
	}
	return nil
}

// TestEmailNotification validates and exercises the same SMTP/TLS/auth path as
// scheduled notifications, regardless of the saved delivery policy. It lets a
// client verify credentials and routing before depending on a future backup.
func TestEmailNotification(ctx context.Context, notification EmailNotification) (err error) {
	policy := NotificationPolicy(strings.ToLower(strings.TrimSpace(string(notification.Policy))))
	if policy == "" || policy == NotificationNever {
		notification.Policy = NotificationBoth
	}
	notification.applyDefaults()
	defer func() { err = redactSMTPError(err, notification) }()
	if err := notification.Validate(); err != nil {
		return err
	}
	from, err := parseSingleMailbox(notification.From)
	if err != nil {
		return fmt.Errorf("parse notification sender: %w", err)
	}
	mailboxes, err := parseRecipientMailboxes(notification.Recipients)
	if err != nil {
		return err
	}
	recipients := make([]string, len(mailboxes))
	recipientHeaders := make([]string, len(mailboxes))
	for index, mailbox := range mailboxes {
		recipients[index] = mailbox.Address
		recipientHeaders[index] = mailbox.String()
	}
	now := time.Now().UTC()
	message := strings.Join([]string{
		"Date: " + now.Format(time.RFC1123Z),
		"From: " + from.String(),
		"To: " + strings.Join(recipientHeaders, ", "),
		"Subject: [dbterm] Test email notification",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		"dbterm SMTP notification test succeeded.\r\nNo backup was run or modified.\r\n",
	}, "\r\n")
	if err := sendSMTPMessage(ctx, notification, from.Address, recipients, []byte(message), nil); err != nil {
		if ctx != nil && ctx.Err() != nil {
			return fmt.Errorf("SMTP notification test stopped: %w", ctx.Err())
		}
		return err
	}
	return nil
}

func buildRunNotification(job Job, run Run, notification EmailNotification) (string, []string, []byte, error) {
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
	status := strings.ToUpper(string(run.Status))
	subjectStatus := strings.ToLower(status)
	if strings.TrimSpace(run.RetentionError) != "" {
		subjectStatus = "warning"
	}
	subject := fmt.Sprintf("[dbterm] Backup %s: %s", subjectStatus, sanitizeMailHeader(job.Name))
	finishedAt := run.FinishedAt.UTC()
	duration := run.FinishedAt.Sub(run.StartedAt)
	if duration < 0 {
		duration = 0
	}
	var body strings.Builder
	fmt.Fprintf(&body, "dbterm backup run %s\r\n\r\n", status)
	fmt.Fprintf(&body, "Job: %s\r\nRun ID: %s\r\nTrigger: %s\r\nStarted: %s\r\nFinished: %s\r\nDuration: %s\r\n",
		job.Name, run.ID, run.Trigger, run.StartedAt.UTC().Format(time.RFC3339), finishedAt.Format(time.RFC3339), duration.Round(time.Millisecond))
	if run.Status == RunSucceeded {
		fmt.Fprintf(&body, "Artifact: %s\r\nSize: %d bytes\r\nSHA-256: %s\r\n", run.Artifact.Path, run.Artifact.Size, run.Artifact.SHA256)
	} else {
		failure := strings.TrimSpace(run.Error)
		if failure == "" {
			failure = "No additional error detail was recorded."
		}
		failure = strings.ReplaceAll(strings.ReplaceAll(failure, "\r\n", "\n"), "\r", "\n")
		failure = strings.ReplaceAll(failure, "\n", "\r\n")
		fmt.Fprintf(&body, "Error: %s\r\n", failure)
	}
	if retentionError := strings.TrimSpace(run.RetentionError); retentionError != "" {
		retentionError = strings.ReplaceAll(strings.ReplaceAll(retentionError, "\r\n", "\n"), "\r", "\n")
		retentionError = strings.ReplaceAll(retentionError, "\n", "\r\n")
		fmt.Fprintf(&body, "Retention warning: %s\r\nThe newly created backup remains valid; storage cleanup needs attention.\r\n", retentionError)
	}
	message := strings.Join([]string{
		"Date: " + finishedAt.Format(time.RFC1123Z),
		"From: " + from.String(),
		"To: " + strings.Join(recipientHeaders, ", "),
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		body.String(),
	}, "\r\n")
	return from.Address, recipientAddresses, []byte(message), nil
}

func sendSMTPMessage(ctx context.Context, notification EmailNotification, from string, recipients []string, message []byte, tlsConfig *tls.Config) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	host := strings.TrimSpace(notification.SMTPHost)
	address := net.JoinHostPort(host, strconv.Itoa(notification.SMTPPort))
	dialer := &net.Dialer{Timeout: smtpOperationTimeout}
	rawConnection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("connect to SMTP server %s: %w", address, err)
	}
	connection := rawConnection
	serverName := strings.Trim(host, "[]")
	config := cloneSMTPConfig(tlsConfig, serverName)
	if notification.TLSMode == SMTPTLSImplicit {
		tlsConnection := tls.Client(rawConnection, config)
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			_ = rawConnection.Close()
			return fmt.Errorf("establish implicit SMTP TLS: %w", err)
		}
		connection = tlsConnection
	}
	deadline := time.Now().Add(smtpOperationTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		_ = connection.Close()
		return fmt.Errorf("set SMTP operation deadline: %w", err)
	}
	stopWatch := closeConnectionOnContext(ctx, connection)
	defer stopWatch()
	client, err := smtp.NewClient(connection, serverName)
	if err != nil {
		_ = connection.Close()
		return fmt.Errorf("start SMTP session: %w", err)
	}
	defer client.Close()
	if notification.TLSMode == SMTPTLSStartTLS {
		if supported, _ := client.Extension("STARTTLS"); !supported {
			return fmt.Errorf("SMTP server does not advertise required STARTTLS support")
		}
		if err := client.StartTLS(config); err != nil {
			return fmt.Errorf("establish SMTP STARTTLS: %w", err)
		}
	}
	if notification.Username != "" {
		var auth smtp.Auth = smtp.PlainAuth("", notification.Username, notification.Password, serverName)
		if notification.TLSMode == SMTPTLSNone {
			// Validation permits plaintext authentication only for literal
			// loopback addresses and the reserved .localhost namespace. The
			// standard library's PlainAuth has the same security rule but only
			// recognizes three exact spellings, so use the equivalent local-only
			// implementation for the wider loopback set.
			auth = localPlainAuth{username: notification.Username, password: notification.Password}
		}
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("authenticate to SMTP server: %w", err)
		}
	}
	if err := client.Mail(from); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("set SMTP recipient %s: %w", recipient, err)
		}
	}
	data, err := client.Data()
	if err != nil {
		return fmt.Errorf("start SMTP message body: %w", err)
	}
	if _, err := data.Write(message); err != nil {
		_ = data.Close()
		return fmt.Errorf("write SMTP message body: %w", err)
	}
	if err := data.Close(); err != nil {
		return fmt.Errorf("finish SMTP message body: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("close SMTP session: %w", err)
	}
	return nil
}

type localPlainAuth struct {
	username string
	password string
}

func (auth localPlainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + auth.username + "\x00" + auth.password), nil
}

func (localPlainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, fmt.Errorf("SMTP server sent an unexpected authentication challenge")
	}
	return nil, nil
}

func cloneSMTPConfig(source *tls.Config, serverName string) *tls.Config {
	var config *tls.Config
	if source == nil {
		config = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		config = source.Clone()
		if config.MinVersion == 0 {
			config.MinVersion = tls.VersionTLS12
		}
	}
	if config.ServerName == "" {
		config.ServerName = serverName
	}
	return config
}

func closeConnectionOnContext(ctx context.Context, connection io.Closer) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = connection.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

func parseSingleMailbox(value string) (*mail.Address, error) {
	if strings.ContainsAny(value, "\r\n\x00") {
		return nil, fmt.Errorf("mailbox contains a control character")
	}
	address, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(address.Address) == "" {
		return nil, fmt.Errorf("mailbox address is empty")
	}
	return address, nil
}

func parseRecipientMailboxes(values []string) ([]*mail.Address, error) {
	var recipients []*mail.Address
	for _, value := range values {
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("email notification recipient contains a control character")
		}
		parsed, err := mail.ParseAddressList(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("invalid email notification recipient %q: %w", value, err)
		}
		recipients = append(recipients, parsed...)
	}
	if len(recipients) == 0 {
		return nil, fmt.Errorf("at least one email notification recipient is required")
	}
	return recipients, nil
}

func sanitizeMailHeader(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, strings.TrimSpace(value))
}

func isLocalSMTPHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func redactSMTPError(err error, notification EmailNotification) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	for _, secret := range []string{notification.Password, notification.Username} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	message = strings.Join(strings.FieldsFunc(message, func(character rune) bool {
		return unicode.IsControl(character)
	}), " ")
	if strings.TrimSpace(message) == "" {
		message = "SMTP notification failed without a printable error message"
	}
	if message == err.Error() {
		return err
	}
	return smtpRedactedError{message: message}
}

type smtpRedactedError struct {
	message string
}

func (err smtpRedactedError) Error() string { return err.message }
