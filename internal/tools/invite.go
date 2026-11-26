package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func (t *Toolset) inviteTools() []Tool {
	codeParam := req("inviteCode", "Invite code or full URL (e.g. 'ABCde' or 'https://discord.gg/ABCde')")
	return []Tool{
		{
			Name:        "create_invite",
			Description: "Create a new invite link for a specific channel",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("channelId", "ID of the channel the invite directs to"),
				opt("maxAge", "Duration in seconds before expiry (0 = never, default 86400 = 24h)"),
				opt("maxUses", "Max number of uses (0 = unlimited, default 0)"),
				opt("temporary", "Whether members get temporary membership (kicked when disconnected unless role assigned)"),
				opt("unique", "Force creation of a new unique invite code"),
			},
			Handler: t.createInvite,
		},
		{
			Name:        "list_invites",
			Description: "List all active invites on the server with their statistics",
			Params:      []Param{opt("guildId", "Discord server ID")},
			Handler:     t.listInvites,
		},
		{
			Name:        "delete_invite",
			Description: "Delete (revoke) an invite so the link stops working",
			Params:      []Param{codeParam},
			Handler:     t.deleteInvite,
		},
		{
			Name:        "get_invite_details",
			Description: "Get details about a specific invite (works for any public invite)",
			Params:      []Param{codeParam, opt("withCounts", "Whether to include approximate member counts (default true)")},
			Handler:     t.getInviteDetails,
		},
	}
}

// inviteChannelTypes are the channel types that can have invites.
var inviteChannelTypes = []discordgo.ChannelType{
	discordgo.ChannelTypeGuildText,
	discordgo.ChannelTypeGuildNews,
	discordgo.ChannelTypeGuildVoice,
	discordgo.ChannelTypeGuildStageVoice,
	discordgo.ChannelTypeGuildForum,
	discordgo.ChannelTypeGuildMedia,
}

// inviteExtractCode strips a discord.gg or discord.com/invite prefix.
func inviteExtractCode(input string) (string, error) {
	if input == "" {
		return "", errors.New("invite_code cannot be null")
	}
	r := strings.NewReplacer(
		"https://discord.gg/", "",
		"http://discord.gg/", "",
		"https://discord.com/invite/", "",
		"http://discord.com/invite/", "",
	)
	return strings.TrimSpace(r.Replace(input)), nil
}

// inviteParseInt parses a whole number, failing with the offending input.
func inviteParseInt(s string) (int, error) {
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0, errors.New("For input string: \"" + s + "\"")
	}
	return int(n), nil
}

// inviteParseBool is true only for "true" in any letter case.
func inviteParseBool(s string) bool { return strings.EqualFold(s, "true") }

func inviteMaxUses(n int) string {
	if n == 0 {
		return "Unlimited"
	}
	return strconv.Itoa(n)
}

func (t *Toolset) createInvite(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	channelID := a.Get("channelId")
	if channelID == "" {
		return "", errors.New("channelId cannot be null")
	}
	ch := t.cachedChannel(channelID)
	if ch == nil || ch.GuildID != g.ID {
		return "", errChannelNotFound
	}
	if t.guildChannelOfType(g, channelID, inviteChannelTypes...) == nil {
		return "", errors.New("This channel type does not support invites")
	}

	// Only send the options that were given, so Discord applies its own
	// defaults (24 hours, unlimited uses) to the rest.
	body := map[string]any{}
	if v := a.Get("maxAge"); v != "" {
		n, err := inviteParseInt(v)
		if err != nil {
			return "", err
		}
		if n < 0 {
			return "", errors.New("maxAge may not be negative")
		}
		body["max_age"] = n
	}
	if v := a.Get("maxUses"); v != "" {
		n, err := inviteParseInt(v)
		if err != nil {
			return "", err
		}
		if n < 0 {
			return "", errors.New("maxUses may not be negative")
		}
		body["max_uses"] = n
	}
	if v := a.Get("temporary"); v != "" {
		body["temporary"] = inviteParseBool(v)
	}
	if v := a.Get("unique"); v != "" {
		body["unique"] = inviteParseBool(v)
	}
	endpoint := discordgo.EndpointChannelInvites(ch.ID)
	raw, err := t.s.RequestWithBucketID(http.MethodPost, endpoint, body, endpoint)
	if err != nil {
		return "", err
	}
	var inv discordgo.Invite
	if err := json.Unmarshal(raw, &inv); err != nil {
		return "", err
	}

	maxAge := "Never expires"
	if inv.MaxAge != 0 {
		maxAge = strconv.Itoa(inv.MaxAge) + "s"
	}
	return "Created invite: https://discord.gg/" + inv.Code +
		"\n  • Channel: " + ch.Name + " (ID: " + channelID + ")" +
		"\n  • Max Age: " + maxAge +
		"\n  • Max Uses: " + inviteMaxUses(inv.MaxUses) +
		"\n  • Temporary: " + boolText(inv.Temporary), nil
}

