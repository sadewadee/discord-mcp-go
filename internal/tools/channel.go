package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

var errCategoryNotFound = errors.New("Category not found by categoryId")

func (t *Toolset) channelTools() []Tool {
	return []Tool{
		{
			Name:        "delete_channel",
			Description: "Delete a channel",
			Params:      []Param{opt("guildId", "Discord server ID"), req("channelId", "Discord channel ID"), opt("reason", "Reason for audit log")},
			Handler:     t.deleteChannel,
		},
		{
			Name:        "create_text_channel",
			Description: "Create a new text channel",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("name", "Channel name"),
				opt("categoryId", "Category ID"),
				opt("topic", "Channel topic"),
				opt("nsfw", "Whether channel is NSFW"),
				opt("slowmode", "Slowmode in seconds (0-21600)"),
				opt("position", "Position in channel list"),
			},
			Handler: t.createTextChannel,
		},
		{
			Name:        "find_channel",
			Description: "Find a channel type and ID using name and server ID",
			Params:      []Param{opt("guildId", "Discord server ID"), req("channelName", "Discord channel name")},
			Handler:     t.findChannel,
		},
		{
			Name:        "list_channels",
			Description: "List of all channels",
			Params:      []Param{opt("guildId", "Discord server ID")},
			Handler:     t.listChannels,
		},
		{
			Name:        "edit_text_channel",
			Description: "Edit settings of a text channel (name, topic, nsfw, slowmode, category, position)",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("channelId", "Channel ID"),
				opt("name", "New channel name"),
				opt("topic", "New channel topic"),
				opt("nsfw", "Whether channel is NSFW"),
				opt("slowmode", "Slowmode in seconds (0-21600)"),
				opt("categoryId", "Category ID (empty to remove from category)"),
				opt("position", "Position in channel list"),
				opt("reason", "Reason for audit log"),
			},
			Handler: t.editTextChannel,
		},
		{
			Name:        "get_channel_info",
			Description: "Get detailed information about a channel",
			Params:      []Param{opt("guildId", "Discord server ID"), req("channelId", "Channel ID")},
			Handler:     t.getChannelInfo,
		},
		{
			Name:        "move_channel",
			Description: "Move a channel to another category and/or change its position",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("channelId", "Channel ID"),
				opt("categoryId", "Target category ID (empty to remove from category)"),
				opt("position", "New position in channel list"),
				opt("reason", "Reason for audit log"),
			},
			Handler: t.moveChannel,
		},
	}
}

// channelTypeName names a channel type the way the tools print it, for
// example TEXT, VOICE, CATEGORY or GUILD_PUBLIC_THREAD.
func channelTypeName(ty discordgo.ChannelType) string {
	switch ty {
	case discordgo.ChannelTypeGuildText:
		return "TEXT"
	case discordgo.ChannelTypeDM:
		return "PRIVATE"
	case discordgo.ChannelTypeGuildVoice:
		return "VOICE"
	case discordgo.ChannelTypeGroupDM:
		return "GROUP"
	case discordgo.ChannelTypeGuildCategory:
		return "CATEGORY"
	case discordgo.ChannelTypeGuildNews:
		return "NEWS"
	case discordgo.ChannelTypeGuildNewsThread:
		return "GUILD_NEWS_THREAD"
	case discordgo.ChannelTypeGuildPublicThread:
		return "GUILD_PUBLIC_THREAD"
	case discordgo.ChannelTypeGuildPrivateThread:
		return "GUILD_PRIVATE_THREAD"
	case discordgo.ChannelTypeGuildStageVoice:
		return "STAGE"
	case discordgo.ChannelTypeGuildForum:
		return "FORUM"
	case discordgo.ChannelTypeGuildMedia:
		return "MEDIA"
	default:
		return "UNKNOWN"
	}
}

// channelSortBucket groups channel kinds for display: message-style channels
// come before voice-style channels.
func channelSortBucket(ty discordgo.ChannelType) int {
	switch ty {
	case discordgo.ChannelTypeGuildVoice, discordgo.ChannelTypeGuildStageVoice:
		return 1
	case discordgo.ChannelTypeGuildCategory:
		return 2
	default:
		return 0
	}
}

// sortChannelsForDisplay sorts by kind bucket, then position, then ID.
func sortChannelsForDisplay(cs []*discordgo.Channel) {
	sort.SliceStable(cs, func(i, j int) bool {
		bi, bj := channelSortBucket(cs[i].Type), channelSortBucket(cs[j].Type)
		if bi != bj {
			return bi < bj
		}
		if cs[i].Position != cs[j].Position {
			return cs[i].Position < cs[j].Position
		}
		return idLess(cs[i].ID, cs[j].ID)
	})
}

