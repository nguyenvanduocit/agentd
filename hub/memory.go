package hub

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MemoryTokens is a Tokens that keeps paired daemons in memory: for tests, examples and apps that do not mind losing
// pairings when they restart. A real app keeps them in its own database.
type MemoryTokens struct {
	mu sync.Mutex
	// recs is in the order the daemons were paired.
	recs []Record
}

// NewMemoryTokens is an empty MemoryTokens.
func NewMemoryTokens() *MemoryTokens { return &MemoryTokens{} }

var _ Tokens = (*MemoryTokens)(nil)

// Add keeps a new daemon for d.UserID and returns it as stored: Created is set, and a Name the user already has gets
// " (2)", " (3)"... until it is free.
func (m *MemoryTokens) Add(d Record) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	taken := map[string]bool{}
	for _, r := range m.recs {
		if r.UserID == d.UserID {
			taken[r.Name] = true
		}
	}
	d.Name = freeName(taken, d.Name)
	d.Created = time.Now().UTC().Format(time.RFC3339)
	m.recs = append(m.recs, d)
	return d, nil
}

func freeName(taken map[string]bool, name string) string {
	if !taken[name] {
		return name
	}
	for n := 2; ; n++ {
		if candidate := name + " (" + strconv.Itoa(n) + ")"; !taken[candidate] {
			return candidate
		}
	}
}

// ByHash finds the daemon a token hash belongs to.
func (m *MemoryTokens) ByHash(hash string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.recs {
		if r.TokenHash == hash {
			return r, nil
		}
	}
	return Record{}, ErrNotFound
}

// ByID finds the user's daemon id. A daemon of another user is not found.
func (m *MemoryTokens) ByID(userID, id string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.recs {
		if r.ID == id && r.UserID == userID {
			return r, nil
		}
	}
	return Record{}, ErrNotFound
}

// List returns the user's daemons, the one paired first first.
func (m *MemoryTokens) List(userID string) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Record{}
	for _, r := range m.recs {
		if r.UserID == userID {
			out = append(out, r)
		}
	}
	return out, nil
}

// Rename gives the user's daemon id the name. It fails with ErrNotFound when the user has no such daemon and with
// ErrNameTaken when another of their daemons has the name.
func (m *MemoryTokens) Rename(userID, id, name string) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at := -1
	for i, r := range m.recs {
		switch {
		case r.ID == id && r.UserID == userID:
			at = i
		case r.UserID == userID && r.Name == name:
			return Record{}, fmt.Errorf("%w: %s", ErrNameTaken, strings.TrimSpace(name))
		}
	}
	if at < 0 {
		return Record{}, ErrNotFound
	}
	m.recs[at].Name = name
	return m.recs[at], nil
}

// Seen records when the daemon id was last connected, in unix milliseconds.
func (m *MemoryTokens) Seen(id string, at int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.recs {
		if m.recs[i].ID == id {
			m.recs[i].LastSeen = at
		}
	}
	return nil
}

// Delete removes the user's daemon id; its token stops working. It fails with ErrNotFound when the user has no such daemon.
func (m *MemoryTokens) Delete(userID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, r := range m.recs {
		if r.ID == id && r.UserID == userID {
			m.recs = slices.Delete(m.recs, i, i+1)
			return nil
		}
	}
	return ErrNotFound
}
