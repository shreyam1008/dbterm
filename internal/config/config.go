package config

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/shreyam1008/dbterm/internal/appdirs"
	"github.com/shreyam1008/dbterm/internal/d1sql"
	"github.com/shreyam1008/dbterm/internal/persist"
)

// DBType represents the supported database types
type DBType string

const (
	PostgreSQL   DBType = "postgresql"
	MySQL        DBType = "mysql"
	SQLite       DBType = "sqlite"
	Turso        DBType = "turso"
	CloudflareD1 DBType = "d1"
)

// ConnectionConfig holds all info for a saved database connection
type ConnectionConfig struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Type       DBType `json:"type"`
	Host       string `json:"host,omitempty"`
	Port       string `json:"port,omitempty"`
	User       string `json:"user,omitempty"`
	Password   string `json:"password,omitempty"`
	Database   string `json:"database,omitempty"`
	ReadOnly   bool   `json:"read_only,omitempty"`
	FilePath   string `json:"file_path,omitempty"`   // SQLite only
	SSLMode    string `json:"ssl_mode,omitempty"`    // PostgreSQL only
	AccountID  string `json:"account_id,omitempty"`  // Cloudflare D1 only
	DatabaseID string `json:"database_id,omitempty"` // Cloudflare D1 only
	AuthToken  string `json:"auth_token,omitempty"`  // Turso & D1
	LastUsed   string `json:"last_used,omitempty"`
	Active     bool   `json:"active"`
}

// Store manages the collection of saved connections
type Store struct {
	Connections   []ConnectionConfig `json:"connections"`
	configPath    string
	recoveryPath  string
	recoveredFrom string
}

const (
	connectionsBackupSuffix         = ".bak"
	connectionsPreviousBackupSuffix = ".bak.previous"
)

// configDir returns the path to the dbterm config directory
func configDir() (string, error) {
	return appdirs.ConfigDir()
}

// configFilePath returns the full path to the connections JSON file
func configFilePath() (string, error) {
	return persist.DefaultConfigFile("connections.json")
}

func connectionsRecoveryFilePath() (string, error) {
	return profileRecoveryFilePath("connections.json")
}

func profileRecoveryFilePath(name string) (string, error) {
	stateDir, err := appdirs.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(stateDir, "profile-recovery", name), nil
}

// LoadStore reads saved connections from disk, or returns an empty store
func LoadStore() (*Store, error) {
	path, err := configFilePath()
	if err != nil {
		return nil, err
	}

	recoveryPath, err := connectionsRecoveryFilePath()
	if err != nil {
		return nil, fmt.Errorf("resolve connection recovery vault: %w", err)
	}

	s := &Store{configPath: path, recoveryPath: recoveryPath}

	data, readErr := os.ReadFile(path)
	if readErr == nil {
		if err := json.Unmarshal(data, &s.Connections); err != nil {
			if recoveryErr := s.recoverConnections(data, true); recoveryErr != nil {
				return s, fmt.Errorf("could not parse config: %w; automatic recovery failed: %v", err, recoveryErr)
			}
		}
	} else if os.IsNotExist(readErr) {
		if recoveryErr := s.recoverConnections(nil, false); recoveryErr != nil {
			if !os.IsNotExist(recoveryErr) {
				return s, recoveryErr
			}
			return s, nil // No primary or recovery copy yet, that's fine.
		}
	} else {
		return nil, fmt.Errorf("could not read config: %w", readErr)
	}

	changed, err := s.ensureConnectionIDs()
	if err != nil {
		return s, fmt.Errorf("validate saved connection identities: %w", err)
	}
	if changed {
		if err := s.Save(); err != nil {
			return s, fmt.Errorf("persist generated connection identities: %w", err)
		}
	} else {
		initializeRecovery := false
		for _, recoveryFile := range []string{path + connectionsBackupSuffix, recoveryPath} {
			_, backupErr := os.Stat(recoveryFile)
			switch {
			case backupErr == nil:
			case os.IsNotExist(backupErr):
				initializeRecovery = true
			default:
				return s, fmt.Errorf("inspect connection recovery copy %s: %w", recoveryFile, backupErr)
			}
		}
		if initializeRecovery {
			// Existing installations gain both mirrors on their first v0.9.1+
			// load, without waiting for a connection edit.
			if err := s.Save(); err != nil {
				return s, fmt.Errorf("initialize connection recovery copies: %w", err)
			}
		}
	}

	return s, nil
}

