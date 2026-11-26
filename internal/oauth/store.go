package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Client is a registered OAuth client.
type Client struct {
	ID           string    `json:"id"`
	SecretHash   string    `json:"secret_hash,omitempty"` // empty for public clients
	Name         string    `json:"name,omitempty"`
	RedirectURIs []string  `json:"redirect_uris"`
	AuthMethod   string    `json:"auth_method"`
	CreatedAt    time.Time `json:"created_at"`
	LastUsedAt   time.Time `json:"last_used_at"`
}

// Token kinds.
const (
	kindAccess  = "access"
	kindRefresh = "refresh"
)

// Token is an issued access or refresh token. Tokens are stored under the
// SHA-256 of their value, never in clear.
type Token struct {
	Kind      string    `json:"kind"`
	ClientID  string    `json:"client_id"`
	UserID    string    `json:"user_id"`
	UserName  string    `json:"user_name"`
	Resource  string    `json:"resource"`
	Scope     string    `json:"scope"`
	Family    string    `json:"family"` // all tokens descending from one authorization
	ExpiresAt time.Time `json:"expires_at"`
	// Used marks a refresh token that has been exchanged. It is kept until
	// it expires so that a replay can be detected and the family revoked.
	Used bool `json:"used,omitempty"`
}

type storeData struct {
	Clients map[string]*Client `json:"clients"`
	Tokens  map[string]*Token  `json:"tokens"`
}

// store keeps clients and tokens in memory and writes them to a JSON file
// after every change, so they survive restarts. An empty path keeps
// everything in memory only.
type store struct {
	mu   sync.Mutex
	path string
	data storeData
	now  func() time.Time
}

func openStore(path string, now func() time.Time) (*store, error) {
	s := &store{path: path, now: now, data: storeData{Clients: map[string]*Client{}, Tokens: map[string]*Token{}}}
	if path == "" {
		return s, nil
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read OAuth data file: %w", err)
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("parse OAuth data file %s: %w", path, err)
	}
	if s.data.Clients == nil {
		s.data.Clients = map[string]*Client{}
	}
	if s.data.Tokens == nil {
		s.data.Tokens = map[string]*Token{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	return s, nil
}

// saveLocked writes the data file atomically. The caller holds s.mu.
func (s *store) saveLocked() error {
	if s.path == "" {
		return nil
	}
	b, err := json.Marshal(s.data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".oauth-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}

// pruneLocked drops expired tokens. The caller holds s.mu.
func (s *store) pruneLocked() {
	now := s.now()
	for k, t := range s.data.Tokens {
		if !now.Before(t.ExpiresAt) {
			delete(s.data.Tokens, k)
		}
	}
}

func (s *store) clientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data.Clients)
}

func (s *store) addClient(c *Client) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Clients[c.ID] = c
	return s.saveLocked()
}

// client returns a copy of the client, or nil.
func (s *store) client(id string) *Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.data.Clients[id]
	if !ok {
		return nil
	}
	cp := *c
	cp.RedirectURIs = append([]string(nil), c.RedirectURIs...)
	return &cp
}

// pruneClients removes the least recently used clients until at most max
// remain, along with their tokens.
func (s *store) pruneClients(max int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.data.Clients) > max {
		var oldest *Client
		for _, c := range s.data.Clients {
			if oldest == nil || c.LastUsedAt.Before(oldest.LastUsedAt) {
				oldest = c
			}
		}
		delete(s.data.Clients, oldest.ID)
		for k, t := range s.data.Tokens {
			if t.ClientID == oldest.ID {
				delete(s.data.Tokens, k)
			}
		}
	}
	return s.saveLocked()
}

// issue stores a new token and returns its value.
func (s *store) issue(t Token) (string, error) {
	value := randomToken()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	s.data.Tokens[hashToken(value)] = &t
	if c := s.data.Clients[t.ClientID]; c != nil {
		c.LastUsedAt = s.now()
	}
	return value, s.saveLocked()
}

// lookup returns a copy of the token stored for value, or nil when it is
// unknown or expired.
func (s *store) lookup(value string) *Token {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data.Tokens[hashToken(value)]
	if !ok || !s.now().Before(t.ExpiresAt) {
		return nil
	}
	cp := *t
	return &cp
}

// errRefreshReused reports a refresh token that was presented twice.
var errRefreshReused = errors.New("refresh token reused")

// useRefresh marks a refresh token as used and returns it. A token that was
// already used signals a replay: its whole family is revoked and
// errRefreshReused is returned.
func (s *store) useRefresh(value string) (*Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data.Tokens[hashToken(value)]
	if !ok || t.Kind != kindRefresh || !s.now().Before(t.ExpiresAt) {
		return nil, nil
	}
	if t.Used {
		s.revokeFamilyLocked(t.Family)
		return nil, errors.Join(errRefreshReused, s.saveLocked())
	}
	t.Used = true
	cp := *t
	return &cp, s.saveLocked()
}

// revoke deletes the token for value; for a refresh token its whole family
// is revoked.
func (s *store) revoke(value, clientID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.data.Tokens[hashToken(value)]
	if !ok || t.ClientID != clientID {
		return nil
	}
	if t.Kind == kindRefresh {
		s.revokeFamilyLocked(t.Family)
	} else {
		delete(s.data.Tokens, hashToken(value))
	}
	return s.saveLocked()
}

func (s *store) revokeFamily(family string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokeFamilyLocked(family)
	return s.saveLocked()
}

func (s *store) revokeFamilyLocked(family string) {
	for k, t := range s.data.Tokens {
		if t.Family == family {
			delete(s.data.Tokens, k)
		}
	}
}

// randomToken returns 32 random bytes, base64url-encoded.
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}
