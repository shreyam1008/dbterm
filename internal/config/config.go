package config

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
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
	Connections []ConnectionConfig `json:"connections"`
	configPath  string
}

// configDir returns the path to the dbterm config directory
func configDir() (string, error) {
	return appdirs.ConfigDir()
}

// configFilePath returns the full path to the connections JSON file
func configFilePath() (string, error) {
	return persist.DefaultConfigFile("connections.json")
}

// LoadStore reads saved connections from disk, or returns an empty store
func LoadStore() (*Store, error) {
	path, err := configFilePath()
	if err != nil {
		return nil, err
	}

	s := &Store{configPath: path}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil // No config yet, that's fine
		}
		return nil, fmt.Errorf("could not read config: %w", err)
	}

	if err := json.Unmarshal(data, &s.Connections); err != nil {
		return nil, fmt.Errorf("could not parse config: %w", err)
	}

	changed, err := s.ensureConnectionIDs()
	if err != nil {
		return s, fmt.Errorf("validate saved connection identities: %w", err)
	}
	if changed {
		if err := s.Save(); err != nil {
			return s, fmt.Errorf("persist generated connection identities: %w", err)
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
	if _, err := s.ensureConnectionIDs(); err != nil {
		return fmt.Errorf("validate connection identities: %w", err)
	}
	if err := persist.SaveJSON(s.configPath, s.Connections); err != nil {
		return fmt.Errorf("save connections: %w", err)
	}
	return nil
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

		user := url.User(c.User)
		if c.Password != "" {
			user = url.UserPassword(c.User, c.Password)
		}

		u := &url.URL{
			Scheme: "postgres",
			User:   user,
			Host:   net.JoinHostPort(c.Host, c.Port),
			Path:   c.Database,
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
		cfg.ParseTime = true
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
