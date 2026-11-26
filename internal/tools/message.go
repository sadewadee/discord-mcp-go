package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

var errChannelNotFound = errors.New("Channel not found by channelId")

func (t *Toolset) messageTools() []Tool {
	return []Tool{
		{
			Name:        "send_message",
			Description: "Send a message to a specific channel",
			Params: []Param{
				req("channelId", "Discord channel ID"),
				req("message", "Message content"),
				opt("replyToMessageId", "Send as a reply to this message in the same channel, without pinging its author"),
			},
			Handler: t.sendMessage,
		},
		{
			Name:        "edit_message",
			Description: "Edit a message from a specific channel",
			Params:      []Param{req("channelId", "Discord channel ID"), req("messageId", "Specific message ID"), req("newMessage", "New message content")},
			Handler:     t.editMessage,
		},
		{
			Name:        "delete_message",
			Description: "Delete a message from a specific channel",
			Params:      []Param{req("channelId", "Discord channel ID"), req("messageId", "Specific message ID")},
			Handler:     t.deleteMessage,
		},
		{
			Name:        "read_messages",
			Description: "Read message history from a specific channel, optionally paginated with before/after/around",
			Params: []Param{
				req("channelId", "Discord channel ID"),
				opt("count", "Number of messages to retrieve (1-100)"),
				opt("before", "Message ID to fetch messages before this message"),
				opt("after", "Message ID to fetch messages after this message"),
				opt("around", "Message ID to fetch messages around this message"),
			},
			Handler: t.readMessages,
		},
		{
			Name:        "search_messages",
			Description: "Search the most recent messages of a channel or thread for text (case-insensitive). Scans up to maxMessages messages from newest to oldest (default 500, max 1000); it is not a server-side search",
			Params: []Param{
				req("channelId", "Discord channel or thread ID"),
				req("searchText", "Text to search for (case-insensitive)"),
				opt("maxMessages", "Maximum number of recent messages to scan (1-1000, default 500)"),
			},
			Handler: t.searchMessages,
		},
		{
			Name:        "add_reaction",
			Description: "Add a reaction (emoji) to a specific message",
			Params:      []Param{req("channelId", "Discord channel ID"), req("messageId", "Discord message ID"), req("emoji", "Emoji (Unicode or string)")},
			Handler:     t.addReaction,
		},
		{
			Name:        "remove_reaction",
			Description: "Remove a specified reaction (emoji) from a message",
			Params:      []Param{req("channelId", "Discord channel ID"), req("messageId", "Discord message ID"), req("emoji", "Emoji (Unicode or string)")},
			Handler:     t.removeReaction,
		},
		{
			Name:        "get_attachment",
			Description: "Get attachment metadata (filename, size, content type, URLs) from a specific message. Returns info only, does not download files.",
			Params: []Param{
				req("channelId", "Discord channel ID"),
				req("messageId", "Discord message ID"),
				opt("attachmentId", "Specific attachment ID (omit to get all attachments)"),
			},
			Handler: t.getAttachment,
		},
		{
			Name:        "get_message",
			Description: "Get a single message by ID, in the same format as read_messages. If the message is a reply, the message it replies to is included",
			Params:      []Param{req("channelId", "Discord channel or thread ID"), req("messageId", "Discord message ID")},
			Handler:     t.getMessage,
		},
		{
			Name:        "get_message_replies",
			Description: "Find replies to a message by scanning the messages posted after it (up to maxMessages, default 500, max 1000). Discord has no replies lookup, so replies outside the scanned window are not found",
			Params: []Param{
				req("channelId", "Discord channel or thread ID"),
				req("messageId", "ID of the message to find replies for"),
				opt("maxMessages", "Maximum number of messages after the target to scan (1-1000, default 500)"),
			},
			Handler: t.getMessageReplies,
		},
	}
}

// requireArgs returns "<name> cannot be null" for the first empty argument.
func requireArgs(a Args, names ...string) error {
	for _, n := range names {
		if a.Get(n) == "" {
			return errors.New(n + " cannot be null")
		}
	}
	return nil
}

// targetMessage validates channelId and messageId and fetches the message.
func (t *Toolset) targetMessage(a Args) (*discordgo.Channel, *discordgo.Message, error) {
	ch := t.messageChannel(a.Get("channelId"))
	if ch == nil {
		return nil, nil, errChannelNotFound
	}
	msg, err := t.s.ChannelMessage(ch.ID, a.Get("messageId"))
	if err != nil {
		return nil, nil, err
	}
	return ch, msg, nil
}

