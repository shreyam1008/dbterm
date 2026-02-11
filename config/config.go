package config

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

// DBType represents the supported database types
type DBType string

const (
	PostgreSQL DBType = "postgresql"
	MySQL      DBType = "mysql"
	SQLite     DBType = "sqlite"
)

// ConnectionConfig holds all info for a saved database connection
type ConnectionConfig struct {
	Name     string `json:"name"`
	Type     DBType `json:"type"`
	Host     string `json:"host,omitempty"`
	Port     string `json:"port,omitempty"`
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	Database string `json:"database,omitempty"`
	FilePath string `json:"file_path,omitempty"` // SQLite only
	SSLMode  string `json:"ssl_mode,omitempty"`  // PostgreSQL only
	LastUsed string `json:"last_used,omitempty"`
	Active   bool   `json:"active"`
}

// Store manages the collection of saved connections
type Store struct {
	Connections []ConnectionConfig `json:"connections"`
	configPath  string
}

// configDir returns the path to the dbterm config directory
func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not find home directory: %w", err)
	}
	return filepath.Join(home, ".config", "dbterm"), nil
}

// configFilePath returns the full path to the connections JSON file
func configFilePath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "connections.json"), nil
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

	return s, nil
}

// Save writes the current store to disk
func (s *Store) Save() error {
	dir := filepath.Dir(s.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("could not create config directory: %w", err)
	}

	data, err := json.MarshalIndent(s.Connections, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal config: %w", err)
	}

	return os.WriteFile(s.configPath, data, 0600)
}

// Add appends a new connection and saves
func (s *Store) Add(c ConnectionConfig) error {
	c.LastUsed = time.Now().Format(time.RFC3339)
	s.Connections = append(s.Connections, c)
	return s.Save()
}

// Update replaces a connection at the given index and saves
func (s *Store) Update(index int, c ConnectionConfig) error {
	if index < 0 || index >= len(s.Connections) {
		return fmt.Errorf("index out of range")
	}
	s.Connections[index] = c
	return s.Save()
}

// Delete removes a connection at the given index and saves
func (s *Store) Delete(index int) error {
	if index < 0 || index >= len(s.Connections) {
		return fmt.Errorf("index out of range")
	}
	s.Connections = append(s.Connections[:index], s.Connections[index+1:]...)
	return s.Save()
}

// MarkUsed updates the LastUsed timestamp and Active flag for a connection
func (s *Store) MarkUsed(index int) error {
	if index < 0 || index >= len(s.Connections) {
		return fmt.Errorf("index out of range")
	}
	// Deactivate all
	for i := range s.Connections {
		s.Connections[i].Active = false
	}
	s.Connections[index].Active = true
	s.Connections[index].LastUsed = time.Now().Format(time.RFC3339)
	return s.Save()
}

// BuildConnString creates a driver-appropriate connection string
func (c *ConnectionConfig) BuildConnString() string {
	switch c.Type {
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
	default:
		return ""
	}
}

// DisplayLabel returns a human-friendly label for the connection
func (c *ConnectionConfig) DisplayLabel() string {
	switch c.Type {
	case SQLite:
		return fmt.Sprintf("[%s] %s (%s)", c.Type, c.Name, c.FilePath)
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
	default:
		return string(c.Type)
	}
}