// Save writes the current store to disk
func (s *Store) Save() error {
	if s == nil {
		return fmt.Errorf("connection store is required")
	}
	if strings.TrimSpace(s.configPath) == "" {
		path, err := configFilePath()
		if err != nil {
			return err
		}
		s.configPath = path
	}
	if strings.TrimSpace(s.recoveryPath) == "" {
		path, err := connectionsRecoveryFilePath()
		if err != nil {
			return fmt.Errorf("resolve connection recovery vault: %w", err)
		}
		s.recoveryPath = path
	}
	if _, err := s.ensureConnectionIDs(); err != nil {
		return fmt.Errorf("validate connection identities: %w", err)
	}
	if err := saveConnectionsWithRecovery(s.configPath, s.recoveryPath, s.Connections); err != nil {
		return fmt.Errorf("save connections: %w", err)
	}
	return nil
}

// RecoveryNotice reports when LoadStore restored the primary connection file
// from a private recovery copy. It contains paths only, never connection data.
func (s *Store) RecoveryNotice() string {
	if s == nil || strings.TrimSpace(s.recoveredFrom) == "" {
		return ""
	}
	return fmt.Sprintf("Saved connections were automatically restored from %s because %s was missing or unreadable.", s.recoveredFrom, s.configPath)
}

func saveConnectionsWithRecovery(path, recoveryPath string, connections []ConnectionConfig) error {
	mirrors := []struct {
		current  string
		previous string
	}{
		{current: path + connectionsBackupSuffix, previous: path + connectionsPreviousBackupSuffix},
		{current: recoveryPath, previous: recoveryPath + ".previous"},
	}

	for _, mirror := range mirrors {
		// Keep the last valid mirror as a second generation before replacing
		// it. Invalid recovery files are never promoted over a known-good one.
		var previous []ConnectionConfig
		if err := persist.LoadJSON(mirror.current, &previous); err == nil {
			if _, statErr := os.Stat(mirror.current); statErr == nil {
				if err := persist.SaveJSON(mirror.previous, previous); err != nil {
					return fmt.Errorf("rotate previous recovery copy %s: %w", mirror.previous, err)
				}
			}
		}
	}

	// Write both recovery mirrors first and the primary last. The state vault
	// is outside the config directory, so whole-directory loss can self-heal.
	for _, mirror := range mirrors {
		if err := persist.SaveJSON(mirror.current, connections); err != nil {
			return fmt.Errorf("write recovery copy %s: %w", mirror.current, err)
		}
	}
	if err := persist.SaveJSON(path, connections); err != nil {
		return fmt.Errorf("write primary file: %w", err)
	}
	return nil
}

func (s *Store) recoverConnections(corruptPrimary []byte, preserveCorrupt bool) error {
	if s == nil || strings.TrimSpace(s.configPath) == "" {
		return fmt.Errorf("connection store path is required")
	}

	foundRecoveryFile := false
	candidates := []string{
		s.configPath + connectionsBackupSuffix,
		s.configPath + connectionsPreviousBackupSuffix,
	}
	if strings.TrimSpace(s.recoveryPath) != "" {
		candidates = append(candidates, s.recoveryPath, s.recoveryPath+".previous")
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			foundRecoveryFile = true
			continue
		}
		foundRecoveryFile = true
		var recovered []ConnectionConfig
		if err := persist.LoadJSON(candidate, &recovered); err != nil {
			continue
		}
		probe := &Store{Connections: append([]ConnectionConfig(nil), recovered...)}
		if _, err := probe.ensureConnectionIDs(); err != nil {
			continue
		}

		if preserveCorrupt && len(corruptPrimary) > 0 {
			preservedPath := fmt.Sprintf("%s.corrupt-%s", s.configPath, time.Now().Format("20060102-150405.000000000"))
			if err := os.MkdirAll(filepath.Dir(preservedPath), 0o700); err != nil {
				return fmt.Errorf("prepare corrupt-file preservation: %w", err)
			}
			if err := os.WriteFile(preservedPath, corruptPrimary, 0o600); err != nil {
				return fmt.Errorf("preserve unreadable primary at %s: %w", preservedPath, err)
			}
		}
		if err := persist.SaveJSON(s.configPath, recovered); err != nil {
			return fmt.Errorf("restore primary connections from %s: %w", candidate, err)
		}
		s.Connections = recovered
		s.recoveredFrom = candidate
		return nil
	}

	if foundRecoveryFile {
		return fmt.Errorf("no valid connection recovery copy was found beside %s", s.configPath)
	}
	return os.ErrNotExist
}

