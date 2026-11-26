// Package gateway connects the bot to Discord and fills the state cache
// before the MCP server starts accepting requests.
package gateway

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
)

// Intents are the gateway intents the bot identifies with: every
// non-privileged intent plus the privileged GUILD_MEMBERS intent, which the
// member lookup and search tools need. Voice states and scheduled events are
// part of the non-privileged set.
const Intents = discordgo.IntentsAllWithoutPrivileged | discordgo.IntentsGuildMembers

// ReadyTimeout bounds how long Connect waits for guilds to load and for
// member chunking to finish.
const ReadyTimeout = 2 * time.Minute

// Connect opens a gateway session and blocks until every guild the bot is in
// has been received and its members have been chunked into the state cache,
// or until ReadyTimeout passes.
func Connect(token string, log *slog.Logger) (*discordgo.Session, error) {
	s, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	s.Identify.Intents = Intents
	s.State.TrackMembers = true
	s.State.TrackVoice = true
	s.State.TrackRoles = true
	s.State.TrackChannels = true
	s.State.TrackThreads = true
	s.State.TrackEmojis = true
	s.State.MaxMessageCount = 0
	s.ShouldReconnectOnError = true

	w := newReadyWaiter()
	s.AddHandler(func(_ *discordgo.Session, r *discordgo.Ready) { w.ready(r) })
	s.AddHandler(func(s *discordgo.Session, g *discordgo.GuildCreate) {
		if w.guildCreated(g.ID) {
			if err := s.RequestGuildMembers(g.ID, "", 0, "", false); err != nil {
				log.Warn("member chunk request failed", "guild", g.ID, "error", err)
				w.chunked(g.ID)
			}
		}
	})
	s.AddHandler(func(_ *discordgo.Session, c *discordgo.GuildMembersChunk) {
		if c.ChunkIndex+1 >= c.ChunkCount {
			w.chunked(c.GuildID)
		}
	})

	if err := s.Open(); err != nil {
		return nil, err
	}

	select {
	case <-w.done:
		log.Info("discord gateway ready", "guilds", len(s.State.Guilds))
	case <-time.After(ReadyTimeout):
		log.Warn("timed out waiting for guilds and members to load; continuing with a partial cache")
	}
	return s, nil
}

// readyWaiter tracks which guilds still need to arrive and be chunked.
type readyWaiter struct {
	mu       sync.Mutex
	gotReady bool
	pending  map[string]bool // guild ID -> chunked
	created  map[string]bool
	done     chan struct{}
	closed   bool
}

func newReadyWaiter() *readyWaiter {
	return &readyWaiter{pending: map[string]bool{}, created: map[string]bool{}, done: make(chan struct{})}
}

func (w *readyWaiter) ready(r *discordgo.Ready) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.gotReady = true
	for _, g := range r.Guilds {
		if _, ok := w.pending[g.ID]; !ok {
			w.pending[g.ID] = false
		}
	}
	w.checkLocked()
}

// guildCreated records a GUILD_CREATE and reports whether members should be
// requested for it (only the first time a guild is seen).
func (w *readyWaiter) guildCreated(id string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.created[id] {
		return false
	}
	w.created[id] = true
	if _, ok := w.pending[id]; !ok {
		w.pending[id] = false
	}
	return true
}

func (w *readyWaiter) chunked(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending[id] = true
	w.checkLocked()
}

func (w *readyWaiter) checkLocked() {
	if w.closed || !w.gotReady {
		return
	}
	for id, done := range w.pending {
		if !done || !w.created[id] {
			return
		}
	}
	w.closed = true
	close(w.done)
}

// StartupFailure turns a connection error into the actionable message that is
// printed to stderr, where MCP clients surface it.
func StartupFailure(err error) string {
	var b strings.Builder
	details := errorChain(err)
	lower := strings.ToLower(details)

	var closeErr *websocket.CloseError
	code := 0
	if errors.As(err, &closeErr) {
		code = closeErr.Code
	}
	var restErr *discordgo.RESTError
	if errors.As(err, &restErr) && restErr.Response != nil && restErr.Response.StatusCode == 401 {
		code = 4004
	}

	b.WriteString("ERROR: Could not connect to Discord; the MCP server cannot start.\n")
	switch {
	case code == 4004 || strings.Contains(lower, "token") || strings.Contains(lower, "authentication failed"):
		b.WriteString("  Likely cause: Discord rejected DISCORD_TOKEN (missing, malformed, or reset/revoked).\n")
		b.WriteString("  Fix: create a fresh token in the Discord Developer Portal (Bot -> Reset Token) and set it\n")
		b.WriteString("       WITHOUT surrounding quotes, e.g. DISCORD_TOKEN=abc... (an env-file keeps quotes literally).\n")
	case code == 4014 || strings.Contains(lower, "intent") || strings.Contains(lower, "4014"):
		b.WriteString("  Likely cause: a privileged gateway intent is not enabled for this bot.\n")
		b.WriteString("  Fix: in the Discord Developer Portal (Bot -> Privileged Gateway Intents), enable\n")
		b.WriteString("       'Server Members Intent' (GUILD_MEMBERS).\n")
	}
	b.WriteString("  Details: " + details + "\n")
	return b.String()
}

func errorChain(err error) string {
	var parts []string
	for e := err; e != nil; e = errors.Unwrap(e) {
		parts = append(parts, e.Error())
	}
	if len(parts) == 0 {
		return "unknown error"
	}
	// Wrapped errors usually repeat their cause's text; keep the outermost
	// message and only add causes that contribute something new.
	out := parts[0]
	for _, p := range parts[1:] {
		if !strings.Contains(out, p) {
			out += " | " + p
		}
	}
	return fmt.Sprint(out)
}