// orderChannelsForDisplay arranges non-thread guild channels the way the
// Discord client lists them: channels without a category first, then each
// category followed by its own channels.
func orderChannelsForDisplay(all []*discordgo.Channel) []*discordgo.Channel {
	var categories, uncategorized []*discordgo.Channel
	children := map[string][]*discordgo.Channel{}
	for _, c := range all {
		switch {
		case c.IsThread():
			continue
		case c.Type == discordgo.ChannelTypeGuildCategory:
			categories = append(categories, c)
		case c.ParentID == "":
			uncategorized = append(uncategorized, c)
		default:
			children[c.ParentID] = append(children[c.ParentID], c)
		}
	}
	sortChannels(categories)
	sortChannelsForDisplay(uncategorized)
	out := append([]*discordgo.Channel(nil), uncategorized...)
	seen := map[string]bool{}
	for _, cat := range categories {
		seen[cat.ID] = true
		out = append(out, cat)
		kids := children[cat.ID]
		sortChannelsForDisplay(kids)
		out = append(out, kids...)
	}
	// Channels whose category is not cached are listed last.
	var orphans []*discordgo.Channel
	for parent, kids := range children {
		if !seen[parent] {
			orphans = append(orphans, kids...)
		}
	}
	sortChannelsForDisplay(orphans)
	return append(out, orphans...)
}

// displayChannels returns the guild's channels (no threads) in display order.
func (t *Toolset) displayChannels(g *discordgo.Guild) []*discordgo.Channel {
	all := append([]*discordgo.Channel(nil), g.Channels...)
	return orderChannelsForDisplay(all)
}

// categoryChildren returns the channels in a category, in display order.
func (t *Toolset) categoryChildren(g *discordgo.Guild, categoryID string) []*discordgo.Channel {
	var kids []*discordgo.Channel
	for _, c := range g.Channels {
		if c.ParentID == categoryID && !c.IsThread() && c.Type != discordgo.ChannelTypeGuildCategory {
			kids = append(kids, c)
		}
	}
	sortChannelsForDisplay(kids)
	return kids
}

// displayPosition is the channel's index in the guild's display order, or -1.
func (t *Toolset) displayPosition(g *discordgo.Guild, channelID string) int {
	for i, c := range t.displayChannels(g) {
		if c.ID == channelID {
			return i
		}
	}
	return -1
}

// guildChannel returns any cached channel or thread of guild g, or nil.
func (t *Toolset) guildChannel(g *discordgo.Guild, id string) *discordgo.Channel {
	c := t.cachedChannel(id)
	if c == nil || c.GuildID != g.ID {
		return nil
	}
	return c
}

// guildCategory returns a cached category of guild g, or nil.
func (t *Toolset) guildCategory(g *discordgo.Guild, id string) *discordgo.Channel {
	return t.guildChannelOfType(g, id, discordgo.ChannelTypeGuildCategory)
}

// channelLine renders "- TYPE channel: name (ID: id)".
func channelLine(c *discordgo.Channel) string {
	return "- " + channelTypeName(c.Type) + " channel: " + c.Name + " (ID: " + c.ID + ")"
}

// channelList renders a "Retrieved N channels:" block.
func channelList(cs []*discordgo.Channel) string {
	lines := make([]string, len(cs))
	for i, c := range cs {
		lines[i] = channelLine(c)
	}
	return "Retrieved " + strconv.Itoa(len(cs)) + " channels:\n" + strings.Join(lines, "\n")
}

// parseChannelInt parses a 32-bit integer argument; the error names the input.
func parseChannelInt(s string) (int, error) {
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("For input string: \"%s\"", s)
	}
	return int(n), nil
}

// parseChannelBool reads "true" (any case) as true and anything else as false.
func parseChannelBool(s string) bool { return strings.EqualFold(s, "true") }

// checkSlowmode enforces the 0-21600 second slowmode range when creating a
// channel.
func checkSlowmode(n int) error {
	if n < 0 || n > 21600 {
		return errors.New("Slowmode must be between 0 and 21600 (seconds)!")
	}
	return nil
}

// checkSlowmodeEdit enforces the same range when editing a channel.
func checkSlowmodeEdit(n int) error {
	if n < 0 || n > 21600 {
		return errors.New("Slowmode per user must be between 0 and 21600 (seconds)!")
	}
	return nil
}