func (t *Toolset) sendMessage(_ context.Context, a Args) (string, error) {
	if err := requireArgs(a, "channelId", "message"); err != nil {
		return "", err
	}
	ch := t.messageChannel(a.Get("channelId"))
	if ch == nil {
		return "", errChannelNotFound
	}
	var sent *discordgo.Message
	var err error
	if replyTo := strings.TrimSpace(a.Get("replyToMessageId")); replyTo != "" {
		failIfMissing := true // an unknown message ID is an error, not a plain send
		sent, err = t.s.ChannelMessageSendComplex(ch.ID, &discordgo.MessageSend{
			Content: a.Get("message"),
			Reference: &discordgo.MessageReference{
				MessageID: replyTo, ChannelID: ch.ID, GuildID: ch.GuildID, FailIfNotExists: &failIfMissing,
			},
			// Mentions in the text work as in a plain message; only the
			// reply itself does not notify the replied-to author.
			AllowedMentions: &discordgo.MessageAllowedMentions{
				Parse: []discordgo.AllowedMentionType{
					discordgo.AllowedMentionTypeUsers, discordgo.AllowedMentionTypeRoles, discordgo.AllowedMentionTypeEveryone,
				},
				RepliedUser: false,
			},
		})
	} else {
		sent, err = t.s.ChannelMessageSend(ch.ID, a.Get("message"))
	}
	if err != nil {
		return "", err
	}
	return "Message sent successfully. Message link: " + jumpURL(ch.GuildID, ch.ID, sent.ID), nil
}

func (t *Toolset) editMessage(_ context.Context, a Args) (string, error) {
	if err := requireArgs(a, "channelId", "messageId", "newMessage"); err != nil {
		return "", err
	}
	ch, msg, err := t.targetMessage(a)
	if err != nil {
		return "", err
	}
	edited, err := t.s.ChannelMessageEdit(ch.ID, msg.ID, a.Get("newMessage"))
	if err != nil {
		return "", err
	}
	return "Message edited successfully. Message link: " + jumpURL(ch.GuildID, ch.ID, edited.ID), nil
}

func (t *Toolset) deleteMessage(_ context.Context, a Args) (string, error) {
	if err := requireArgs(a, "channelId", "messageId"); err != nil {
		return "", err
	}
	ch, msg, err := t.targetMessage(a)
	if err != nil {
		return "", err
	}
	if err := t.s.ChannelMessageDelete(ch.ID, msg.ID); err != nil {
		return "", err
	}
	return "Message deleted successfully", nil
}

func (t *Toolset) readMessages(_ context.Context, a Args) (string, error) {
	if err := requireArgs(a, "channelId"); err != nil {
		return "", err
	}
	limit, err := parseMessageLimit(a.Get("count"))
	if err != nil {
		return "", err
	}
	if err := validateCursors(a); err != nil {
		return "", err
	}
	ch := t.messageChannel(a.Get("channelId"))
	if ch == nil {
		return "", errChannelNotFound
	}
	msgs, err := t.readHistory(ch.ID, limit, a)
	if err != nil {
		return "", err
	}
	return t.retrievedMessages(msgs, ch.GuildID), nil
}

func (t *Toolset) searchMessages(_ context.Context, a Args) (string, error) {
	if err := requireArgs(a, "channelId"); err != nil {
		return "", err
	}
	searchText := a.Get("searchText")
	if isBlank(searchText) {
		return "", errors.New("searchText cannot be null")
	}
	maxToScan, err := parseScanLimit(a.Get("maxMessages"))
	if err != nil {
		return "", err
	}
	ch := t.messageChannel(a.Get("channelId"))
	if ch == nil {
		return "", errChannelNotFound
	}

	needle := strings.ToLower(searchText)
	var matches []*discordgo.Message
	scanned := 0
	reachedStart := false
	before := ""
	for scanned < maxToScan {
		batch, err := t.s.ChannelMessages(ch.ID, min(100, maxToScan-scanned), before, "", "")
		if err != nil {
			return "", err
		}
		if len(batch) == 0 {
			reachedStart = true
			break
		}
		sortNewestFirst(batch)
		for _, m := range batch {
			if strings.Contains(strings.ToLower(t.contentDisplay(m, ch.GuildID)), needle) {
				matches = append(matches, m)
			}
		}
		scanned += len(batch)
		before = batch[len(batch)-1].ID
	}
	// The limit was hit exactly: probe one more message to tell whether anything older exists.
	if !reachedStart {
		probe, err := t.s.ChannelMessages(ch.ID, 1, before, "", "")
		if err != nil {
			return "", err
		}
		reachedStart = len(probe) == 0
	}

	coverage := "older messages were not scanned"
	if reachedStart {
		coverage = "the whole history was scanned"
	}
	if len(matches) == 0 {
		return fmt.Sprintf("No messages found containing \"%s\" (scanned %d messages, %s).", searchText, scanned, coverage), nil
	}
	return fmt.Sprintf("**Found %d messages containing \"%s\" (scanned %d messages, %s):**\n%s",
		len(matches), searchText, scanned, coverage, strings.Join(t.formatMessages(matches, ch.GuildID), "\n")), nil
}

func (t *Toolset) reactionTarget(a Args) (*discordgo.Channel, *discordgo.Message, error) {
	if err := requireArgs(a, "channelId", "messageId", "emoji"); err != nil {
		return nil, nil, err
	}
	return t.targetMessage(a)
}

