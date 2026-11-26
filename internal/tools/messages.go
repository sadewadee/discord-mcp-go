package tools

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

var (
	userMentionRe    = regexp.MustCompile(`<@!?(\d+)>`)
	roleMentionRe    = regexp.MustCompile(`<@&(\d+)>`)
	channelMentionRe = regexp.MustCompile(`<#(\d+)>`)
	emojiMentionRe   = regexp.MustCompile(`<a?:([A-Za-z0-9_~]+):\d+>`)
	commandMentionRe = regexp.MustCompile(`</([-_\p{L}\p{N} ]+):\d+>`)
)

// contentDisplay returns the message content with mentions replaced by
// readable names: users by "@name" (the member's effective name inside a
// guild, otherwise the username), roles by "@role", channels by "#channel",
// custom emoji by ":name:" and slash commands by "/command".
func (t *Toolset) contentDisplay(m *discordgo.Message, guildID string) string {
	content := m.Content
	mentioned := map[string]*discordgo.User{}
	for _, u := range m.Mentions {
		if u != nil {
			mentioned[u.ID] = u
		}
	}
	content = userMentionRe.ReplaceAllStringFunc(content, func(s string) string {
		id := userMentionRe.FindStringSubmatch(s)[1]
		u, ok := mentioned[id]
		if !ok {
			return s
		}
		name := u.Username
		if guildID != "" {
			if mem := t.cachedMember(guildID, id); mem != nil {
				name = memberEffectiveName(mem)
			}
		}
		return "@" + name
	})
	content = emojiMentionRe.ReplaceAllString(content, ":$1:")
	content = channelMentionRe.ReplaceAllStringFunc(content, func(s string) string {
		id := channelMentionRe.FindStringSubmatch(s)[1]
		if c := t.cachedChannel(id); c != nil && c.GuildID != "" {
			return "#" + c.Name
		}
		return s
	})
	if guildID != "" {
		content = roleMentionRe.ReplaceAllStringFunc(content, func(s string) string {
			id := roleMentionRe.FindStringSubmatch(s)[1]
			if r := t.cachedRole(&discordgo.Guild{ID: guildID}, id); r != nil {
				return "@" + r.Name
			}
			return s
		})
	}
	content = commandMentionRe.ReplaceAllString(content, "/$1")
	return content
}

// ContentDisplay renders m's content with mentions shown as names (users as
// "@name", roles as "@role", channels as "#channel", custom emoji as
// ":name:"), using the gateway state cache. guildID may be empty outside a
// guild.
func (t *Toolset) ContentDisplay(m *discordgo.Message, guildID string) string {
	return t.contentDisplay(m, guildID)
}

// repliedToID returns the ID of the message m replies to, or "" when m is not
// a reply. Forwards, thread starters and crossposts also carry a message
// reference but are not replies.
func repliedToID(m *discordgo.Message) string {
	if m.Type != discordgo.MessageTypeReply || m.MessageReference == nil {
		return ""
	}
	return m.MessageReference.MessageID
}

// formatAttachmentSummary renders one attachment on a single line.
func formatAttachmentSummary(a *discordgo.MessageAttachment) string {
	contentType := a.ContentType
	if contentType == "" {
		contentType = "unknown"
	}
	return fmt.Sprintf("(Attachment ID: %s) `%s` (%s, %s) URL: %s",
		a.ID, a.Filename, formatFileSize(a.Size), contentType, a.URL)
}

// formatAttachmentDetail adds the proxy URL to the summary.
func formatAttachmentDetail(a *discordgo.MessageAttachment) string {
	return fmt.Sprintf("- %s\n  Proxy URL: %s", formatAttachmentSummary(a), a.ProxyURL)
}

// formatMessage renders one message in the line format shared by the
// message-reading tools.
func (t *Toolset) formatMessage(m *discordgo.Message, guildID string) string {
	var authorName, authorID string
	if m.Author != nil {
		authorName, authorID = m.Author.Username, m.Author.ID
	}
	replyInfo := ""
	if id := repliedToID(m); id != "" {
		replyInfo = " [Reply to: " + id + "]"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- (ID: %s) **[%s]** (Author ID: %s) `%s`%s: ```%s```",
		m.ID, authorName, authorID, formatTimestamp(snowflakeTime(m.ID)), replyInfo, t.contentDisplay(m, guildID))
	if len(m.Attachments) > 0 {
		b.WriteString("\n  Attachments:")
		for _, a := range m.Attachments {
			b.WriteString("\n    - ")
			b.WriteString(formatAttachmentSummary(a))
		}
	}
	return b.String()
}

// formatMessages renders each message with formatMessage.
func (t *Toolset) formatMessages(msgs []*discordgo.Message, guildID string) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, t.formatMessage(m, guildID))
	}
	return out
}

// parseMessageLimit parses the count argument of the history tools: 1-100,
// default 100.
func parseMessageLimit(count string) (int, error) {
	if isBlank(count) {
		return 100, nil
	}
	limit, err := strconv.Atoi(count)
	if err != nil {
		return 0, errors.New("count must be an integer between 1 and 100")
	}
	if limit < 1 || limit > 100 {
		return 0, errors.New("count must be between 1 and 100")
	}
	return limit, nil
}

// parseScanLimit parses maxMessages for the scanning tools: 1-1000, default 500.
func parseScanLimit(maxMessages string) (int, error) {
	if isBlank(maxMessages) {
		return 500, nil
	}
	limit, err := strconv.Atoi(strings.TrimSpace(maxMessages))
	if err != nil {
		return 0, errors.New("maxMessages must be an integer between 1 and 1000")
	}
	if limit < 1 || limit > 1000 {
		return 0, errors.New("maxMessages must be between 1 and 1000")
	}
	return limit, nil
}

// validateCursors checks the before/after/around arguments: a sent cursor may
// not be blank, and at most one may be given.
func validateCursors(a Args) error {
	for _, name := range []string{"before", "after", "around"} {
		if a.Has(name) && isBlank(a.Get(name)) {
			return errors.New(name + " cannot be blank")
		}
	}
	provided := 0
	for _, name := range []string{"before", "after", "around"} {
		if !isBlank(a.Get(name)) {
			provided++
		}
	}
	if provided > 1 {
		return errors.New("before, after, and around are mutually exclusive; provide only one")
	}
	return nil
}

// readHistory fetches up to limit messages using the cursor in a (if any),
// newest first.
func (t *Toolset) readHistory(channelID string, limit int, a Args) ([]*discordgo.Message, error) {
	msgs, err := t.s.ChannelMessages(channelID, limit, a.Get("before"), a.Get("after"), a.Get("around"))
	if err != nil {
		return nil, err
	}
	sortNewestFirst(msgs)
	return msgs, nil
}

func sortNewestFirst(msgs []*discordgo.Message) {
	sort.SliceStable(msgs, func(i, j int) bool { return idLess(msgs[j].ID, msgs[i].ID) })
}

func sortOldestFirst(msgs []*discordgo.Message) {
	sort.SliceStable(msgs, func(i, j int) bool { return idLess(msgs[i].ID, msgs[j].ID) })
}

// retrievedMessages renders a history result for read_messages and
// read_private_messages.
func (t *Toolset) retrievedMessages(msgs []*discordgo.Message, guildID string) string {
	return "**Retrieved " + strconv.Itoa(len(msgs)) + " messages:** \n" + strings.Join(t.formatMessages(msgs, guildID), "\n")
}