// checkCreatePosition rejects a negative position for a new channel.
func checkCreatePosition(fields map[string]any) error {
	if n, ok := fields["position"].(int); ok && n < 0 {
		return errors.New("Position must be >= 0!")
	}
	return nil
}

// reasonOptions adds the audit log reason when one is given.
func reasonOptions(reason string) []discordgo.RequestOption {
	if reason == "" {
		return nil
	}
	return []discordgo.RequestOption{discordgo.WithAuditLogReason(reason)}
}

// patchChannel sends a channel modify request with exactly the given fields
// (a nil value clears the field, for example parent_id).
func (t *Toolset) patchChannel(channelID string, fields map[string]any, reason string) error {
	if len(fields) == 0 {
		return nil
	}
	_, err := t.s.RequestWithBucketID("PATCH", discordgo.EndpointChannel(channelID), fields,
		discordgo.EndpointChannel(channelID), reasonOptions(reason)...)
	return err
}

// setParentField fills parent_id from the categoryId argument: absent leaves
// it alone, empty removes the category, otherwise the category must exist.
func (t *Toolset) setParentField(g *discordgo.Guild, a Args, fields map[string]any) error {
	if !a.Has("categoryId") {
		return nil
	}
	id := a.Get("categoryId")
	if id == "" {
		fields["parent_id"] = nil
		return nil
	}
	if t.guildCategory(g, id) == nil {
		return errCategoryNotFound
	}
	fields["parent_id"] = id
	return nil
}

// optionalChannelInt parses an optional integer argument into fields[key].
func optionalChannelInt(a Args, name, key string, fields map[string]any) error {
	s := a.Get(name)
	if s == "" {
		return nil
	}
	n, err := parseChannelInt(s)
	if err != nil {
		return err
	}
	fields[key] = n
	return nil
}

func (t *Toolset) deleteChannel(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	if err := requireArgs(a, "channelId"); err != nil {
		return "", err
	}
	c := t.guildChannel(g, a.Get("channelId"))
	if c == nil {
		return "", errChannelNotFound
	}
	if _, err := t.s.ChannelDelete(c.ID, reasonOptions(a.Get("reason"))...); err != nil {
		return "", err
	}
	return "Deleted " + channelTypeName(c.Type) + " channel: " + c.Name, nil
}

func (t *Toolset) createTextChannel(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	if err := requireArgs(a, "name"); err != nil {
		return "", err
	}
	fields := map[string]any{"name": a.Get("name"), "type": discordgo.ChannelTypeGuildText}
	if id := a.Get("categoryId"); id != "" {
		if t.guildCategory(g, id) == nil {
			return "", errCategoryNotFound
		}
		fields["parent_id"] = id
	}
	if topic := a.Get("topic"); topic != "" {
		fields["topic"] = topic
	}
	if nsfw := a.Get("nsfw"); nsfw != "" {
		fields["nsfw"] = parseChannelBool(nsfw)
	}
	if err := optionalChannelInt(a, "slowmode", "rate_limit_per_user", fields); err != nil {
		return "", err
	}
	if n, ok := fields["rate_limit_per_user"].(int); ok {
		if err := checkSlowmode(n); err != nil {
			return "", err
		}
	}
	if err := optionalChannelInt(a, "position", "position", fields); err != nil {
		return "", err
	}
	if err := checkCreatePosition(fields); err != nil {
		return "", err
	}
	endpoint := discordgo.EndpointGuildChannels(g.ID)
	body, err := t.s.RequestWithBucketID("POST", endpoint, fields, endpoint)
	if err != nil {
		return "", err
	}
	var created discordgo.Channel
	if err := discordgo.Unmarshal(body, &created); err != nil {
		return "", err
	}
	return "Created text channel: " + created.Name + " (ID: " + created.ID + ")", nil
}

func (t *Toolset) findChannel(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	name := a.Get("channelName")
	if name == "" {
		return "", errors.New("channelName cannot be null")
	}
	channels := t.displayChannels(g)
	if len(channels) == 0 {
		return "", errors.New("No channels found by guildId")
	}
	var found []*discordgo.Channel
	for _, c := range channels {
		if strings.EqualFold(c.Name, name) {
			found = append(found, c)
		}
	}
	if len(found) == 0 {
		return "", errors.New("No channels found with name " + name)
	}
	if len(found) > 1 {
		return channelList(found), nil
	}
	c := found[0]
	return "Retrieved " + channelTypeName(c.Type) + " channel: " + c.Name + " (ID: " + c.ID + ")", nil
}

func (t *Toolset) listChannels(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	channels := t.displayChannels(g)
	if len(channels) == 0 {
		return "", errors.New("No channels found by guildId")
	}
	return channelList(channels), nil
}

