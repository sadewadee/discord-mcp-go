package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
)

func (t *Toolset) busTools() []Tool {
	return []Tool{
		{
			Name: "wait_for_messages",
			Description: "Wait until a new message is posted in a channel, thread or forum, then return it (and any that arrive within the next half second), oldest first, " +
				"in the same format as read_messages. Use it instead of polling read_messages. Messages sent through this bot are included. " +
				"With after, messages newer than that ID that already exist are returned at once (for a forum channel, only messages that arrive while waiting count). " +
				"A forum channel ID waits on all of its posts, and each result is grouped by post. When nothing arrives in time the result is \"No new messages\"; that is not an error",
			Params: []Param{
				req("channelId", "Text, announcement or thread channel ID, or a forum channel ID to wait on all of its posts"),
				opt("after", "Message ID; only messages newer than it count. Without it, only messages posted after the call starts count"),
				opt("timeoutSeconds", "How long to wait, 1-50 seconds (default 25)"),
			},
			Handler: t.waitForMessages,
		},
		{
			Name: "create_thread",
			Description: "Create a thread in a text or announcement channel, either from an existing message or standalone. " +
				"The returned thread ID can be used as channelId in send_message and read_messages. For forum channels use create_forum_post",
			Params: []Param{
				req("channelId", "Text or announcement channel ID"),
				req("name", "Thread name (1-100 characters)"),
				opt("messageId", "Start the thread from this message; omit for a standalone thread"),
				opt("private", "true for a private thread (text channels only, not with messageId); default false"),
				opt("autoArchiveMinutes", "Archive after this many minutes of inactivity: 60, 1440, 4320 or 10080 (default 1440)"),
			},
			Handler: t.createThread,
		},
	}
}

// parseWaitTimeout parses timeoutSeconds: 1-50, default 25.
func parseWaitTimeout(raw string) (time.Duration, error) {
	if isBlank(raw) {
		return 25 * time.Second, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, errors.New("timeoutSeconds must be an integer between 1 and 50")
	}
	if n < 1 || n > 50 {
		return 0, errors.New("timeoutSeconds must be between 1 and 50")
	}
	return time.Duration(n) * time.Second, nil
}

// waitChannel returns a channel wait_for_messages accepts: text,
// announcement, thread, forum or media. A thread missing from the cache
// (archived) is fetched.
func (t *Toolset) waitChannel(id string) *discordgo.Channel {
	types := append([]discordgo.ChannelType{
		discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews,
		discordgo.ChannelTypeGuildForum, discordgo.ChannelTypeGuildMedia,
	}, threadTypes...)
	if c := t.channelOfType(id, types...); c != nil {
		return c
	}
	c, err := t.s.Channel(id)
	if err != nil || c == nil || !c.IsThread() {
		return nil
	}
	_ = t.s.State.ChannelAdd(c)
	return c
}

func isForumType(ty discordgo.ChannelType) bool {
	return ty == discordgo.ChannelTypeGuildForum || ty == discordgo.ChannelTypeGuildMedia
}