// reactionEmoji converts the emoji argument to the form the reactions API
// expects. Unicode emoji are sent as-is; a custom emoji may be given as
// "<:name:id>", "<a:name:id>" or "name:id".
func reactionEmoji(emoji string) string {
	e := strings.TrimSuffix(strings.TrimPrefix(emoji, "<"), ">")
	e = strings.TrimPrefix(e, "a:")
	if i := strings.LastIndex(e, ":"); i > 0 {
		if _, err := strconv.ParseUint(e[i+1:], 10, 64); err == nil {
			return strings.TrimPrefix(e, ":")
		}
	}
	return emoji
}

func (t *Toolset) addReaction(_ context.Context, a Args) (string, error) {
	ch, msg, err := t.reactionTarget(a)
	if err != nil {
		return "", err
	}
	if err := t.s.MessageReactionAdd(ch.ID, msg.ID, reactionEmoji(a.Get("emoji"))); err != nil {
		return "", err
	}
	return "Added reaction successfully. Message link: " + jumpURL(ch.GuildID, ch.ID, msg.ID), nil
}

func (t *Toolset) removeReaction(_ context.Context, a Args) (string, error) {
	ch, msg, err := t.reactionTarget(a)
	if err != nil {
		return "", err
	}
	if err := t.s.MessageReactionRemove(ch.ID, msg.ID, reactionEmoji(a.Get("emoji")), "@me"); err != nil {
		return "", err
	}
	return "Removed reaction successfully. Message link: " + jumpURL(ch.GuildID, ch.ID, msg.ID), nil
}

func (t *Toolset) getAttachment(_ context.Context, a Args) (string, error) {
	if err := requireArgs(a, "channelId", "messageId"); err != nil {
		return "", err
	}
	_, msg, err := t.targetMessage(a)
	if err != nil {
		return "", err
	}
	if len(msg.Attachments) == 0 {
		return "This message has no attachments.", nil
	}
	if id := a.Get("attachmentId"); id != "" {
		for _, att := range msg.Attachments {
			if att.ID == id {
				return formatAttachmentDetail(att), nil
			}
		}
		return "", errors.New("Attachment not found by attachmentId")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Found %d attachment(s):**\n", len(msg.Attachments))
	for _, att := range msg.Attachments {
		b.WriteString(formatAttachmentDetail(att))
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String()), nil
}

func (t *Toolset) getMessage(_ context.Context, a Args) (string, error) {
	if err := requireArgs(a, "channelId", "messageId"); err != nil {
		return "", err
	}
	ch, msg, err := t.targetMessage(a)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(t.formatMessage(msg, ch.GuildID))
	b.WriteString("\n  Jump URL: " + jumpURL(ch.GuildID, ch.ID, msg.ID))
	if id := repliedToID(msg); id != "" {
		if msg.ReferencedMessage != nil {
			b.WriteString("\n  Replying to: " + t.formatMessage(msg.ReferencedMessage, ch.GuildID))
		} else {
			b.WriteString("\n  Replying to message " + id + " (not available, it may have been deleted)")
		}
	}
	return b.String(), nil
}

func (t *Toolset) getMessageReplies(_ context.Context, a Args) (string, error) {
	if err := requireArgs(a, "channelId", "messageId"); err != nil {
		return "", err
	}
	maxToScan, err := parseScanLimit(a.Get("maxMessages"))
	if err != nil {
		return "", err
	}
	// Fails with a clear error if the message does not exist in this channel.
	ch, target, err := t.targetMessage(a)
	if err != nil {
		return "", err
	}
	messageID := target.ID

	// A reply is always posted after the message it replies to, so walk forward from the target.
	var replies []*discordgo.Message
	scanned := 0
	reachedLatest := false
	after := messageID
	for {
		batch, err := t.s.ChannelMessages(ch.ID, min(100, maxToScan-scanned), "", after, "")
		if err != nil {
			return "", err
		}
		if len(batch) == 0 {
			reachedLatest = true
			break
		}
		sortOldestFirst(batch)
		for _, m := range batch {
			if repliedToID(m) == messageID {
				replies = append(replies, m)
			}
		}
		scanned += len(batch)
		after = batch[len(batch)-1].ID
		if scanned >= maxToScan {
			break
		}
	}
	// The limit was hit exactly: probe one more message to tell whether anything newer exists.
	if !reachedLatest {
		probe, err := t.s.ChannelMessages(ch.ID, 1, "", after, "")
		if err != nil {
			return "", err
		}
		reachedLatest = len(probe) == 0
	}
	sortOldestFirst(replies)

	coverage := "newer messages were not scanned"
	if reachedLatest {
		coverage = "up to the latest message"
	}
	if len(replies) == 0 {
		return fmt.Sprintf("No replies to message %s found (scanned %d messages after it, %s).", messageID, scanned, coverage), nil
	}
	return fmt.Sprintf("**Found %d replies to message %s (scanned %d messages after it, %s):**\n%s",
		len(replies), messageID, scanned, coverage, strings.Join(t.formatMessages(replies, ch.GuildID), "\n")), nil
}
