package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func (t *Toolset) threadTools() []Tool {
	return []Tool{
		{
			Name:        "list_threads",
			Description: "List the threads of one channel (text, news, forum or media): active threads plus up to 100 recent archived public threads. A returned thread ID can be used as channelId in read_messages and send_message. Calling this also makes archived threads of the channel usable in get_thread_info and the message tools",
			Params:      []Param{req("channelId", "ID of the parent channel that contains the threads")},
			Handler:     t.listThreads,
		},
		{
			Name:        "get_thread_info",
			Description: "Get detailed information about a thread: parent channel, owner, archived/locked state, creation time, message and member counts. Archived threads are found only after list_threads has been called on their parent channel",
			Params:      []Param{req("threadId", "Thread ID")},
			Handler:     t.getThreadInfo,
		},
		{
			Name:        "list_active_threads",
			Description: "List all active threads in the server",
			Params:      []Param{opt("guildId", "Discord server ID")},
			Handler:     t.listActiveThreads,
		},
	}
}

// threadContainerTypes are the channel types that can contain threads.
var threadContainerTypes = []discordgo.ChannelType{
	discordgo.ChannelTypeGuildText,
	discordgo.ChannelTypeGuildNews,
	discordgo.ChannelTypeGuildForum,
	discordgo.ChannelTypeGuildMedia,
}

// threadTypeName names a thread's channel type.
func threadTypeName(ty discordgo.ChannelType) string {
	switch ty {
	case discordgo.ChannelTypeGuildPublicThread:
		return "GUILD_PUBLIC_THREAD"
	case discordgo.ChannelTypeGuildPrivateThread:
		return "GUILD_PRIVATE_THREAD"
	case discordgo.ChannelTypeGuildNewsThread:
		return "GUILD_NEWS_THREAD"
	default:
		return "UNKNOWN"
	}
}

func threadArchived(c *discordgo.Channel) bool {
	return c.ThreadMetadata != nil && c.ThreadMetadata.Archived
}

func threadLocked(c *discordgo.Channel) bool {
	return c.ThreadMetadata != nil && c.ThreadMetadata.Locked
}

// threadJumpURL is the link to a thread.
func threadJumpURL(c *discordgo.Channel) string {
	return "https://discord.com/channels/" + c.GuildID + "/" + c.ID
}

// threadCache adds threads fetched from the API to the state cache, so later
// lookups by ID find them.
func (t *Toolset) threadCache(threads []*discordgo.Channel, guildID string) {
	for _, th := range threads {
		if th.GuildID == "" {
			th.GuildID = guildID
		}
		_ = t.s.State.ChannelAdd(th)
	}
}

// threadCanReadHistory reports whether the bot may read message history in
// channelID. It assumes it may when the permissions cannot be worked out
// from the cache.
func (t *Toolset) threadCanReadHistory(channelID string) bool {
	if t.s.State.User == nil {
		return true
	}
	perms, err := t.s.State.UserChannelPermissions(t.s.State.User.ID, channelID)
	if err != nil {
		return true
	}
	return perms&discordgo.PermissionReadMessageHistory != 0
}

const threadArchivedNote = "\n(Archived threads were not listed: the bot lacks the Read History permission)"