// Add appends a new connection and saves
func (s *Store) Add(c ConnectionConfig) error {
	if s == nil {
		return fmt.Errorf("connection store is required")
	}
	original := append([]ConnectionConfig(nil), s.Connections...)
	if strings.TrimSpace(c.ID) == "" {
		id, err := s.nextConnectionID()
		if err != nil {
			return err
		}
		c.ID = id
	} else if s.hasConnectionID(c.ID, -1) {
		return fmt.Errorf("connection ID %q already exists", c.ID)
	}
	c.LastUsed = time.Now().Format(time.RFC3339)
	s.Connections = append(s.Connections, c)
	if err := s.Save(); err != nil {
		s.Connections = original
		return err
	}
	return nil
}

// Update replaces a connection at the given index and saves
func (s *Store) Update(index int, c ConnectionConfig) error {
	if s == nil {
		return fmt.Errorf("connection store is required")
	}
	if index < 0 || index >= len(s.Connections) {
		return fmt.Errorf("index out of range")
	}
	original := append([]ConnectionConfig(nil), s.Connections...)
	storedID := original[index].ID
	if strings.TrimSpace(storedID) == "" {
		id, err := s.nextConnectionID()
		if err != nil {
			return err
		}
		storedID = id
	}
	// Identity belongs to the stored record, not to editable form data.
	c.ID = storedID
	s.Connections[index] = c
	if err := s.Save(); err != nil {
		s.Connections = original
		return err
	}
	return nil
}

// Delete removes a connection at the given index and saves
func (s *Store) Delete(index int) error {
	if s == nil {
		return fmt.Errorf("connection store is required")
	}
	if index < 0 || index >= len(s.Connections) {
		return fmt.Errorf("index out of range")
	}
	original := append([]ConnectionConfig(nil), s.Connections...)
	s.Connections = append(s.Connections[:index], s.Connections[index+1:]...)
	if err := s.Save(); err != nil {
		s.Connections = original
		return err
	}
	return nil
}

// MarkUsed updates the LastUsed timestamp and Active flag for a connection
func (s *Store) MarkUsed(index int) error {
	if s == nil {
		return fmt.Errorf("connection store is required")
	}
	if index < 0 || index >= len(s.Connections) {
		return fmt.Errorf("index out of range")
	}
	original := append([]ConnectionConfig(nil), s.Connections...)
	// Deactivate all
	for i := range s.Connections {
		s.Connections[i].Active = false
	}
	s.Connections[index].Active = true
	s.Connections[index].LastUsed = time.Now().Format(time.RFC3339)
	if err := s.Save(); err != nil {
		s.Connections = original
		return err
	}
	return nil
}

func (s *Store) ensureConnectionIDs() (bool, error) {
	if s == nil {
		return false, fmt.Errorf("connection store is required")
	}

	seen := make(map[string]struct{}, len(s.Connections))
	for _, connection := range s.Connections {
		id := connection.ID
		if strings.TrimSpace(id) == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			return false, fmt.Errorf("duplicate connection ID %q", id)
		}
		seen[id] = struct{}{}
	}

	changed := false
	for i := range s.Connections {
		if strings.TrimSpace(s.Connections[i].ID) != "" {
			continue
		}
		generated, err := nextUniqueConnectionID(seen)
		if err != nil {
			return changed, err
		}
		s.Connections[i].ID = generated
		seen[generated] = struct{}{}
		changed = true
	}
	return changed, nil
}

func (s *Store) nextConnectionID() (string, error) {
	seen := make(map[string]struct{}, len(s.Connections))
	for _, connection := range s.Connections {
		if connection.ID != "" {
			seen[connection.ID] = struct{}{}
		}
	}
	return nextUniqueConnectionID(seen)
}

func (s *Store) hasConnectionID(id string, exceptIndex int) bool {
	for i := range s.Connections {
		if i != exceptIndex && s.Connections[i].ID == id {
			return true
		}
	}
	return false
}

func nextUniqueConnectionID(existing map[string]struct{}) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		id, err := newConnectionID()
		if err != nil {
			return "", err
		}
		if _, collision := existing[id]; !collision {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not generate a unique connection ID")
}

func newConnectionID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate connection ID: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

