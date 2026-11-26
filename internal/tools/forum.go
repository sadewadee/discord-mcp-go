package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func (t *Toolset) forumTools() []Tool {
	return []Tool{
		{
			Name:        "create_forum_channel",
			Description: "Create a new forum channel",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("name", "Channel name"),
				opt("categoryId", "Category ID"),
				opt("topic", "Default post guidelines / topic"),
				opt("nsfw", "Whether channel is NSFW"),
				opt("slowmode", "Default slowmode for new posts in seconds (0-21600)"),
				opt("position", "Position in channel list"),
			},
			Handler: t.createForumChannel,
		},
		{
			Name:        "edit_forum_channel",
			Description: "Edit settings of a forum channel (name, topic, nsfw, slowmode, category, position, default sort, default layout)",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("channelId", "Forum channel ID"),
				opt("name", "New channel name"),
				opt("topic", "New channel topic / post guidelines"),
				opt("nsfw", "Whether channel is NSFW"),
				opt("slowmode", "Default slowmode for new posts in seconds (0-21600)"),
				opt("categoryId", "Category ID (empty to remove from category)"),
				opt("position", "Position in channel list"),
				opt("defaultSort", "Default sort order: RECENT_ACTIVITY or CREATION_TIME"),
				opt("defaultLayout", "Default layout: LIST_VIEW or GALLERY_VIEW"),
				opt("reason", "Reason for audit log"),
			},
			Handler: t.editForumChannel,
		},
		{
			Name:        "list_forum_channels",
			Description: "List all forum channels in the server",
			Params:      []Param{opt("guildId", "Discord server ID")},
			Handler:     t.listForumChannels,
		},
		{
			Name:        "get_forum_channel_info",
			Description: "Get detailed information about a forum channel including tags and settings",
			Params:      []Param{opt("guildId", "Discord server ID"), req("channelId", "Forum channel ID")},
			Handler:     t.getForumChannelInfo,
		},
		{
			Name:        "list_forum_tags",
			Description: "List all available tags in a forum channel",
			Params:      []Param{opt("guildId", "Discord server ID"), req("channelId", "Forum channel ID")},
			Handler:     t.listForumTags,
		},
		{
			Name:        "create_forum_post",
			Description: "Create a new forum post (thread) with an initial message in a forum channel",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("channelId", "Forum channel ID"),
				req("title", "Post title"),
				req("message", "Content of the initial message"),
				opt("tagIds", "Comma-separated tag IDs to apply"),
			},
			Handler: t.createForumPost,
		},
		{
			Name:        "list_forum_posts",
			Description: "List active posts (threads) in a forum channel",
			Params:      []Param{opt("guildId", "Discord server ID"), req("channelId", "Forum channel ID")},
			Handler:     t.listForumPosts,
		},
		{
			Name:        "modify_forum_post",
			Description: "Modify a forum post: lock/unlock, archive/unarchive, pin/unpin, or change applied tags",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("postId", "Forum post (thread) ID"),
				opt("locked", "Lock state (true/false)"),
				opt("archived", "Archive state (true/false)"),
				opt("pinned", "Pin state (true/false)"),
				opt("tagIds", "Comma-separated tag IDs to set (empty string to clear)"),
				opt("reason", "Reason for audit log"),
			},
			Handler: t.modifyForumPost,
		},
	}
}

// forumParseInt parses a whole-number argument. A value that is not a valid
// 32-bit integer is rejected with `For input string: "<value>"`.
func forumParseInt(s string) (int, error) {
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("For input string: \"%s\"", s)
	}
	return int(n), nil
}

// forumParseBool is true only for "true" in any letter case; every other
// value is false.
func forumParseBool(s string) bool { return strings.EqualFold(s, "true") }

