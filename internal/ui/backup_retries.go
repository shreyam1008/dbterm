package ui

import (
	"fmt"
	"github.com/rivo/tview"
)

func parseBackupRetryFields(attempts, initial, maximum string) (int, int, int, error) {
	a, err := parseBackupFormInt("Max attempts", attempts, 1, 10)
	if err != nil {
		return 0, 0, 0, err
	}
	i, err := parseBackupFormInt("Initial retry seconds", initial, 1, 3600)
	if err != nil {
		return 0, 0, 0, err
	}
	m, err := parseBackupFormInt("Maximum retry seconds", maximum, i, 86400)
	if err != nil {
		return 0, 0, 0, err
	}
	return a, i, m, nil
}

func addBackupRetryFields(form *tview.Form, attempts, initial, maximum *string) {
	addBackupFormSection(form, "RETRY POLICY", "Attempts include the first run; 1 turns automatic retries off")
	for _, field := range []struct {
		label string
		value *string
	}{
		{"Max Attempts (1 = no retry)", attempts}, {"Initial Retry Seconds", initial}, {"Maximum Retry Seconds", maximum},
	} {
		value := field.value
		form.AddInputField(field.label, *value, 8, func(s string, _ rune) bool { return digitsOnly(s) }, func(s string) { *value = s })
	}
	form.AddTextView("Retry Scope", "Transient failures only.\nRetries share the job timeout.\nPublication safeguards still apply.", 0, 3, true, false)
}

func backupRetryPolicyLabel(attempts int) string {
	if attempts <= 1 {
		return "1 attempt (automatic retry off)"
	}
	return fmt.Sprintf("up to %d attempts for transient failures", attempts)
}
