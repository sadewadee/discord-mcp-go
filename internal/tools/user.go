package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

var errUserNotFound = errors.New("User not found by userId")

func (t *Toolset) userTools() []Tool {
	return []Tool{
		{
			Name:        "get_user_id_by_name",
			Description: "Get a Discord user's ID by exact username in a guild for ping usage <@id>. To match by nickname or global name, use search_members.",
			Params:      []Param{req("username", "Discord username (optionally username#discriminator)"), opt("guildId", "Discord server ID")},
			Handler:     t.getUserIDByName,
		},
		{
			Name:        "get_member_by_id",
			Description: "Get a Discord guild member by stable user ID, including username, global name, nickname, effective name, and roles.",
			Params:      []Param{req("userId", "Discord user ID"), opt("guildId", "Discord server ID")},
			Handler:     t.getMemberByID,
		},
		{
			Name:        "search_members",
			Description: "Search Discord guild members by ID, username, global name, nickname, or effective name. Returns candidates with stable user IDs for reconciliation.",
			Params: []Param{
				req("query", "Search query or Discord user ID"),
				opt("count", "Maximum candidates to return (1-100)"),
				opt("guildId", "Discord server ID"),
			},
			Handler: t.searchMembers,
		},
		{
			Name:        "send_private_message",
			Description: "Send a private message to a specific user",
			Params:      []Param{req("userId", "Discord user ID"), req("message", "Message content")},
			Handler:     t.sendPrivateMessage,
		},
		{
			Name:        "edit_private_message",
			Description: "Edit a private message from a specific user",
			Params:      []Param{req("userId", "Discord user ID"), req("messageId", "Specific message ID"), req("newMessage", "New message content")},
			Handler:     t.editPrivateMessage,
		},
		{
			Name:        "delete_private_message",
			Description: "Delete a private message from a specific user",
			Params:      []Param{req("userId", "Discord user ID"), req("messageId", "Specific message ID")},
			Handler:     t.deletePrivateMessage,
		},
		{
			Name:        "read_private_messages",
			Description: "Read private message history from a specific user, optionally paginated with before/after/around",
			Params: []Param{
				req("userId", "Discord user ID"),
				opt("count", "Number of messages to retrieve (1-100)"),
				opt("before", "Message ID to fetch messages before this message"),
				opt("after", "Message ID to fetch messages after this message"),
				opt("around", "Message ID to fetch messages around this message"),
			},
			Handler: t.readPrivateMessages,
		},
	}
}

func (t *Toolset) getUserIDByName(_ context.Context, a Args) (string, error) {
	username := a.Get("username")
	if username == "" {
		return "", errors.New("username cannot be null")
	}
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	name, disc, hasDisc := username, "", false
	if i := strings.LastIndex(username, "#"); i >= 0 {
		name, disc, hasDisc = username[:i], username[i+1:], true
	}
	var found []*discordgo.Member
	for _, m := range t.guildMembers(g) {
		if m.User == nil || !strings.EqualFold(m.User.Username, name) {
			continue
		}
		if hasDisc && discriminator(m.User) != disc {
			continue
		}
		found = append(found, m)
	}
	if len(found) == 0 {
		return "", errors.New("No user found with username " + username)
	}
	if len(found) > 1 {
		list := make([]string, len(found))
		for i, m := range found {
			list[i] = m.User.Username + "#" + discriminator(m.User) + " (ID: " + m.User.ID + ")"
		}
		return "", fmt.Errorf("Multiple users found with username '%s'. List: %s. Please specify the full username#discriminator.", username, strings.Join(list, ", "))
	}
	return found[0].User.ID, nil
}

func (t *Toolset) getMemberByID(_ context.Context, a Args) (string, error) {
	userID := a.Get("userId")
	if userID == "" {
		return "", errors.New("userId cannot be null")
	}
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	m := t.member(g.ID, userID, true)
	if m == nil {
		return "", errors.New("No guild member found with userId " + userID)
	}
	return "**Member found:**\n" + t.formatMemberSummary(g, m, true), nil
}