func (t *Toolset) listInvites(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	invites, err := t.s.GuildInvites(g.ID)
	if err != nil {
		return "", err
	}
	if len(invites) == 0 {
		return "No active invites found on this server.", nil
	}
	entries := make([]string, 0, len(invites))
	for _, inv := range invites {
		var b strings.Builder
		b.WriteString("- **" + inv.Code + "** (https://discord.gg/" + inv.Code + ")\n")
		if inv.Channel != nil {
			b.WriteString("  • Channel: " + inv.Channel.Name + " (ID: " + inv.Channel.ID + ")\n")
		}
		if inv.Inviter != nil {
			b.WriteString("  • Created by: " + inv.Inviter.Username + " (ID: " + inv.Inviter.ID + ")\n")
		}
		maxAge := "Never"
		if inv.MaxAge != 0 {
			maxAge = strconv.Itoa(inv.MaxAge) + "s"
		}
		b.WriteString("  • Uses: " + strconv.Itoa(inv.Uses))
		b.WriteString(" | Max Uses: " + inviteMaxUses(inv.MaxUses))
		b.WriteString(" | Max Age: " + maxAge)
		b.WriteString(" | Temporary: " + boolText(inv.Temporary))
		entries = append(entries, b.String())
	}
	return "Retrieved " + strconv.Itoa(len(invites)) + " active invites:\n" + strings.Join(entries, "\n"), nil
}

func (t *Toolset) deleteInvite(_ context.Context, a Args) (string, error) {
	code, err := inviteExtractCode(a.Get("inviteCode"))
	if err != nil {
		return "", err
	}
	if _, err := t.s.Invite(code); err != nil {
		return "", err
	}
	if _, err := t.s.InviteDelete(code); err != nil {
		return "", err
	}
	return "Successfully deleted invite: " + code, nil
}

func (t *Toolset) getInviteDetails(_ context.Context, a Args) (string, error) {
	code, err := inviteExtractCode(a.Get("inviteCode"))
	if err != nil {
		return "", err
	}
	withCounts := a.Get("withCounts")
	counts := withCounts == "" || inviteParseBool(withCounts)

	var inv *discordgo.Invite
	if counts {
		inv, err = t.s.InviteWithCounts(code)
	} else {
		inv, err = t.s.Invite(code)
	}
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("Invite: **" + inv.Code + "** (https://discord.gg/" + inv.Code + ")\n")
	if inv.Guild != nil {
		b.WriteString("  • Server: " + inv.Guild.Name + " (ID: " + inv.Guild.ID + ")\n")
	}
	if inv.Channel != nil {
		b.WriteString("  • Channel: " + inv.Channel.Name + " (ID: " + inv.Channel.ID + ")\n")
	}
	if inv.Inviter != nil {
		b.WriteString("  • Created by: " + inv.Inviter.Username + " (ID: " + inv.Inviter.ID + ")\n")
	}
	if counts {
		online, total := "N/A", "N/A"
		if inv.Guild != nil {
			online = strconv.Itoa(inv.ApproximatePresenceCount)
			total = strconv.Itoa(inv.ApproximateMemberCount)
		}
		b.WriteString("  • Members Online: " + online)
		b.WriteString(" | Total Members: " + total)
	}
	return b.String(), nil
}