// forumSplitIDs splits a comma-separated ID list, trimming entries and
// dropping empty ones. The result is never nil, so it encodes as [].
func forumSplitIDs(s string) []string {
	ids := []string{}
	for _, raw := range strings.Split(s, ",") {
		if id := strings.TrimSpace(raw); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// forumRequest sends a JSON request to the Discord API, with an audit log
// reason when one is given, and decodes the response into out (if not nil).
func (t *Toolset) forumRequest(method, endpoint string, body any, reason string, out any) error {
	var opts []discordgo.RequestOption
	if reason != "" {
		opts = append(opts, discordgo.WithAuditLogReason(reason))
	}
	resp, err := t.s.RequestWithBucketID(method, endpoint, body, endpoint, opts...)
	if err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(resp, out)
	}
	return nil
}

// forumIsMissingPermissions reports whether err is Discord's "Missing
// Permissions" or "Missing Access" API error.
func forumIsMissingPermissions(err error) bool {
	var rest *discordgo.RESTError
	if errors.As(err, &rest) && rest.Message != nil {
		return rest.Message.Code == discordgo.ErrCodeMissingPermissions || rest.Message.Code == discordgo.ErrCodeMissingAccess
	}
	return false
}

// forumChannel returns the forum channel channelID of guild g.
func (t *Toolset) forumChannel(g *discordgo.Guild, channelID string) (*discordgo.Channel, error) {
	if channelID == "" {
		return nil, errors.New("channelId cannot be null")
	}
	f := t.guildChannelOfType(g, channelID, discordgo.ChannelTypeGuildForum)
	if f == nil {
		return nil, errors.New("Channel not found or is not a forum channel")
	}
	return f, nil
}

// forumCategory returns the category categoryID of guild g.
func (t *Toolset) forumCategory(g *discordgo.Guild, categoryID string) (*discordgo.Channel, error) {
	c := t.guildChannelOfType(g, categoryID, discordgo.ChannelTypeGuildCategory)
	if c == nil {
		return nil, errors.New("Category not found by categoryId")
	}
	return c, nil
}

// forumThreads returns the cached threads whose parent is channelID.
func (t *Toolset) forumThreads(g *discordgo.Guild, channelID string) []*discordgo.Channel {
	var out []*discordgo.Channel
	for _, th := range t.guildChannels(g, threadTypes...) {
		if th.ParentID == channelID {
			out = append(out, th)
		}
	}
	return out
}

// forumSortOrderName names a forum's default sort order.
func forumSortOrderName(o *discordgo.ForumSortOrderType) string {
	if o == nil {
		return "UNKNOWN"
	}
	switch *o {
	case discordgo.ForumSortOrderLatestActivity:
		return "RECENT_ACTIVITY"
	case discordgo.ForumSortOrderCreationDate:
		return "CREATION_TIME"
	default:
		return "UNKNOWN"
	}
}

// forumLayoutName names a forum's default layout.
func forumLayoutName(l discordgo.ForumLayout) string {
	switch l {
	case discordgo.ForumLayoutNotSet:
		return "DEFAULT_VIEW"
	case discordgo.ForumLayoutListView:
		return "LIST_VIEW"
	case discordgo.ForumLayoutGalleryView:
		return "GALLERY_VIEW"
	default:
		return "UNKNOWN"
	}
}

// forumParseSort parses a default sort order name, ignoring case.
func forumParseSort(s string) (discordgo.ForumSortOrderType, bool) {
	switch strings.ToUpper(s) {
	case "RECENT_ACTIVITY":
		return discordgo.ForumSortOrderLatestActivity, true
	case "CREATION_TIME":
		return discordgo.ForumSortOrderCreationDate, true
	}
	return 0, false
}

// forumParseLayout parses a default layout name, ignoring case.
func forumParseLayout(s string) (discordgo.ForumLayout, bool) {
	switch strings.ToUpper(s) {
	case "DEFAULT_VIEW":
		return discordgo.ForumLayoutNotSet, true
	case "LIST_VIEW":
		return discordgo.ForumLayoutListView, true
	case "GALLERY_VIEW":
		return discordgo.ForumLayoutGalleryView, true
	}
	return 0, false
}

// forumTagEmoji renders a tag's emoji: a custom emoji as "<:name:id>", a
// Unicode emoji as itself, and "" when the tag has none.
func forumTagEmoji(tag discordgo.ForumTag) string {
	switch {
	case tag.EmojiID != "":
		return "<:" + tag.EmojiName + ":" + tag.EmojiID + ">"
	case tag.EmojiName != "":
		return tag.EmojiName
	}
	return ""
}

// forumTagLine renders "name (ID: id)", plus " [moderated]" when only
// moderators may apply the tag.
func forumTagLine(tag discordgo.ForumTag) string {
	moderated := ""
	if tag.Moderated {
		moderated = " [moderated]"
	}
	return tag.Name + " (ID: " + tag.ID + ")" + moderated
}

func (t *Toolset) createForumChannel(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	name := a.Get("name")
	if name == "" {
		return "", errors.New("name cannot be null")
	}
	body := map[string]any{"name": name, "type": discordgo.ChannelTypeGuildForum}
	if id := a.Get("categoryId"); id != "" {
		c, err := t.forumCategory(g, id)
		if err != nil {
			return "", err
		}
		body["parent_id"] = c.ID
	}
	if v := a.Get("topic"); v != "" {
		body["topic"] = v
	}
	if v := a.Get("nsfw"); v != "" {
		body["nsfw"] = forumParseBool(v)
	}
	if v := a.Get("slowmode"); v != "" {
		n, err := forumParseInt(v)
		if err != nil {
			return "", err
		}
		if err := checkSlowmode(n); err != nil {
			return "", err
		}
		body["rate_limit_per_user"] = n
	}
	if v := a.Get("position"); v != "" {
		n, err := forumParseInt(v)
		if err != nil {
			return "", err
		}
		if n < 0 {
			return "", errors.New("Position must be >= 0!")
		}
		body["position"] = n
	}
	var created discordgo.Channel
	if err := t.forumRequest("POST", discordgo.EndpointGuildChannels(g.ID), body, "", &created); err != nil {
		return "", err
	}
	return "Created forum channel: " + created.Name + " (ID: " + created.ID + ")", nil
}

func (t *Toolset) editForumChannel(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	forum, err := t.forumChannel(g, a.Get("channelId"))
	if err != nil {
		return "", err
	}
	hasEditable := a.Get("name") != "" || a.Has("topic") || a.Get("nsfw") != "" ||
		a.Get("slowmode") != "" || a.Has("categoryId") || a.Get("position") != "" ||
		a.Get("defaultSort") != "" || a.Get("defaultLayout") != ""
	if !hasEditable {
		return "", errors.New("At least one forum channel field must be provided")
	}

	body := map[string]any{}
	if v := a.Get("name"); v != "" {
		body["name"] = v
	}
	if a.Has("topic") {
		body["topic"] = a.Get("topic")
	}
	if v := a.Get("nsfw"); v != "" {
		body["nsfw"] = forumParseBool(v)
	}
	if v := a.Get("slowmode"); v != "" {
		n, err := forumParseInt(v)
		if err != nil {
			return "", err
		}
		if err := checkSlowmodeEdit(n); err != nil {
			return "", err
		}
		body["rate_limit_per_user"] = n
	}
	if a.Has("categoryId") {
		if id := a.Get("categoryId"); id == "" {
			body["parent_id"] = nil
		} else {
			c, err := t.forumCategory(g, id)
			if err != nil {
				return "", err
			}
			body["parent_id"] = c.ID
		}
	}
	if v := a.Get("position"); v != "" {
		n, err := forumParseInt(v)
		if err != nil {
			return "", err
		}
		body["position"] = n
	}
	if v := a.Get("defaultSort"); v != "" {
		order, ok := forumParseSort(v)
		if !ok {
			return "", errors.New("Invalid defaultSort. Use RECENT_ACTIVITY or CREATION_TIME")
		}
		body["default_sort_order"] = order
	}
	if v := a.Get("defaultLayout"); v != "" {
		layout, ok := forumParseLayout(v)
		if !ok {
			return "", errors.New("Invalid defaultLayout. Use LIST_VIEW or GALLERY_VIEW")
		}
		body["default_forum_layout"] = layout
	}
	if err := t.forumRequest("PATCH", discordgo.EndpointChannel(forum.ID), body, a.Get("reason"), nil); err != nil {
		return "", err
	}
	return "Updated forum channel: " + forum.Name + " (ID: " + forum.ID + ")", nil
}

func (t *Toolset) listForumChannels(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	forums := t.guildChannels(g, discordgo.ChannelTypeGuildForum)
	if len(forums) == 0 {
		return "No forum channels found in the server.", nil
	}
	lines := make([]string, 0, len(forums))
	for _, f := range forums {
		parent := ""
		if c := t.guildChannelOfType(g, f.ParentID, discordgo.ChannelTypeGuildCategory); c != nil {
			parent = " [" + c.Name + "]"
		}
		lines = append(lines, "- "+f.Name+" (ID: "+f.ID+")"+parent)
	}
	return fmt.Sprintf("Retrieved %d forum channels:\n%s", len(forums), strings.Join(lines, "\n")), nil
}

func (t *Toolset) getForumChannelInfo(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	forum, err := t.forumChannel(g, a.Get("channelId"))
	if err != nil {
		return "", err
	}
	topic := forum.Topic
	if topic == "" {
		topic = "none"
	}
	var b strings.Builder
	b.WriteString("Name: " + forum.Name + "\n")
	b.WriteString("ID: " + forum.ID + "\n")
	b.WriteString("Topic: " + topic + "\n")
	b.WriteString("NSFW: " + boolText(forum.NSFW) + "\n")
	b.WriteString("Slowmode: " + strconv.Itoa(forum.RateLimitPerUser) + "s\n")
	b.WriteString("Position: " + strconv.Itoa(t.displayPosition(g, forum.ID)) + "\n")
	b.WriteString("Default Sort: " + forumSortOrderName(forum.DefaultSortOrder) + "\n")
	b.WriteString("Default Layout: " + forumLayoutName(forum.DefaultForumLayout) + "\n")
	if c := t.guildChannelOfType(g, forum.ParentID, discordgo.ChannelTypeGuildCategory); c != nil {
		b.WriteString("Category: " + c.Name + " (ID: " + c.ID + ")\n")
	}
	none := ""
	if len(forum.AvailableTags) == 0 {
		none = "none"
	}
	b.WriteString("Tags: " + none + "\n")
	for _, tag := range forum.AvailableTags {
		b.WriteString("  - " + forumTagLine(tag) + "\n")
	}
	b.WriteString("Active Posts: " + strconv.Itoa(len(t.forumThreads(g, forum.ID))) + "\n")
	b.WriteString("Created: " + formatDate(snowflakeTime(forum.ID)))
	return b.String(), nil
}

func (t *Toolset) listForumTags(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	forum, err := t.forumChannel(g, a.Get("channelId"))
	if err != nil {
		return "", err
	}
	if len(forum.AvailableTags) == 0 {
		return "No tags configured on forum: " + forum.Name, nil
	}
	lines := make([]string, 0, len(forum.AvailableTags))
	for _, tag := range forum.AvailableTags {
		line := "- " + forumTagLine(tag)
		if e := forumTagEmoji(tag); e != "" {
			line += " " + e
		}
		lines = append(lines, line)
	}
	return fmt.Sprintf("Retrieved %d tags on forum %s:\n%s", len(forum.AvailableTags), forum.Name, strings.Join(lines, "\n")), nil
}

func (t *Toolset) createForumPost(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	forum, err := t.forumChannel(g, a.Get("channelId"))
	if err != nil {
		return "", err
	}
	if err := requireArgs(a, "title", "message"); err != nil {
		return "", err
	}
	start := &discordgo.ThreadStart{Name: a.Get("title")}
	if v := a.Get("tagIds"); v != "" {
		if tags := forumSplitIDs(v); len(tags) > 0 {
			start.AppliedTags = tags
		}
	}
	thread, err := t.s.ForumThreadStartComplex(forum.ID, start, &discordgo.MessageSend{Content: a.Get("message")})
	if err != nil {
		return "", err
	}
	return "Created forum post: " + thread.Name + " (ID: " + thread.ID + ") in forum " + forum.Name, nil
}

func (t *Toolset) listForumPosts(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	forum, err := t.forumChannel(g, a.Get("channelId"))
	if err != nil {
		return "", err
	}
	posts := t.forumThreads(g, forum.ID)
	if len(posts) == 0 {
		return "No active posts found in forum: " + forum.Name, nil
	}
	tagNames := map[string]string{}
	for _, tag := range forum.AvailableTags {
		tagNames[tag.ID] = tag.Name
	}
	lines := make([]string, 0, len(posts))
	for _, p := range posts {
		var names []string
		for _, id := range p.AppliedTags {
			if n, ok := tagNames[id]; ok {
				names = append(names, n)
			}
		}
		tagPart := ""
		if len(names) > 0 {
			tagPart = " [" + strings.Join(names, ", ") + "]"
		}
		locked, archived := "", ""
		if p.ThreadMetadata != nil && p.ThreadMetadata.Locked {
			locked = " (locked)"
		}
		if p.ThreadMetadata != nil && p.ThreadMetadata.Archived {
			archived = " (archived)"
		}
		lines = append(lines, "- "+p.Name+" (ID: "+p.ID+")"+tagPart+locked+archived)
	}
	return fmt.Sprintf("Retrieved %d active posts in forum %s:\n%s", len(posts), forum.Name, strings.Join(lines, "\n")), nil
}

func (t *Toolset) modifyForumPost(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	postID := a.Get("postId")
	if postID == "" {
		return "", errors.New("postId cannot be null")
	}
	// Archived posts are only in the cache after list_threads has loaded them.
	thread := t.guildChannelOfType(g, postID, threadTypes...)
	if thread == nil {
		return "", errors.New("Forum post not found by postId")
	}
	if t.channelOfType(thread.ParentID, discordgo.ChannelTypeGuildForum) == nil {
		return "", errors.New("Thread is not inside a forum channel")
	}
	if !a.Has("locked") && !a.Has("archived") && !a.Has("pinned") && !a.Has("tagIds") {
		return "", errors.New("At least one of locked, archived, pinned, or tagIds must be provided")
	}

	body := map[string]any{}
	if v := a.Get("locked"); v != "" {
		body["locked"] = forumParseBool(v)
	}
	if v := a.Get("archived"); v != "" {
		body["archived"] = forumParseBool(v)
	}
	if v := a.Get("pinned"); v != "" {
		flags := thread.Flags
		if forumParseBool(v) {
			flags |= discordgo.ChannelFlagPinned
		} else {
			flags &^= discordgo.ChannelFlagPinned
		}
		body["flags"] = flags
	}
	if a.Has("tagIds") {
		body["applied_tags"] = forumSplitIDs(a.Get("tagIds"))
	}
	if len(body) > 0 {
		if err := t.forumRequest("PATCH", discordgo.EndpointChannel(thread.ID), body, a.Get("reason"), nil); err != nil {
			return "", err
		}
	}
	return "Updated forum post: " + thread.Name + " (ID: " + thread.ID + ")", nil
}
