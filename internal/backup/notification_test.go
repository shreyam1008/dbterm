package backup

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestEmailNotificationDefaultsToGmailSubmission(t *testing.T) {
	notification := EmailNotification{
		Policy: NotificationBoth, Username: "operator@example.com", Password: "app-password",
		Recipients: []string{"alerts@example.com"},
	}
	notification.applyDefaults()
	if notification.SMTPHost != defaultSMTPHost || notification.SMTPPort != 587 || notification.TLSMode != SMTPTLSStartTLS {
		t.Fatalf("Gmail defaults = %#v", notification)
	}
	if notification.From != notification.Username {
		t.Fatalf("default sender = %q, want username", notification.From)
	}
	if err := notification.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEmailNotificationRejectsUnsafePlaintextAuthenticationAndHeaders(t *testing.T) {
	tests := []struct {
		name         string
		notification EmailNotification
		want         string
	}{
		{
			name: "remote plaintext authentication",
			notification: EmailNotification{Policy: NotificationBoth, SMTPHost: "smtp.example.com", SMTPPort: 25, TLSMode: SMTPTLSNone,
				Username: "sender@example.com", Password: "secret", From: "sender@example.com", Recipients: []string{"alerts@example.com"}},
			want: "requires TLS",
		},
		{
			name: "recipient header injection",
			notification: EmailNotification{Policy: NotificationBoth, SMTPHost: "localhost", SMTPPort: 25, TLSMode: SMTPTLSNone,
				From: "sender@example.com", Recipients: []string{"alerts@example.com\r\nBcc: attacker@example.com"}},
			want: "control character",
		},
		{
			name: "host injection",
			notification: EmailNotification{Policy: NotificationBoth, SMTPHost: "localhost\r\nMAIL FROM:bad", SMTPPort: 25, TLSMode: SMTPTLSNone,
				From: "sender@example.com", Recipients: []string{"alerts@example.com"}},
			want: "SMTP host",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.notification.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want %q", err, test.want)
			}
		})
	}
}

func TestTestEmailNotificationUsesSMTPPathEvenWhenPolicyIsNever(t *testing.T) {
	server := startSMTPTestServer(t, "")
	notification := EmailNotification{
		Policy: NotificationNever, SMTPHost: server.host, SMTPPort: server.port, TLSMode: SMTPTLSNone,
		From: "dbterm@example.com", Recipients: []string{"alerts@example.com"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := TestEmailNotification(ctx, notification); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-server.message:
		if !strings.Contains(message, "Subject: [dbterm] Test email notification") || !strings.Contains(message, "No backup was run") {
			t.Fatalf("unexpected SMTP test message:\n%s", message)
		}
	case <-ctx.Done():
		t.Fatal("SMTP server did not receive test message")
	}
}

func TestSMTPAuthenticationErrorsRedactUsernameAndPassword(t *testing.T) {
	username := "private-user@example.com"
	password := "private-app-password"
	server := startSMTPTestServer(t, "authentication rejected for "+username+" using "+password)
	notification := EmailNotification{
		Policy: NotificationBoth, SMTPHost: server.host, SMTPPort: server.port, TLSMode: SMTPTLSNone,
		Username: username, Password: password, From: username, Recipients: []string{"alerts@example.com"},
	}
	run := Run{
		ID: "run_notification", Status: RunSucceeded, Trigger: TriggerManual,
		StartedAt: time.Now().Add(-time.Second), FinishedAt: time.Now(),
		Artifact: Artifact{Path: "/safe/backup.dump", Size: 42, SHA256: "abc"},
	}
	err := SendRunNotification(context.Background(), Job{Name: "nightly", Notification: notification}, run)
	if err == nil {
		t.Fatal("SendRunNotification() unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), username) || strings.Contains(err.Error(), password) || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("SMTP error was not safely redacted: %v", err)
	}
}

type smtpTestServer struct {
	host    string
	port    int
	message chan string
}

func startSMTPTestServer(t *testing.T, authenticationFailure string) smtpTestServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	host, rawPort, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	server := smtpTestServer{host: host, port: port, message: make(chan string, 1)}
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		writer := bufio.NewWriter(connection)
		writeResponse := func(format string, values ...any) bool {
			if _, err := fmt.Fprintf(writer, format, values...); err != nil {
				return false
			}
			return writer.Flush() == nil
		}
		if !writeResponse("220 localhost dbterm test SMTP\r\n") {
			return
		}
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			command := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(command, "EHLO"):
				if !writeResponse("250-localhost\r\n250 AUTH PLAIN\r\n") {
					return
				}
			case strings.HasPrefix(command, "HELO"):
				if !writeResponse("250 localhost\r\n") {
					return
				}
			case strings.HasPrefix(command, "AUTH"):
				if authenticationFailure != "" {
					_ = writeResponse("535 %s\r\n", authenticationFailure)
					return
				}
				if !writeResponse("235 authenticated\r\n") {
					return
				}
			case strings.HasPrefix(command, "MAIL FROM"), strings.HasPrefix(command, "RCPT TO"):
				if !writeResponse("250 accepted\r\n") {
					return
				}
			case command == "DATA":
				if !writeResponse("354 end with dot\r\n") {
					return
				}
				var message strings.Builder
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if line == ".\r\n" || line == ".\n" {
						break
					}
					message.WriteString(line)
				}
				server.message <- message.String()
				if !writeResponse("250 queued\r\n") {
					return
				}
			case command == "QUIT":
				_ = writeResponse("221 goodbye\r\n")
				return
			default:
				if !writeResponse("250 ok\r\n") {
					return
				}
			}
		}
	}()
	return server
}
