package autoreply

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	botID   = "900000000000000001"
	guildID = "100000000000000001"
	chanID  = "200000000000000002"
	userID  = "300000000000000003"
)

type sent struct {
	channelID string
	msg       *discordgo.MessageSend
}

type fakeDiscord struct {
	mu        sync.Mutex
	history   []*discordgo.Message
	sent      []sent
	reactions []string
	typing    int
	parents   map[string]string
	sendErr   error
}

func (f *fakeDiscord) History(channelID, beforeID string, limit int) ([]*discordgo.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if limit < len(f.history) {
		return f.history[:limit], nil
	}
	return f.history, nil
}

func (f *fakeDiscord) Send(channelID string, msg *discordgo.MessageSend) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sent{channelID, msg})
	return f.sendErr
}

func (f *fakeDiscord) Typing(string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.typing++
	return nil
}

func (f *fakeDiscord) React(channelID, messageID, emoji string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reactions = append(f.reactions, messageID+" "+emoji)
	return nil
}

func (f *fakeDiscord) Parent(channelID string) (string, error) {
	return f.parents[channelID], nil
}

type fakeLLM struct {
	mu       sync.Mutex
	answer   string
	err      error
	calls    [][]Message
	block    chan struct{} // when set, Complete waits for it
	inFlight atomic.Int32
	maxSeen  atomic.Int32
}

func (f *fakeLLM) Complete(ctx context.Context, msgs []Message) (string, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		m := f.maxSeen.Load()
		if n <= m || f.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, msgs)
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	return f.answer, f.err
}

func (f *fakeLLM) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// render stands in for the real mention renderer: it replaces the bot
// mention with "@Bot" and user mentions with "@User".
func render(m *discordgo.Message, _ string) string {
	c := strings.ReplaceAll(m.Content, "<@"+botID+">", "@Bot")
	return strings.ReplaceAll(c, "<@"+userID+">", "@Alice")
}

func newListener(t *testing.T, d *fakeDiscord, llm *fakeLLM, mod func(*Settings)) (*Listener, *bytes.Buffer) {
	t.Helper()
	cfg := Settings{
		GuildID:         guildID,
		SystemPrompt:    "SYSTEM",
		ContextMessages: 15,
		UserCooldown:    10 * time.Second,
		MaxConcurrent:   4,
	}
	if mod != nil {
		mod(&cfg)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	return New(cfg, d, llm, render, func() string { return botID }, log), &logs
}

var nextID atomic.Int64

func msg(content string, mod func(*discordgo.Message)) *discordgo.Message {
	id := 400000000000000000 + nextID.Add(1)
	m := &discordgo.Message{
		ID:        itoa(id),
		ChannelID: chanID,
		GuildID:   guildID,
		Type:      discordgo.MessageTypeDefault,
		Content:   content,
		Author:    &discordgo.User{ID: userID, Username: "alice"},
		Mentions:  []*discordgo.User{{ID: botID}},
	}
	if mod != nil {
		mod(m)
	}
	return m
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestTriggerRules(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*discordgo.Message)
		cfg  func(*Settings)
		want bool
	}{
		{"mention in guild", nil, nil, true},
		{"reply to the bot with ping", func(m *discordgo.Message) {
			m.Type = discordgo.MessageTypeReply
			m.Content = "follow-up"
		}, nil, true},
		// Without a guild filter, so only the DM rule can reject it.
		{"DM", func(m *discordgo.Message) { m.GuildID = "" }, func(s *Settings) { s.GuildID = "" }, false},
		{"other guild", func(m *discordgo.Message) { m.GuildID = "100000000000000009" }, nil, false},
		{"any guild when unset", func(m *discordgo.Message) { m.GuildID = "100000000000000009" }, func(s *Settings) { s.GuildID = "" }, true},
		{"bot author", func(m *discordgo.Message) { m.Author.Bot = true }, nil, false},
		{"the bot itself", func(m *discordgo.Message) { m.Author.ID = botID }, nil, false},
		{"webhook", func(m *discordgo.Message) { m.WebhookID = "1" }, nil, false},
		{"system message", func(m *discordgo.Message) { m.Type = discordgo.MessageTypeGuildMemberJoin }, nil, false},
		{"no mention", func(m *discordgo.Message) { m.Mentions = nil }, nil, false},
		{"other user mentioned", func(m *discordgo.Message) { m.Mentions = []*discordgo.User{{ID: userID}} }, nil, false},
		{"@everyone only", func(m *discordgo.Message) {
			m.Mentions = nil
			m.MentionEveryone = true
			m.Content = "@everyone help"
		}, nil, false},
		{"role mention only", func(m *discordgo.Message) {
			m.Mentions = nil
			m.MentionRoles = []string{"500000000000000005"}
		}, nil, false},
		{"allowed channel", nil, func(s *Settings) { s.ChannelIDs = map[string]bool{chanID: true} }, true},
		{"other channel", nil, func(s *Settings) { s.ChannelIDs = map[string]bool{"200000000000000009": true} }, false},
		{"thread under allowed channel", func(m *discordgo.Message) { m.ChannelID = "600000000000000006" },
			func(s *Settings) { s.ChannelIDs = map[string]bool{chanID: true} }, true},
		{"thread under other channel", func(m *discordgo.Message) { m.ChannelID = "600000000000000007" },
			func(s *Settings) { s.ChannelIDs = map[string]bool{chanID: true} }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDiscord{parents: map[string]string{"600000000000000006": chanID, "600000000000000007": "200000000000000009"}}
			llm := &fakeLLM{answer: "hi"}
			l, _ := newListener(t, d, llm, tt.cfg)
			l.Handle(context.Background(), msg("<@"+botID+"> hello", tt.mod))
			if got := llm.callCount() == 1; got != tt.want {
				t.Errorf("answered = %v, want %v", got, tt.want)
			}
			if got := len(d.sent) == 1; got != tt.want {
				t.Errorf("sent %d messages", len(d.sent))
			}
		})
	}
}