func (t *Toolset) editTextChannel(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	if err := requireArgs(a, "channelId"); err != nil {
		return "", err
	}
	channelID := a.Get("channelId")
	c := t.guildChannel(g, channelID)
	if c == nil || (c.Type != discordgo.ChannelTypeGuildText && c.Type != discordgo.ChannelTypeGuildNews) {
		return "", errors.New("Channel not found or is not a standard text/news channel")
	}
	isText := c.Type == discordgo.ChannelTypeGuildText
	if !isText && a.Get("slowmode") != "" {
		return "", errors.New("slowmode is supported only for text channels")
	}

	fields := map[string]any{}
	if name := a.Get("name"); name != "" {
		fields["name"] = name
	}
	if a.Has("topic") {
		fields["topic"] = a.Get("topic")
	}
	if nsfw := a.Get("nsfw"); nsfw != "" {
		fields["nsfw"] = parseChannelBool(nsfw)
	}
	if isText {
		if err := optionalChannelInt(a, "slowmode", "rate_limit_per_user", fields); err != nil {
			return "", err
		}
		if n, ok := fields["rate_limit_per_user"].(int); ok {
			if err := checkSlowmodeEdit(n); err != nil {
				return "", err
			}
		}
	}
	if err := t.setParentField(g, a, fields); err != nil {
		return "", err
	}
	if err := optionalChannelInt(a, "position", "position", fields); err != nil {
		return "", err
	}
	if err := t.patchChannel(c.ID, fields, a.Get("reason")); err != nil {
		return "", err
	}
	if isText {
		return "Updated text channel: " + c.Name + " (ID: " + channelID + ")", nil
	}
	return "Updated " + channelTypeName(c.Type) + " channel: " + c.Name + " (ID: " + channelID + ")", nil
}

func (t *Toolset) getChannelInfo(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	if err := requireArgs(a, "channelId"); err != nil {
		return "", err
	}
	c := t.guildChannel(g, a.Get("channelId"))
	if c == nil {
		return "", errChannelNotFound
	}
	var b strings.Builder
	b.WriteString("Type: " + channelTypeName(c.Type) + "\n")
	b.WriteString("Name: " + c.Name + "\n")
	b.WriteString("ID: " + c.ID + "\n")
	writeCategory := func() {
		if c.ParentID == "" {
			return
		}
		if cat := t.guildCategory(g, c.ParentID); cat != nil {
			b.WriteString("Category: " + cat.Name + " (ID: " + cat.ID + ")\n")
		}
	}
	switch c.Type {
	case discordgo.ChannelTypeGuildText:
		b.WriteString("Topic: " + orNone(c.Topic) + "\n")
		b.WriteString("NSFW: " + boolText(c.NSFW) + "\n")
		b.WriteString("Slowmode: " + strconv.Itoa(c.RateLimitPerUser) + "s\n")
		b.WriteString("Position: " + strconv.Itoa(t.displayPosition(g, c.ID)) + "\n")
		writeCategory()
	case discordgo.ChannelTypeGuildVoice:
		b.WriteString("Bitrate: " + strconv.Itoa(c.Bitrate) + "\n")
		b.WriteString("User Limit: " + strconv.Itoa(c.UserLimit) + "\n")
		b.WriteString("Position: " + strconv.Itoa(t.displayPosition(g, c.ID)) + "\n")
		writeCategory()
	case discordgo.ChannelTypeGuildCategory:
		b.WriteString("Position: " + strconv.Itoa(t.displayPosition(g, c.ID)) + "\n")
		b.WriteString("Channels: " + strconv.Itoa(len(t.categoryChildren(g, c.ID))) + "\n")
	}
	b.WriteString("Created: " + formatDate(snowflakeTime(c.ID)))
	return b.String(), nil
}

func (t *Toolset) moveChannel(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	if err := requireArgs(a, "channelId"); err != nil {
		return "", err
	}
	channelID := a.Get("channelId")
	c := t.guildChannel(g, channelID)
	if c == nil || c.IsThread() || c.Type == discordgo.ChannelTypeGuildCategory {
		return "", errors.New("Channel not found or cannot be moved")
	}
	fields := map[string]any{}
	if err := t.setParentField(g, a, fields); err != nil {
		return "", err
	}
	if err := optionalChannelInt(a, "position", "position", fields); err != nil {
		return "", err
	}
	if err := t.patchChannel(c.ID, fields, a.Get("reason")); err != nil {
		return "", err
	}
	return "Moved channel: " + c.Name + " (ID: " + channelID + ")", nil
}
