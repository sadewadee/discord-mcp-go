package tools

import (
	"errors"
	"fmt"
	"sync"

	"github.com/bwmarrin/discordgo"
)

// MaxWaiters caps concurrent wait_for_messages calls.
const MaxWaiters = 64

// waiterBuffer is how many messages one waiter can hold before further
// ones are dropped; a waiter only collects for a short burst window.
const waiterBuffer = 256

var (
	errTooManyWaiters = fmt.Errorf("Too many wait_for_messages calls in progress (limit %d); try again later", MaxWaiters)
	errHubClosed      = errors.New("The server is shutting down")
)

// hub fans MESSAGE_CREATE events out to waiting calls. A waiter subscribes
// to a key, which is a channel or thread ID; a message is delivered to the
// waiters of its channel and of that channel's parent, so waiting on a
// forum channel sees messages in all of its posts.
type hub struct {
	mu      sync.Mutex
	byKey   map[string]map[*waiter]struct{}
	count   int
	closed  bool
	closeCh chan struct{}
	// parentOf returns the parent channel ID of a thread, or "".
	parentOf func(channelID string) string
}

type waiter struct {
	key string
	ch  chan *discordgo.Message
}

func newHub(parentOf func(string) string) *hub {
	return &hub{byKey: map[string]map[*waiter]struct{}{}, closeCh: make(chan struct{}), parentOf: parentOf}
}

// subscribe registers a waiter for key. Every successful subscribe must be
// followed by unsubscribe.
func (h *hub) subscribe(key string) (*waiter, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errHubClosed
	}
	if h.count >= MaxWaiters {
		return nil, errTooManyWaiters
	}
	w := &waiter{key: key, ch: make(chan *discordgo.Message, waiterBuffer)}
	if h.byKey[key] == nil {
		h.byKey[key] = map[*waiter]struct{}{}
	}
	h.byKey[key][w] = struct{}{}
	h.count++
	return w, nil
}

func (h *hub) unsubscribe(w *waiter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.byKey[w.key]
	if _, ok := set[w]; !ok {
		return
	}
	delete(set, w)
	if len(set) == 0 {
		delete(h.byKey, w.key)
	}
	h.count--
}

// waiting reports how many waiters are subscribed.
func (h *hub) waiting() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.count
}

// close wakes every waiter (through done) and refuses new ones.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		h.closed = true
		close(h.closeCh)
	}
}

func (h *hub) done() <-chan struct{} { return h.closeCh }

// deliver hands m to the waiters of its channel and of its parent.
func (h *hub) deliver(m *discordgo.Message) {
	if m == nil {
		return
	}
	h.mu.Lock()
	if h.count == 0 {
		h.mu.Unlock()
		return
	}
	targets := h.snapshotLocked(m.ChannelID)
	othersWaiting := h.count > len(targets)
	h.mu.Unlock()

	// The parent only matters when someone waits on another channel, and
	// looking it up may need a REST call, so it is done outside the lock and
	// only then.
	if !othersWaiting {
		h.send(targets, m)
		return
	}
	if parent := h.parentOf(m.ChannelID); parent != "" {
		h.mu.Lock()
		targets = append(targets, h.snapshotLocked(parent)...)
		h.mu.Unlock()
	}
	h.send(targets, m)
}

func (h *hub) send(targets []*waiter, m *discordgo.Message) {
	for _, w := range targets {
		select {
		case w.ch <- m:
		default: // the waiter's buffer is full; it already has a burst to return
		}
	}
}

func (h *hub) snapshotLocked(key string) []*waiter {
	var out []*waiter
	for w := range h.byKey[key] {
		out = append(out, w)
	}
	return out
}

// onMessageCreate is the discordgo handler.
func (h *hub) onMessageCreate(_ *discordgo.Session, e *discordgo.MessageCreate) {
	if e != nil {
		h.deliver(e.Message)
	}
}
