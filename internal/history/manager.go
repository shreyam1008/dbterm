package history

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shreyam1008/dbterm/internal/persist"
)

const (
	DefaultFileName                = "history.json"
	DefaultMaxEntriesPerConnection = 200
)

// Entry is a single history record.
type Entry struct {
	SQL       string    `json:"sql"`
	Timestamp time.Time `json:"timestamp"`
}

type fileState struct {
	Connections map[string][]Entry `json:"connections"`
}

// Manager handles query history persistence.
type Manager struct {
	mu sync.RWMutex

	path                    string
	maxEntriesPerConnection int
	now                     func() time.Time
	state                   fileState
}

// NewManager creates a manager backed by ~/.config/dbterm/history.json.
func NewManager(maxEntriesPerConnection int) (*Manager, error) {
	path, err := persist.DefaultConfigFile(DefaultFileName)
	if err != nil {
		return nil, err
	}
	return NewManagerAt(path, maxEntriesPerConnection)
}

// NewManagerAt creates a manager backed by the given file path.
func NewManagerAt(path string, maxEntriesPerConnection int) (*Manager, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("history path is required")
	}

	if maxEntriesPerConnection <= 0 {
		maxEntriesPerConnection = DefaultMaxEntriesPerConnection
	}

	m := &Manager{
		path:                    path,
		maxEntriesPerConnection: maxEntriesPerConnection,
		now:                     time.Now,
		state: fileState{
			Connections: map[string][]Entry{},
		},
	}

	if err := m.loadLocked(); err != nil {
		return nil, err
	}

	return m, nil
}

// Append stores a query in history for a connection key.
func (m *Manager) Append(connectionKey, query string) error {
	key := strings.TrimSpace(connectionKey)
	if key == "" {
		return errors.New("connection key is required")
	}

	if strings.TrimSpace(query) == "" {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	entries := append(m.state.Connections[key], Entry{
		SQL:       query,
		Timestamp: m.now().UTC(),
	})
	if overflow := len(entries) - m.maxEntriesPerConnection; overflow > 0 {
		entries = entries[overflow:]
	}
	m.state.Connections[key] = entries

	return m.saveLocked()
}

// Entries returns a copy of the history list for a connection.
func (m *Manager) Entries(connectionKey string) []Entry {
	key := strings.TrimSpace(connectionKey)
	if key == "" {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	return cloneEntries(m.state.Connections[key])
}

// Load refreshes manager state from disk.
func (m *Manager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadLocked()
}

// Save persists current manager state.
func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked()
}

func (m *Manager) loadLocked() error {
	var loaded fileState
	if err := persist.LoadJSON(m.path, &loaded); err != nil {
		return fmt.Errorf("load history: %w", err)
	}

	if loaded.Connections == nil {
		loaded.Connections = map[string][]Entry{}
	}

	for key, entries := range loaded.Connections {
		if overflow := len(entries) - m.maxEntriesPerConnection; overflow > 0 {
			entries = entries[overflow:]
		}
		loaded.Connections[key] = cloneEntries(entries)
	}

	m.state = loaded
	return nil
}

func (m *Manager) saveLocked() error {
	return persist.SaveJSON(m.path, m.state)
}

func cloneEntries(entries []Entry) []Entry {
	if len(entries) == 0 {
		return nil
	}

	cloned := make([]Entry, len(entries))
	copy(cloned, entries)
	return cloned
}
