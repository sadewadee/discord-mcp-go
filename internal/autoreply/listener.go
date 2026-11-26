// Package autoreply answers guild messages that mention the bot, using an
// LLM.
//
// The listener only reads the conversation and replies with text. The LLM
// gets no tools and no access to the MCP toolset: channel text is untrusted,
// so the worst a hostile message can cause is a bad text answer. Every reply
// is sent with allowed mentions that ping nobody but the person replied to.
package autoreply

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
)

// Settings configures the listener.
type Settings struct {
	// GuildID, when set, limits answers to that guild.
	GuildID string
	// ChannelIDs, when non-empty, limits answers to these channels. A thread
	// or forum post matches through its parent channel.
	ChannelIDs      map[string]bool
	SystemPrompt    string
	ContextMessages int           // earlier messages sent as context, 0-50
	UserCooldown    time.Duration // minimum gap between answers to one user
	MaxConcurrent   int           // LLM calls in flight at once

	// Defaults are used when zero.
	SlotWait       time.Duration // how long a trigger waits for a free slot; 30s
	TypingInterval time.Duration // typing indicator refresh; 8s
}

// Renderer renders a message's content with mentions shown as names.
type Renderer func(m *discordgo.Message, guildID string) string

// WarnEmoji marks a trigger message that could not be answered.
const WarnEmoji = "⚠️"

// maxMessageLength is Discord's limit for one message.
const maxMessageLength = 2000

// Listener answers mentions of the bot.
type Listener struct {
	cfg     Settings
	discord Discord
	llm     LLM
	render  Renderer
	botID   func() string
	log     *slog.Logger
	now     func() time.Time

	sem chan struct{}

	// life guards closed and wg.Add together, so Shutdown cannot miss an
	// answer that is just starting.
	life   sync.Mutex
	closed bool
	wg     sync.WaitGroup

	mu        sync.Mutex
	lastReply map[string]time.Time // user ID -> last accepted trigger

	contentWarning sync.Once
}