// parseMemberLimit parses the count argument of search_members: 1-100, default 25.
func parseMemberLimit(count string) (int, error) {
	if isBlank(count) {
		return 25, nil
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

// memberMatches reports whether the query appears (case-insensitively) in
// the member's username, global name, nickname or effective name.
func memberMatches(m *discordgo.Member, query string) bool {
	q := strings.ToLower(query)
	contains := func(v string) bool { return v != "" && strings.Contains(strings.ToLower(v), q) }
	return m.User != nil && (contains(m.User.Username) || contains(m.User.GlobalName) ||
		contains(m.Nick) || contains(memberEffectiveName(m)))
}

func (t *Toolset) searchMembers(_ context.Context, a Args) (string, error) {
	query := a.Get("query")
	if isBlank(query) {
		return "", errors.New("query cannot be null")
	}
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	limit, err := parseMemberLimit(a.Get("count"))
	if err != nil {
		return "", err
	}
	query = strings.TrimSpace(query)

	// Insertion-ordered set of candidates keyed by user ID.
	var order []string
	candidates := map[string]*discordgo.Member{}
	add := func(m *discordgo.Member) {
		if m == nil || m.User == nil {
			return
		}
		if _, ok := candidates[m.User.ID]; !ok {
			order = append(order, m.User.ID)
		}
		candidates[m.User.ID] = m
	}

	if isSnowflake(query) {
		add(t.member(g.ID, query, true))
	}
	if len(candidates) < limit {
		for _, m := range t.guildMembers(g) {
			if memberMatches(m, query) {
				add(m)
			}
			if len(candidates) >= limit {
				break
			}
		}
	}
	if len(candidates) < limit {
		// Return exact/cache matches even if the live prefix lookup is unavailable.
		if found, err := t.s.GuildMembersSearch(g.ID, query, limit); err == nil {
			for _, m := range found {
				add(m)
				if len(candidates) >= limit {
					break
				}
			}
		}
	}

	if len(candidates) == 0 {
		return "No members found for query " + query, nil
	}
	lines := make([]string, 0, len(order))
	for _, id := range order {
		lines = append(lines, t.formatMemberSummary(g, candidates[id], false))
	}
	return fmt.Sprintf("**Found %d member candidate(s):**\n%s", len(lines), strings.Join(lines, "\n")), nil
}

// formatMemberSummary renders a member on one line; with details it adds the
// join time and roles.
func (t *Toolset) formatMemberSummary(g *discordgo.Guild, m *discordgo.Member, details bool) string {
	u := m.User
	summary := fmt.Sprintf("- User ID: %s | Username: %s | Global: %s | Nickname: %s | Effective: %s | Bot: %s",
		u.ID, u.Username, orNone(u.GlobalName), orNone(m.Nick), memberEffectiveName(m), boolText(u.Bot))
	if !details {
		return summary
	}
	var roles []string
	for _, r := range t.memberRoles(g, m) {
		roles = append(roles, r.Name+" (ID: "+r.ID+")")
	}
	roleText := strings.Join(roles, ", ")
	if isBlank(roleText) {
		roleText = "none"
	}
	return summary + fmt.Sprintf(" | Joined: %s | Roles: %s", formatTimestamp(m.JoinedAt), roleText)
}

// dmChannel validates userId and opens the DM channel with that user.
func (t *Toolset) dmChannel(userID string) (*discordgo.Channel, error) {
	if _, err := t.s.User(userID); err != nil {
		return nil, errUserNotFound
	}
	return t.s.UserChannelCreate(userID)
}

func (t *Toolset) sendPrivateMessage(_ context.Context, a Args) (string, error) {
	if err := requireArgs(a, "userId", "message"); err != nil {
		return "", err
	}
	ch, err := t.dmChannel(a.Get("userId"))
	if err != nil {
		return "", err
	}
	sent, err := t.s.ChannelMessageSend(ch.ID, a.Get("message"))
	if err != nil {
		return "", err
	}
	return "Message sent successfully. Message link: " + jumpURL("", ch.ID, sent.ID), nil
}

func (t *Toolset) editPrivateMessage(_ context.Context, a Args) (string, error) {
	if err := requireArgs(a, "userId", "messageId", "newMessage"); err != nil {
		return "", err
	}
	ch, err := t.dmChannel(a.Get("userId"))
	if err != nil {
		return "", err
	}
	msg, err := t.s.ChannelMessage(ch.ID, a.Get("messageId"))
	if err != nil {
		return "", err
	}
	edited, err := t.s.ChannelMessageEdit(ch.ID, msg.ID, a.Get("newMessage"))
	if err != nil {
		return "", err
	}
	return "Message edited successfully. Message link: " + jumpURL("", ch.ID, edited.ID), nil
}

func (t *Toolset) deletePrivateMessage(_ context.Context, a Args) (string, error) {
	if err := requireArgs(a, "userId", "messageId"); err != nil {
		return "", err
	}
	ch, err := t.dmChannel(a.Get("userId"))
	if err != nil {
		return "", err
	}
	msg, err := t.s.ChannelMessage(ch.ID, a.Get("messageId"))
	if err != nil {
		return "", err
	}
	if err := t.s.ChannelMessageDelete(ch.ID, msg.ID); err != nil {
		return "", err
	}
	return "Message deleted successfully", nil
}

func (t *Toolset) readPrivateMessages(_ context.Context, a Args) (string, error) {
	if err := requireArgs(a, "userId"); err != nil {
		return "", err
	}
	limit, err := parseMessageLimit(a.Get("count"))
	if err != nil {
		return "", err
	}
	if err := validateCursors(a); err != nil {
		return "", err
	}
	ch, err := t.dmChannel(a.Get("userId"))
	if err != nil {
		return "", err
	}
	msgs, err := t.readHistory(ch.ID, limit, a)
	if err != nil {
		return "", err
	}
	return t.retrievedMessages(msgs, ""), nil
}