func (t *Toolset) listThreads(_ context.Context, a Args) (string, error) {
	channelID := a.Get("channelId")
	if channelID == "" {
		return "", errors.New("channelId cannot be null")
	}
	container := t.channelOfType(channelID, threadContainerTypes...)
	if container == nil || container.GuildID == "" {
		return "", errors.New("Channel not found by channelId or it cannot contain threads")
	}
	g := t.guildSnapshot(container.GuildID)
	if g == nil {
		return "", errors.New("Channel not found by channelId or it cannot contain threads")
	}

	var threads []*discordgo.Channel
	seen := map[string]bool{}
	for _, th := range t.guildChannels(g, threadTypes...) {
		if th.ParentID == container.ID {
			threads = append(threads, th)
			seen[th.ID] = true
		}
	}

	archivedNote := ""
	if !t.threadCanReadHistory(container.ID) {
		archivedNote = threadArchivedNote
	} else {
		list, err := t.s.ThreadsArchived(container.ID, nil, 100)
		switch {
		case err != nil && forumIsMissingPermissions(err):
			archivedNote = threadArchivedNote
		case err != nil:
			return "", err
		case list != nil:
			t.threadCache(list.Threads, container.GuildID)
			for _, th := range list.Threads {
				if !seen[th.ID] {
					threads = append(threads, th)
					seen[th.ID] = true
				}
			}
		}
	}

	if len(threads) == 0 {
		return "No threads found in channel " + container.Name + " (ID: " + channelID + ")." + archivedNote, nil
	}
	lines := make([]string, 0, len(threads))
	for _, th := range threads {
		lines = append(lines, fmt.Sprintf("- **%s** (ID: %s)\n"+
			"  • Archived: %s\n"+
			"  • Locked: %s\n"+
			"  • Created: %s\n"+
			"  • Messages: %d\n"+
			"  • Jump URL: %s",
			th.Name, th.ID, boolText(threadArchived(th)), boolText(threadLocked(th)),
			formatTimestamp(snowflakeTime(th.ID)), th.MessageCount, threadJumpURL(th)))
	}
	return fmt.Sprintf("Retrieved %d threads in #%s:\n%s%s", len(threads), container.Name, strings.Join(lines, "\n"), archivedNote), nil
}

func (t *Toolset) getThreadInfo(_ context.Context, a Args) (string, error) {
	threadID := a.Get("threadId")
	if threadID == "" {
		return "", errors.New("threadId cannot be null")
	}
	thread := t.channelOfType(threadID, threadTypes...)
	if thread == nil {
		return "", errors.New("Thread not found by threadId. If it is archived, call list_threads on its parent channel first")
	}

	owner := "unknown (ID: " + thread.OwnerID + ")"
	if m := t.cachedMember(thread.GuildID, thread.OwnerID); m != nil && m.User != nil {
		owner = m.User.Username + " (ID: " + thread.OwnerID + ")"
	}
	parentName := "unknown"
	if p := t.cachedChannel(thread.ParentID); p != nil {
		parentName = p.Name
	}

	return fmt.Sprintf("Thread: **%s** (ID: %s)\n"+
		"• Type: %s\n"+
		"• Parent channel: #%s (ID: %s)\n"+
		"• Owner: %s\n"+
		"• Created: %s\n"+
		"• Archived: %s\n"+
		"• Locked: %s\n"+
		"• Messages: %d\n"+
		"• Members: %d\n"+
		"• Jump URL: %s",
		thread.Name, thread.ID, threadTypeName(thread.Type), parentName, thread.ParentID, owner,
		formatTimestamp(snowflakeTime(thread.ID)), boolText(threadArchived(thread)), boolText(threadLocked(thread)),
		thread.MessageCount, thread.MemberCount, threadJumpURL(thread)), nil
}

func (t *Toolset) listActiveThreads(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	list, err := t.s.GuildThreadsActive(g.ID)
	if err != nil {
		return "", err
	}
	t.threadCache(list.Threads, g.ID)
	if len(list.Threads) == 0 {
		return "No active threads found in the server.", nil
	}
	lines := make([]string, 0, len(list.Threads))
	for _, th := range list.Threads {
		parentName := "unknown"
		if p := t.cachedChannel(th.ParentID); p != nil {
			parentName = p.Name
		}
		archived := ""
		if threadArchived(th) {
			archived = " (archived)"
		}
		lines = append(lines, "- "+th.Name+" (ID: "+th.ID+") in #"+parentName+archived)
	}
	return fmt.Sprintf("Retrieved %d active threads:\n%s", len(list.Threads), strings.Join(lines, "\n")), nil
}
