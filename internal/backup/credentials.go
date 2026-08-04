package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/shreyam1008/dbterm/config"
)

func writePGPassFile(dir string, cfg *config.ConnectionConfig) (string, func(), error) {
	if cfg == nil || cfg.Password == "" {
		return "", func() {}, nil
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "host", value: nonEmpty(cfg.Host, "localhost")},
		{name: "port", value: defaultPort(cfg)},
		{name: "database", value: cfg.Database},
		{name: "user", value: cfg.User},
		{name: "password", value: cfg.Password},
	} {
		if strings.ContainsAny(field.value, "\x00\r\n") {
			return "", func() {}, fmt.Errorf("PostgreSQL %s cannot contain NUL or a line break when used with a private pgpass file", field.name)
		}
	}
	file, err := os.CreateTemp(dir, ".dbterm-pgpass-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create private PostgreSQL credential file: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, err
	}
	escape := func(value string) string {
		value = strings.ReplaceAll(value, `\`, `\\`)
		return strings.ReplaceAll(value, ":", `\:`)
	}
	line := strings.Join([]string{
		escape(nonEmpty(cfg.Host, "localhost")), escape(defaultPort(cfg)),
		escape(cfg.Database), escape(cfg.User), escape(cfg.Password),
	}, ":") + "\n"
	if _, err := file.WriteString(line); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write PostgreSQL credential file: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func writeMySQLDefaultsFile(dir, password string) (string, func(), error) {
	if password == "" {
		return "", func() {}, nil
	}
	if strings.IndexByte(password, 0) >= 0 {
		return "", func() {}, fmt.Errorf("MySQL password cannot contain a NUL byte when used with a private option file")
	}
	file, err := os.CreateTemp(dir, ".dbterm-my-*.cnf")
	if err != nil {
		return "", func() {}, fmt.Errorf("create private MySQL credential file: %w", err)
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, err
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(password)
	if _, err := fmt.Fprintf(file, "[client]\npassword=\"%s\"\n", escaped); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("write MySQL credential file: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return filepath.Clean(path), cleanup, nil
}