func TestCooldown(t *testing.T) {
	d := &fakeDiscord{}
	llm := &fakeLLM{answer: "hi"}
	l, _ := newListener(t, d, llm, nil)
	now := time.Now()
	l.now = func() time.Time { return now }

	l.Handle(context.Background(), msg("<@"+botID+"> one", nil))
	l.Handle(context.Background(), msg("<@"+botID+"> two", nil)) // inside cooldown: dropped
	other := msg("<@"+botID+"> from bob", func(m *discordgo.Message) { m.Author = &discordgo.User{ID: "300000000000000009", Username: "bob"} })
	l.Handle(context.Background(), other) // another user is not affected
	if llm.callCount() != 2 {
		t.Fatalf("calls = %d, want 2", llm.callCount())
	}
	now = now.Add(11 * time.Second)
	l.Handle(context.Background(), msg("<@"+botID+"> three", nil))
	if llm.callCount() != 3 {
		t.Errorf("after the cooldown: calls = %d, want 3", llm.callCount())
	}
	if len(d.reactions) != 0 {
		t.Errorf("a dropped trigger must get no reaction: %v", d.reactions)
	}
}

func TestRequestBuilding(t *testing.T) {
	older := msg("how do I point my domain?", func(m *discordgo.Message) {
		m.Mentions = nil
		m.Member = &discordgo.Member{Nick: "Ally"}
	})
	botMsg := msg("Use an A record.", func(m *discordgo.Message) {
		m.Author = &discordgo.User{ID: botID, Username: "bot", Bot: true}
		m.Mentions = nil
	})
	other := msg("ping <@"+userID+"> see this", func(m *discordgo.Message) {
		m.Author = &discordgo.User{ID: "300000000000000009", Username: "bob", GlobalName: "Bobby"}
		m.Mentions = nil
		m.Attachments = []*discordgo.MessageAttachment{{Filename: "dns.png"}}
	})
	empty := msg("", func(m *discordgo.Message) { m.Mentions = nil })
	trigger := msg("<@"+botID+">  <@"+botID+"> and the TTL? ask <@"+botID+">", func(m *discordgo.Message) {
		m.Member = &discordgo.Member{Nick: "Ally"}
	})
	// History comes back newest first, as Discord returns it.
	d := &fakeDiscord{history: []*discordgo.Message{empty, other, botMsg, older}}
	llm := &fakeLLM{answer: "Use 3600."}
	l, _ := newListener(t, d, llm, nil)
	l.Handle(context.Background(), trigger)

	if llm.callCount() != 1 {
		t.Fatal("no LLM call")
	}
	got := llm.calls[0]
	want := []Message{
		{"system", "SYSTEM"},
		{"user", "Ally: how do I point my domain?"},
		{"assistant", "Use an A record."},
		{"user", "Bobby: ping @Alice see this\n[attachment: dns.png]"},
		{"user", "Ally: and the TTL? ask @Bot"},
	}
	if len(got) != len(want) {
		t.Fatalf("messages:\n%v\nwant\n%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestMentionOnlyTrigger(t *testing.T) {
	d := &fakeDiscord{}
	llm := &fakeLLM{answer: "Hi!"}
	l, _ := newListener(t, d, llm, func(s *Settings) { s.ContextMessages = 0 })
	l.Handle(context.Background(), msg("<@!"+botID+">", nil))
	last := llm.calls[0][len(llm.calls[0])-1]
	if last.Content != "alice: (mentioned you without any text)" {
		t.Errorf("trigger = %q", last.Content)
	}
}

func TestReplyChunksAndAllowedMentions(t *testing.T) {
	long := strings.Repeat("a", 1500) + "\n\n" + strings.Repeat("b", 1500) + "\n\n@everyone <@&5> <@1> " + strings.Repeat("c", 100)
	d := &fakeDiscord{}
	l, _ := newListener(t, d, &fakeLLM{answer: long}, nil)
	trigger := msg("<@"+botID+"> write a lot", nil)
	l.Handle(context.Background(), trigger)

	if len(d.sent) != 2 {
		t.Fatalf("sent %d chunks, want 2", len(d.sent))
	}
	for i, s := range d.sent {
		if n := len([]rune(s.msg.Content)); n > maxMessageLength {
			t.Errorf("chunk %d is %d characters", i, n)
		}
		am := s.msg.AllowedMentions
		if am == nil || am.Parse == nil || len(am.Parse) != 0 || len(am.Roles) != 0 || len(am.Users) != 0 {
			t.Errorf("chunk %d allowed mentions = %+v", i, am)
		}
		b, _ := json.Marshal(s.msg)
		if !strings.Contains(string(b), `"parse":[]`) {
			t.Errorf("chunk %d JSON must carry an explicit empty parse list: %s", i, b)
		}
		if (s.msg.Reference != nil) != (i == 0) {
			t.Errorf("chunk %d reference = %v", i, s.msg.Reference)
		}
		if am.RepliedUser != (i == 0) {
			t.Errorf("chunk %d replied_user = %v", i, am.RepliedUser)
		}
	}
	ref := d.sent[0].msg.Reference
	if ref.MessageID != trigger.ID || ref.ChannelID != chanID || ref.FailIfNotExists == nil || *ref.FailIfNotExists {
		t.Errorf("reference = %+v", ref)
	}
	if !strings.Contains(d.sent[1].msg.Content, "@everyone") {
		t.Error("@everyone text should arrive as inert text")
	}
	if d.typing == 0 {
		t.Error("typing indicator never started")
	}
}

func TestSplitMessage(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		limit int
		want  []string
	}{
		{"short", "hello", 10, []string{"hello"}},
		{"paragraph boundary", "aaaa\n\nbbbb", 8, []string{"aaaa", "bbbb"}},
		{"line boundary", "aaaa\nbbbb cc", 8, []string{"aaaa", "bbbb cc"}},
		{"space boundary", "aaa bbb ccc", 8, []string{"aaa bbb", "ccc"}},
		{"hard split", "abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"multibyte runes", "ééééé", 2, []string{"éé", "éé", "é"}},
		{"empty", "  ", 5, nil},
	}
	for _, tt := range tests {
		got := splitMessage(tt.text, tt.limit)
		if strings.Join(got, "|") != strings.Join(tt.want, "|") || len(got) != len(tt.want) {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestFailurePath(t *testing.T) {
	for name, llm := range map[string]*fakeLLM{
		"error": {err: errors.New("LLM call timed out after 1s")},
		"empty": {err: ErrEmptyAnswer},
	} {
		t.Run(name, func(t *testing.T) {
			d := &fakeDiscord{}
			l, logs := newListener(t, d, llm, nil)
			trigger := msg("<@"+botID+"> hi", nil)
			l.Handle(context.Background(), trigger)
			if len(d.sent) != 0 {
				t.Errorf("posted %d messages on failure", len(d.sent))
			}
			if len(d.reactions) != 1 || d.reactions[0] != trigger.ID+" "+WarnEmoji {
				t.Errorf("reactions = %v", d.reactions)
			}
			if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), trigger.ID) {
				t.Errorf("log:\n%s", logs.String())
			}
		})
	}
}

func TestLogsHoldNoContent(t *testing.T) {
	d := &fakeDiscord{}
	l, logs := newListener(t, d, &fakeLLM{answer: "the secret answer"}, nil)
	l.Handle(context.Background(), msg("<@"+botID+"> my private question", nil))
	if strings.Contains(logs.String(), "private question") || strings.Contains(logs.String(), "secret answer") {
		t.Errorf("message content in logs:\n%s", logs.String())
	}
}

func TestConcurrencyCap(t *testing.T) {
	d := &fakeDiscord{}
	llm := &fakeLLM{answer: "ok", block: make(chan struct{})}
	l, _ := newListener(t, d, llm, func(s *Settings) {
		s.MaxConcurrent = 2
		s.UserCooldown = 0
		s.SlotWait = 5 * time.Second
	})
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Handle(context.Background(), msg("<@"+botID+"> q", nil))
		}()
	}
	time.Sleep(200 * time.Millisecond)
	if n := llm.inFlight.Load(); n != 2 {
		t.Errorf("in flight = %d, want 2", n)
	}
	close(llm.block)
	wg.Wait()
	if llm.maxSeen.Load() != 2 || llm.callCount() != 5 {
		t.Errorf("max in flight %d, calls %d", llm.maxSeen.Load(), llm.callCount())
	}
}

func TestSlotWaitTimeout(t *testing.T) {
	d := &fakeDiscord{}
	llm := &fakeLLM{answer: "ok", block: make(chan struct{})}
	l, _ := newListener(t, d, llm, func(s *Settings) {
		s.MaxConcurrent = 1
		s.UserCooldown = 0
		s.SlotWait = 50 * time.Millisecond
	})
	go l.Handle(context.Background(), msg("<@"+botID+"> first", nil))
	time.Sleep(20 * time.Millisecond)
	second := msg("<@"+botID+"> second", nil)
	l.Handle(context.Background(), second) // waits 50ms, then fails
	close(llm.block)
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.reactions) != 1 || !strings.HasPrefix(d.reactions[0], second.ID) {
		t.Errorf("reactions = %v", d.reactions)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	d := &fakeDiscord{}
	l, logs := newListener(t, d, &fakeLLM{answer: "ok"}, nil)
	l.render = func(*discordgo.Message, string) string { panic("boom") }
	l.Handle(context.Background(), msg("<@"+botID+"> hi", nil))
	if !strings.Contains(logs.String(), "panicked") {
		t.Errorf("panic not logged:\n%s", logs.String())
	}
}

func TestShutdown(t *testing.T) {
	d := &fakeDiscord{}
	llm := &fakeLLM{answer: "ok", block: make(chan struct{})}
	l, _ := newListener(t, d, llm, func(s *Settings) { s.UserCooldown = 0 })
	go l.Handle(context.Background(), msg("<@"+botID+"> in flight", nil))
	time.Sleep(50 * time.Millisecond)
	if l.Shutdown(50 * time.Millisecond) {
		t.Error("Shutdown reported done while an answer was in flight")
	}
	l.Handle(context.Background(), msg("<@"+botID+"> after shutdown", nil))
	close(llm.block)
	if !l.Shutdown(time.Second) {
		t.Error("in-flight answer did not finish")
	}
	if llm.callCount() != 1 || len(d.sent) != 1 {
		t.Errorf("calls %d, sent %d: a trigger after shutdown was handled", llm.callCount(), len(d.sent))
	}
}

func TestMessageContentWarning(t *testing.T) {
	blank := func() *discordgo.Message { return msg("", func(m *discordgo.Message) { m.Mentions = nil }) }
	d := &fakeDiscord{history: []*discordgo.Message{blank(), blank()}}
	l, logs := newListener(t, d, &fakeLLM{answer: "ok"}, func(s *Settings) { s.UserCooldown = 0 })
	l.Handle(context.Background(), msg("<@"+botID+"> a", nil))
	l.Handle(context.Background(), msg("<@"+botID+"> b", nil))
	if n := strings.Count(logs.String(), "Message Content Intent"); n != 1 {
		t.Errorf("warning logged %d times, want once:\n%s", n, logs.String())
	}

	d2 := &fakeDiscord{history: []*discordgo.Message{msg("normal text", func(m *discordgo.Message) { m.Mentions = nil })}}
	l2, logs2 := newListener(t, d2, &fakeLLM{answer: "ok"}, nil)
	l2.Handle(context.Background(), msg("<@"+botID+"> a", nil))
	if strings.Contains(logs2.String(), "Message Content Intent") {
		t.Error("warning without the symptom")
	}
}

func TestStripLeadingMention(t *testing.T) {
	for in, want := range map[string]string{
		"<@" + botID + "> hi":                     "hi",
		"<@!" + botID + ">, hi":                   "hi",
		"  <@" + botID + "> <@" + botID + ">: hi": "hi",
		"hi <@" + botID + ">":                     "hi <@" + botID + ">",
		"<@" + userID + "> hi":                    "<@" + userID + "> hi",
	} {
		if got := stripLeadingMention(in, botID); got != want {
			t.Errorf("stripLeadingMention(%q) = %q, want %q", in, got, want)
		}
	}
}
