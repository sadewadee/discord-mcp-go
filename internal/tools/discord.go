package tools

import (
	"context"
	"errors"
	"strconv"

	"github.com/bwmarrin/discordgo"
)

func (t *Toolset) discordTools() []Tool {
	return []Tool{{
		Name:        "get_server_info",
		Description: "Get detailed discord server information",
		Params:      []Param{opt("guildId", "Discord server ID")},
		Handler:     t.getServerInfo,
	}}
}

// boostTierName names a guild's premium tier.
func boostTierName(tier discordgo.PremiumTier) string {
	switch tier {
	case discordgo.PremiumTierNone:
		return "NONE"
	case discordgo.PremiumTier1:
		return "TIER_1"
	case discordgo.PremiumTier2:
		return "TIER_2"
	case discordgo.PremiumTier3:
		return "TIER_3"
	default:
		return "UNKNOWN"
	}
}

func (t *Toolset) getServerInfo(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "Discord server ID cannot be null")
	if err != nil {
		return "", err
	}
	owner := t.member(g.ID, g.OwnerID, false)
	if owner == nil || owner.User == nil {
		return "", errors.New("10007: Unknown Member")
	}
	text := len(t.guildChannels(g, discordgo.ChannelTypeGuildText))
	voice := len(t.guildChannels(g, discordgo.ChannelTypeGuildVoice))
	categories := len(t.guildChannels(g, discordgo.ChannelTypeGuildCategory))

	return "Server Name: " + g.Name + "\n" +
		"Server ID: " + g.ID + "\n" +
		"Owner: " + owner.User.Username + "\n" +
		"Created On: " + formatDate(snowflakeTime(g.ID)) + "\n" +
		"Members: " + strconv.Itoa(g.MemberCount) + "\n" +
		"Channels: " +
		" - Text: " + strconv.Itoa(text) + "\n" +
		" - Voice: " + strconv.Itoa(voice) + "\n" +
		"  - Categories: " + strconv.Itoa(categories) + "\n" +
		"Boosts: " +
		" - Count: " + strconv.Itoa(g.PremiumSubscriptionCount) + "\n" +
		" - Tier: " + boostTierName(g.PremiumTier), nil
}