func (t *Toolset) waitForMessages(ctx context.Context, a Args) (string, error) {
	if err := requireArgs(a, "channelId"); err != nil {
		return "", err
	}
	after := strings.TrimSpace(a.Get("after"))
	if a.Has("after") && after == "" {
		return "", errors.New("after cannot be blank")
	}
	timeout, err := parseWaitTimeout(a.Get("timeoutSeconds"))
	if err != nil {
		return "", err
	}
	ch := t.waitChannel(a.Get("channelId"))
	if ch == nil {
		return "", errChannelNotFound
	}
	forum := isForumType(ch.Type)

	// Subscribe before looking at existing messages, so a message posted in
	// between is not missed.
	w, err := t.hub.subscribe(ch.ID)
	if err != nil {
		return "", err
	}
	defer t.hub.unsubscribe(w)

	if after != "" && !forum {
		existing, err := t.s.ChannelMessages(ch.ID, 100, "", after, "")
		if err != nil {
			return "", err
		}
		if len(existing) > 0 {
			return t.formatWaited(ch, existing, len(existing) == 100), nil
		}
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	seen := map[string]bool{}
	var got []*discordgo.Message
	accept := func(m *discordgo.Message) {
		if seen[m.ID] || (after != "" && !idLess(after, m.ID)) {
			return
		}
		seen[m.ID] = true
		got = append(got, m)
	}

	// Wait for the first message.
	for len(got) == 0 {
		select {
		case m := <-w.ch:
			accept(m)
		case <-timer.C:
			return fmt.Sprintf("No new messages (waited %ds)", int(timeout.Seconds())), nil
		case <-ctx.Done():
			return "", ctx.Err()
		case <-t.hub.done():
			return "", errHubClosed
		}
	}
	// Collect the rest of a burst.
	burst := time.NewTimer(t.burstWindow)
	defer burst.Stop()
	for {
		select {
		case m := <-w.ch:
			accept(m)
		case <-burst.C:
			return t.formatWaited(ch, got, false), nil
		case <-ctx.Done():
			return "", ctx.Err()
		case <-t.hub.done():
			return t.formatWaited(ch, got, false), nil
		}
	}
}

// formatWaited renders messages oldest first in the read_messages line
// format. For a forum channel they are grouped by post.
func (t *Toolset) formatWaited(ch *discordgo.Channel, msgs []*discordgo.Message, full bool) string {
	msgs = append([]*discordgo.Message(nil), msgs...)
	sortOldestFirst(msgs)
	var b strings.Builder
	fmt.Fprintf(&b, "**Received %d new messages:**", len(msgs))
	if !isForumType(ch.Type) {
		for _, line := range t.formatMessages(msgs, ch.GuildID) {
			b.WriteString("\n" + line)
		}
	} else {
		// Group by post, posts in order of their first new message.
		var order []string
		byPost := map[string][]*discordgo.Message{}
		for _, m := range msgs {
			if _, ok := byPost[m.ChannelID]; !ok {
				order = append(order, m.ChannelID)
			}
			byPost[m.ChannelID] = append(byPost[m.ChannelID], m)
		}
		for _, post := range order {
			title := "unknown"
			if c := t.cachedChannel(post); c != nil {
				title = c.Name
			}
			fmt.Fprintf(&b, "\nPost: **%s** (ID: %s)", title, post)
			for _, line := range t.formatMessages(byPost[post], ch.GuildID) {
				b.WriteString("\n" + line)
			}
		}
	}
	if full {
		fmt.Fprintf(&b, "\n(100 messages returned; more may exist. Call again with after=%s)", msgs[len(msgs)-1].ID)
	}
	return b.String()
}

// validAutoArchive lists the thread auto-archive durations Discord accepts.
var validAutoArchive = map[int]bool{60: true, 1440: true, 4320: true, 10080: true}

func (t *Toolset) createThread(_ context.Context, a Args) (string, error) {
	if err := requireArgs(a, "channelId", "name"); err != nil {
		return "", err
	}
	name := strings.TrimSpace(a.Get("name"))
	if n := utf8.RuneCountInString(name); n < 1 || n > 100 {
		return "", errors.New("name must be 1 to 100 characters")
	}
	private := false
	switch v := strings.ToLower(strings.TrimSpace(a.Get("private"))); v {
	case "", "false":
	case "true":
		private = true
	default:
		return "", errors.New("private must be true or false")
	}
	messageID := strings.TrimSpace(a.Get("messageId"))
	if private && messageID != "" {
		return "", errors.New("private threads cannot be started from a message; omit messageId or set private to false")
	}
	archive := 1440
	if v := strings.TrimSpace(a.Get("autoArchiveMinutes")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || !validAutoArchive[n] {
			return "", errors.New("autoArchiveMinutes must be 60, 1440, 4320 or 10080")
		}
		archive = n
	}

	ch := t.cachedChannel(a.Get("channelId"))
	if ch == nil {
		return "", errChannelNotFound
	}
	switch ch.Type {
	case discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews:
	case discordgo.ChannelTypeGuildForum, discordgo.ChannelTypeGuildMedia:
		return "", errors.New("This is a forum channel; use create_forum_post to start a post (a post is a thread)")
	default:
		return "", errors.New("Threads can only be created in text or announcement channels")
	}
	if private && ch.Type == discordgo.ChannelTypeGuildNews {
		return "", errors.New("Announcement channels do not support private threads")
	}

	var thread *discordgo.Channel
	var err error
	if messageID != "" {
		thread, err = t.s.MessageThreadStartComplex(ch.ID, messageID, &discordgo.ThreadStart{Name: name, AutoArchiveDuration: archive})
	} else {
		typ := discordgo.ChannelTypeGuildPublicThread
		switch {
		case private:
			typ = discordgo.ChannelTypeGuildPrivateThread
		case ch.Type == discordgo.ChannelTypeGuildNews:
			typ = discordgo.ChannelTypeGuildNewsThread
		}
		thread, err = t.s.ThreadStartComplex(ch.ID, &discordgo.ThreadStart{
			Name: name, AutoArchiveDuration: archive, Type: typ, Invitable: private,
		})
	}
	if err != nil {
		return "", err
	}
	if thread.GuildID == "" {
		thread.GuildID = ch.GuildID
	}
	_ = t.s.State.ChannelAdd(thread)

	return fmt.Sprintf("Created thread: **%s** (ID: %s)\n"+
		"• Type: %s\n"+
		"• Parent channel: #%s (ID: %s)\n"+
		"• Auto-archive: %d minutes\n"+
		"• Jump URL: %s\n"+
		"Use the thread ID as channelId in send_message and read_messages.",
		thread.Name, thread.ID, threadTypeName(thread.Type), ch.Name, ch.ID, archive, threadJumpURL(thread)), nil
}