// BuildConnString creates a driver-appropriate connection string
func (c *ConnectionConfig) BuildConnString() string {
	switch c.Type {
	case Turso:
		// libsql://... or https://...
		// If user provided a full URL in Host, use it.
		// If just a hostname, assume libsql:// scheme.
		// Append token? The driver usually takes it as a separate arg or embedded in URL?
		// libsql-client-go usually expects a URL.
		// If AuthToken is present, it might be ?authToken=... or similar,
		// OR the driver might handle it differently.
		// Actually, standard sql.Open("libsql", "url")
		// The driver docs say: `db, _ := sql.Open("libsql", "libsql://dbname.turso.io?authToken=...")`

		host := c.Host
		if !strings.Contains(host, "://") {
			host = "libsql://" + host
		}

		if c.AuthToken != "" {
			if strings.Contains(host, "?") {
				return host + "&authToken=" + c.AuthToken
			}
			return host + "?authToken=" + c.AuthToken
		}
		return host

	case CloudflareD1:
		// cfd1 parses account/token from URL user info and the database UUID
		// from the host. Building this through net/url also safely escapes tokens
		// containing URL-significant characters.
		return (&url.URL{
			Scheme: "d1",
			User:   url.UserPassword(c.AccountID, c.AuthToken),
			Host:   c.DatabaseID,
		}).String()

	case PostgreSQL:
		sslMode := c.SSLMode
		if sslMode == "" {
			sslMode = "disable"
		}
		databaseName := strings.TrimSpace(c.Database)
		if databaseName == "" {
			// A profile may represent the whole server. PostgreSQL still needs a
			// physical database for login tests and catalog discovery, so use its
			// maintenance database without changing the saved default database.
			databaseName = "postgres"
		}

		user := url.User(c.User)
		if c.Password != "" {
			user = url.UserPassword(c.User, c.Password)
		}

		u := &url.URL{
			Scheme: "postgres",
			User:   user,
			Host:   net.JoinHostPort(c.Host, c.Port),
			Path:   databaseName,
		}
		q := u.Query()
		q.Set("sslmode", sslMode)
		q.Set("connect_timeout", "5")
		u.RawQuery = q.Encode()
		return u.String()
	case MySQL:
		// Use NewConfig so driver defaults stay intact (notably AllowNativePasswords=true).
		cfg := mysql.NewConfig()
		cfg.User = c.User
		cfg.Passwd = c.Password
		cfg.Net = "tcp"
		cfg.Addr = net.JoinHostPort(c.Host, c.Port)
		cfg.DBName = c.Database
		// Keep temporal values in their database text form. This preserves
		// MySQL's zero dates and declared fractional precision instead of
		// coercing them into a lossy time.Time value.
		cfg.ParseTime = false
		cfg.Timeout = 5 * time.Second
		cfg.ReadTimeout = 30 * time.Second
		cfg.WriteTimeout = 30 * time.Second
		return cfg.FormatDSN()
	case SQLite:
		return c.FilePath
	default:
		return ""
	}
}

// DriverName returns the Go sql driver name for this config
func (c *ConnectionConfig) DriverName() string {
	switch c.Type {
	case PostgreSQL:
		return "postgres"
	case MySQL:
		return "mysql"
	case SQLite:
		return "sqlite"
	case Turso:
		return "libsql"
	case CloudflareD1:
		return d1sql.DriverName
	default:
		return ""
	}
}

// DisplayLabel returns a human-friendly label for the connection
func (c *ConnectionConfig) DisplayLabel() string {
	switch c.Type {
	case SQLite:
		return fmt.Sprintf("[%s] %s (%s)", c.Type, c.Name, c.FilePath)
	case Turso:
		return fmt.Sprintf("[%s] %s (%s)", c.Type, c.Name, c.Host)
	case CloudflareD1:
		return fmt.Sprintf("[%s] %s (%s)", c.Type, c.Name, c.DatabaseID)
	default:
		return fmt.Sprintf("[%s] %s (%s@%s:%s/%s)", c.Type, c.Name, c.User, c.Host, c.Port, c.Database)
	}
}

// TypeLabel returns a styled label for the DB type
func (c *ConnectionConfig) TypeLabel() string {
	switch c.Type {
	case PostgreSQL:
		return "PostgreSQL"
	case MySQL:
		return "MySQL"
	case SQLite:
		return "SQLite"
	case Turso:
		return "Turso"
	case CloudflareD1:
		return "Cloudflare D1"
	default:
		return string(c.Type)
	}
}