// New returns a listener. botID returns the bot's own user ID.
func New(cfg Settings, discord Discord, llm LLM, render Renderer, botID func() string, log *slog.Logger) *Listener {
	if cfg.MaxConcurrent < 1 {
		cfg.MaxConcurrent = 1
	}
	if cfg.SlotWait == 0 {
		cfg.SlotWait = 30 * time.Second
	}
	if cfg.TypingInterval == 0 {
		cfg.TypingInterval = 8 * time.Second
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Listener{
		cfg: cfg, discord: discord, llm: llm, render: render, botID: botID, log: log,
		now:       time.Now,
		sem:       make(chan struct{}, cfg.MaxConcurrent),
		lastReply: map[string]time.Time{},
	}
}

// OnMessageCreate is the discordgo handler.
func (l *Listener) OnMessageCreate(_ *discordgo.Session, m *discordgo.MessageCreate) {
	if m != nil && m.Message != nil {
		l.Handle(context.Background(), m.Message)
	}
}

// Handle answers m if it is a trigger. It returns when the answer has been
// sent or has failed.
func (l *Listener) Handle(ctx context.Context, m *discordgo.Message) {
	l.life.Lock()
	if l.closed {
		l.life.Unlock()
		return
	}
	l.wg.Add(1)
	l.life.Unlock()
	defer l.wg.Done()
	defer func() {
		if p := recover(); p != nil {
			l.log.Error("auto-reply handler panicked", "channel_id", m.ChannelID, "message_id", m.ID, "panic", fmt.Sprint(p))
		}
	}()
	if !l.isTrigger(m) {
		return
	}
	l.answer(ctx, m)
}

// Shutdown stops accepting triggers and waits up to timeout for answers in
// flight. It reports whether they all finished.
func (l *Listener) Shutdown(timeout time.Duration) bool {
	l.life.Lock()
	l.closed = true
	l.life.Unlock()
	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// isTrigger applies the trigger rules, in order. The cooldown is recorded
// when a trigger is accepted.
func (l *Listener) isTrigger(m *discordgo.Message) bool {
	// 1. Guild messages only: DMs belong to other integrations.
	if m.GuildID == "" {
		return false
	}
	// 2. The configured guild only.
	if l.cfg.GuildID != "" && m.GuildID != l.cfg.GuildID {
		return false
	}
	// 3. Humans only, in normal messages and replies.
	if m.Author == nil || m.Author.Bot || m.WebhookID != "" {
		return false
	}
	if m.Type != discordgo.MessageTypeDefault && m.Type != discordgo.MessageTypeReply {
		return false
	}
	// 4. The bot itself is mentioned (@everyone, @here and roles do not count).
	botID := l.botID()
	if botID == "" || m.Author.ID == botID || !mentions(m, botID) {
		return false
	}
	// 5. Channel filter, threads through their parent.
	if len(l.cfg.ChannelIDs) > 0 && !l.channelAllowed(m.ChannelID) {
		return false
	}
	// 6. Per-user cooldown.
	return l.takeCooldown(m.Author.ID)
}

func mentions(m *discordgo.Message, userID string) bool {
	for _, u := range m.Mentions {
		if u != nil && u.ID == userID {
			return true
		}
	}
	return false
}

func (l *Listener) channelAllowed(channelID string) bool {
	if l.cfg.ChannelIDs[channelID] {
		return true
	}
	parent, err := l.discord.Parent(channelID)
	if err != nil {
		l.log.Warn("auto-reply could not resolve the channel's parent", "channel_id", channelID, "error", err)
		return false
	}
	return parent != "" && l.cfg.ChannelIDs[parent]
}

func (l *Listener) takeCooldown(userID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if last, ok := l.lastReply[userID]; ok && now.Sub(last) < l.cfg.UserCooldown {
		return false
	}
	l.lastReply[userID] = now
	if len(l.lastReply) > 10000 {
		for id, t := range l.lastReply {
			if now.Sub(t) >= l.cfg.UserCooldown {
				delete(l.lastReply, id)
			}
		}
	}
	return true
}

func (l *Listener) answer(ctx context.Context, m *discordgo.Message) {
	start := l.now()
	logArgs := []any{"channel_id", m.ChannelID, "message_id", m.ID}

	// Wait for a free LLM slot.
	select {
	case l.sem <- struct{}{}:
		defer func() { <-l.sem }()
	case <-time.After(l.cfg.SlotWait):
		l.fail(m, errors.New("too many answers in progress"), logArgs)
		return
	case <-ctx.Done():
		return
	}

	stopTyping := l.typing(m.ChannelID)
	defer stopTyping()

	history, err := l.discord.History(m.ChannelID, m.ID, l.cfg.ContextMessages)
	if err != nil {
		// Answer without context rather than not at all.
		l.log.Warn("auto-reply could not fetch context", append(logArgs, "error", err)...)
		history = nil
	}
	l.checkContent(history)
	messages := l.buildMessages(m, history)

	text, err := l.llm.Complete(ctx, messages)
	if err != nil {
		l.fail(m, err, logArgs)
		return
	}
	for i, chunk := range splitMessage(text, maxMessageLength) {
		send := &discordgo.MessageSend{Content: chunk, AllowedMentions: allowedMentions(i == 0)}
		if i == 0 {
			failIfMissing := false
			send.Reference = &discordgo.MessageReference{
				MessageID: m.ID, ChannelID: m.ChannelID, GuildID: m.GuildID, FailIfNotExists: &failIfMissing,
			}
		}
		if err := l.discord.Send(m.ChannelID, send); err != nil {
			l.fail(m, fmt.Errorf("sending the answer: %w", err), logArgs)
			return
		}
	}
	l.log.Info("auto-reply sent", append(logArgs, "duration", l.now().Sub(start).Round(time.Millisecond).String(),
		"context_messages", len(history))...)
}

// allowedMentions pings nobody: no @everyone, @here, roles or users. Only
// the first chunk, the actual reply, notifies the person replied to.
func allowedMentions(reply bool) *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{
		Parse:       []discordgo.AllowedMentionType{},
		RepliedUser: reply,
	}
}

// fail logs the error and marks the trigger with a reaction. Nothing is
// posted in the channel.
func (l *Listener) fail(m *discordgo.Message, err error, logArgs []any) {
	l.log.Error("auto-reply failed", append(logArgs, "error", err)...)
	if rerr := l.discord.React(m.ChannelID, m.ID, WarnEmoji); rerr != nil {
		l.log.Warn("auto-reply could not add the failure reaction", append(logArgs, "error", rerr)...)
	}
}

// typing shows the typing indicator until the returned function is called.
func (l *Listener) typing(channelID string) func() {
	_ = l.discord.Typing(channelID)
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(l.cfg.TypingInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = l.discord.Typing(channelID)
			}
		}
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// checkContent warns once when human messages arrive without content, the
// sign that Message Content Intent is off in the Developer Portal.
func (l *Listener) checkContent(history []*discordgo.Message) {
	for _, h := range history {
		if h.Author == nil || h.Author.Bot || h.WebhookID != "" {
			continue
		}
		if h.Type != discordgo.MessageTypeDefault && h.Type != discordgo.MessageTypeReply {
			continue
		}
		if h.Content == "" && len(h.Attachments) == 0 && len(h.Embeds) == 0 && len(h.StickerItems) == 0 {
			l.contentWarning.Do(func() {
				l.log.Warn("auto-reply context messages arrive without content: enable Message Content Intent " +
					"(Developer Portal -> Bot -> Privileged Gateway Intents) so answers can use the conversation")
			})
			return
		}
	}
}

// buildMessages turns the trigger and its context into the chat request:
// the system prompt, the context oldest first (the bot's own messages as
// assistant, everyone else as user, prefixed with their name), then the
// trigger without the leading bot mention.
func (l *Listener) buildMessages(trigger *discordgo.Message, history []*discordgo.Message) []Message {
	botID := l.botID()
	msgs := []Message{{Role: "system", Content: l.cfg.SystemPrompt}}

	ctxMsgs := append([]*discordgo.Message(nil), history...)
	sort.SliceStable(ctxMsgs, func(i, j int) bool { return idLess(ctxMsgs[i].ID, ctxMsgs[j].ID) })
	for _, h := range ctxMsgs {
		if h.Author == nil || h.ID == trigger.ID {
			continue
		}
		text := l.messageText(h, trigger.GuildID)
		if text == "" {
			continue
		}
		if h.Author.ID == botID {
			msgs = append(msgs, Message{Role: "assistant", Content: text})
			continue
		}
		msgs = append(msgs, Message{Role: "user", Content: displayName(h) + ": " + text})
	}

	stripped := *trigger
	stripped.Content = stripLeadingMention(trigger.Content, botID)
	text := l.messageText(&stripped, trigger.GuildID)
	if text == "" {
		text = "(mentioned you without any text)"
	}
	msgs = append(msgs, Message{Role: "user", Content: displayName(trigger) + ": " + text})
	return msgs
}

// messageText is the rendered content plus a note for each attachment.
func (l *Listener) messageText(m *discordgo.Message, guildID string) string {
	text := strings.TrimSpace(l.render(m, guildID))
	for _, a := range m.Attachments {
		if a == nil {
			continue
		}
		note := "[attachment: " + a.Filename + "]"
		if text == "" {
			text = note
		} else {
			text += "\n" + note
		}
	}
	return text
}

// displayName is the author's server nickname, global name or username.
func displayName(m *discordgo.Message) string {
	if m.Member != nil && m.Member.Nick != "" {
		return m.Member.Nick
	}
	if m.Author.GlobalName != "" {
		return m.Author.GlobalName
	}
	return m.Author.Username
}

// stripLeadingMention removes the bot mentions (and whitespace) at the start
// of the content.
func stripLeadingMention(content, botID string) string {
	re := regexp.MustCompile(`^\s*<@!?` + regexp.QuoteMeta(botID) + `>[\s,:]*`)
	for {
		next := re.ReplaceAllString(content, "")
		if next == content {
			return strings.TrimSpace(content)
		}
		content = next
	}
}

// idLess compares two snowflakes numerically.
func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// splitMessage splits text into chunks of at most limit characters,
// preferring paragraph breaks, then line breaks, then spaces.
func splitMessage(text string, limit int) []string {
	var chunks []string
	rest := []rune(strings.TrimSpace(text))
	for len(rest) > limit {
		window := string(rest[:limit])
		cut := -1
		for _, sep := range []string{"\n\n", "\n", " "} {
			if i := strings.LastIndex(window, sep); i > 0 {
				cut = len([]rune(window[:i]))
				break
			}
		}
		if cut <= 0 {
			cut = limit
		}
		if chunk := strings.TrimSpace(string(rest[:cut])); chunk != "" {
			chunks = append(chunks, chunk)
		}
		rest = []rune(strings.TrimLeft(string(rest[cut:]), " \n"))
	}
	if s := strings.TrimSpace(string(rest)); s != "" {
		chunks = append(chunks, s)
	}
	return chunks
}
