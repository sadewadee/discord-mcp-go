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
	// Issued is set once a token has been issued to the client, that is,
	// once an allowed user has signed in with it.
	Issued bool `json:"issued,omitempty"`
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
	// UsedAt marks a refresh token that has been exchanged. It is kept
	// until it expires so that a replay can be detected.
	UsedAt time.Time `json:"used_at,omitempty"`
}

// refreshGrace is how long after a refresh token is exchanged a second use
// is treated as a retry (refused, nothing revoked) rather than a replay
// (the whole family is revoked).
const refreshGrace = 30 * time.Second

// unusedClientTTL is how long a registration that never received a token
// is kept.
const unusedClientTTL = 24 * time.Hour

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
	seq  uint64 // bumped on every change, under mu

	writeMu sync.Mutex // serializes file writes, which happen outside mu
	written uint64     // seq of the last snapshot written, under writeMu
	log     func(msg string, args ...any)
}

func openStore(path string, now func() time.Time, log func(string, ...any)) (*store, error) {
	s := &store{path: path, now: now, log: log, data: storeData{Clients: map[string]*Client{}, Tokens: map[string]*Token{}}}
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
		// A damaged file must not keep the server down; everyone signs in
		// again. The damaged file is kept for inspection.
		aside := path + ".corrupt"
		_ = os.Rename(path, aside)
		log("OAuth data file is damaged; starting with no clients or tokens", "file", path, "moved_to", aside, "error", err)
		s.data = storeData{}
	}
	if s.data.Clients == nil {
		s.data.Clients = map[string]*Client{}
	}
	if s.data.Tokens == nil {
		s.data.Tokens = map[string]*Token{}
	}
	s.mu.Lock()
	s.pruneLocked()
	s.mu.Unlock()
	return s, nil
}

// snapshotLocked records a change and returns the data to write, or nil
// when there is no file. The caller holds s.mu and passes the result to
// persist after unlocking.
func (s *store) snapshotLocked() *snapshot {
	s.seq++
	if s.path == "" {
		return nil
	}
	b, err := json.Marshal(s.data)
	return &snapshot{seq: s.seq, data: b, err: err}
}

type snapshot struct {
	seq  uint64
	data []byte
	err  error
}

// persist writes a snapshot atomically, unless a newer one was already
// written. It runs outside s.mu so that token checks never wait for disk.
func (s *store) persist(snap *snapshot) error {
	if snap == nil {
		return nil
	}
	if snap.err != nil {
		return snap.err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if snap.seq <= s.written {
		return nil
	}
	if err := writeFileAtomic(s.path, snap.data); err != nil {
		return err
	}
	s.written = snap.seq
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".oauth-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// pruneLocked drops expired tokens and registrations that never received a
// token within unusedClientTTL. The caller holds s.mu.
func (s *store) pruneLocked() {
	now := s.now()
	for k, t := range s.data.Tokens {
		if !now.Before(t.ExpiresAt) {
			delete(s.data.Tokens, k)
		}
	}
	for id, c := range s.data.Clients {
		if !c.Issued && now.Sub(c.CreatedAt) > unusedClientTTL {
			delete(s.data.Clients, id)
		}
	}
}

// errStoreFull is returned when no client can be evicted to make room.
var errStoreFull = errors.New("too many registered clients")

// addClient stores a new client. When maxClients are registered, one
// without live tokens is evicted first: the oldest registration that never
// received a token, otherwise the least recently used one. Clients holding
// live tokens are never evicted, so registering clients in bulk cannot sign
// anyone out.
func (s *store) addClient(c *Client, maxClients int) error {
	s.mu.Lock()
	s.pruneLocked()
	if len(s.data.Clients) >= maxClients {
		live := map[string]bool{}
		for _, t := range s.data.Tokens {
			live[t.ClientID] = true
		}
		var victim *Client
		for _, cand := range s.data.Clients {
			if live[cand.ID] {
				continue
			}
			if victim == nil || evictBefore(cand, victim) {
				victim = cand
			}
		}
		if victim == nil {
			s.mu.Unlock()
			return errStoreFull
		}
		delete(s.data.Clients, victim.ID)
	}
	s.data.Clients[c.ID] = c
	snap := s.snapshotLocked()
	s.mu.Unlock()
	return s.persist(snap)
}

// evictBefore orders eviction candidates: never-used registrations first
// (oldest first), then used clients by last use.
func evictBefore(a, b *Client) bool {
	if a.Issued != b.Issued {
		return !a.Issued
	}
	if !a.Issued {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.LastUsedAt.Before(b.LastUsedAt)
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

// issue stores a new token and returns its value.
func (s *store) issue(t Token) (string, error) {
	value := randomToken()
	s.mu.Lock()
	s.pruneLocked()
	s.data.Tokens[hashToken(value)] = &t
	if c := s.data.Clients[t.ClientID]; c != nil {
		c.LastUsedAt = s.now()
		c.Issued = true
	}
	snap := s.snapshotLocked()
	s.mu.Unlock()
	return value, s.persist(snap)
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

// errRefreshReused reports a refresh token that was presented again after
// the grace period: a replay, for which the whole family has been revoked.
var errRefreshReused = errors.New("refresh token reused")

// errRefreshRetried reports a refresh token presented again within the
// grace period, typically a client retrying after a network error. It is
// refused, but nothing is revoked.
var errRefreshRetried = errors.New("refresh token already exchanged")

// useRefresh marks a refresh token as exchanged and returns it.
func (s *store) useRefresh(value string) (*Token, error) {
	s.mu.Lock()
	t, ok := s.data.Tokens[hashToken(value)]
	now := s.now()
	if !ok || t.Kind != kindRefresh || !now.Before(t.ExpiresAt) {
		s.mu.Unlock()
		return nil, nil
	}
	if !t.UsedAt.IsZero() {
		if now.Sub(t.UsedAt) <= refreshGrace {
			s.mu.Unlock()
			return nil, errRefreshRetried
		}
		s.revokeFamilyLocked(t.Family)
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return nil, errors.Join(errRefreshReused, s.persist(snap))
	}
	t.UsedAt = now
	cp := *t
	snap := s.snapshotLocked()
	s.mu.Unlock()
	return &cp, s.persist(snap)
}

// revoke deletes the token for value; for a refresh token its whole family
// is revoked.
func (s *store) revoke(value, clientID string) error {
	s.mu.Lock()
	t, ok := s.data.Tokens[hashToken(value)]
	if !ok || t.ClientID != clientID {
		s.mu.Unlock()
		return nil
	}
	if t.Kind == kindRefresh {
		s.revokeFamilyLocked(t.Family)
	} else {
		delete(s.data.Tokens, hashToken(value))
	}
	snap := s.snapshotLocked()
	s.mu.Unlock()
	return s.persist(snap)
}

func (s *store) revokeFamily(family string) error {
	s.mu.Lock()
	s.revokeFamilyLocked(family)
	snap := s.snapshotLocked()
	s.mu.Unlock()
	return s.persist(snap)
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
