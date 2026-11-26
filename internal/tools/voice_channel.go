package tools

import (
	"context"
	"errors"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func (t *Toolset) voiceChannelTools() []Tool {
	return []Tool{
		{
			Name:        "create_voice_channel",
			Description: "Create a new voice channel in a guild",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("name", "Channel name"),
				opt("categoryId", "Category ID"),
				opt("userLimit", "Max users (0 = unlimited, max 99)"),
				opt("bitrate", "Audio bitrate in bits/s (e.g. 64000). Max depends on server boost level"),
			},
			Handler: t.createVoiceChannel,
		},
		{
			Name:        "create_stage_channel",
			Description: "Create a new stage channel for audio events in a guild",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("name", "Channel name"),
				opt("categoryId", "Category ID"),
				opt("bitrate", "Audio bitrate in bits/s (e.g. 64000)"),
			},
			Handler: t.createStageChannel,
		},
		{
			Name:        "edit_voice_channel",
			Description: "Edit settings of a voice or stage channel (name, bitrate, user limit, region)",
			Params: []Param{
				req("channelId", "Channel ID"),
				opt("name", "New channel name"),
				opt("bitrate", "New bitrate in bits/s"),
				opt("userLimit", "New user limit (0 = unlimited)"),
				opt("rtcRegion", "Voice region (e.g. 'rotterdam', 'us-east'). Empty for automatic"),
			},
			Handler: t.editVoiceChannel,
		},
		{
			Name:        "move_member",
			Description: "Move a member to another voice channel. The member must already be connected to a voice channel.",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("userId", "ID of the user to move"),
				req("channelId", "ID of the target voice channel"),
			},
			Handler: t.moveMember,
		},
		{
			Name:        "disconnect_member",
			Description: "Disconnect a member from their current voice channel",
			Params:      []Param{opt("guildId", "Discord server ID"), req("userId", "ID of the user to disconnect")},
			Handler:     t.disconnectMember,
		},
		{
			Name:        "modify_voice_state",
			Description: "Server mute or deafen a member across all voice channels. The member must be in a voice channel.",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("userId", "ID of the user"),
				opt("mute", "Whether to server-mute the user's microphone"),
				opt("deafen", "Whether to server-deafen the user's audio"),
			},
			Handler: t.modifyVoiceState,
		},
	}
}

var errVoiceNotConnected = errors.New("User is not connected to any voice channel")

// voiceMember fetches a guild member (cache first, then the API).
func (t *Toolset) voiceMember(g *discordgo.Guild, userID string) (*discordgo.Member, error) {
	if userID == "" {
		return nil, errors.New("userId cannot be null")
	}
	m := t.member(g.ID, userID, false)
	if m == nil || m.User == nil {
		return nil, errors.New("User not found in this server by userId")
	}
	return m, nil
}

// voiceConnected reports whether the user is in a voice or stage channel of
// guild g, according to the cached voice states.
func (t *Toolset) voiceConnected(g *discordgo.Guild, userID string) bool {
	vs, err := t.s.State.VoiceState(g.ID, userID)
	if err != nil || vs == nil {
		return false
	}
	t.s.State.RLock()
	defer t.s.State.RUnlock()
	return vs.ChannelID != ""
}

// voiceConnectedMember fetches the member and checks they are in voice.
func (t *Toolset) voiceConnectedMember(a Args) (*discordgo.Guild, *discordgo.Member, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return nil, nil, err
	}
	m, err := t.voiceMember(g, a.Get("userId"))
	if err != nil {
		return nil, nil, err
	}
	if !t.voiceConnected(g, m.User.ID) {
		return nil, nil, errVoiceNotConnected
	}
	return g, m, nil
}

// voiceCreate creates a voice-type channel; userLimit is applied with a
// separate edit after the channel exists.
func (t *Toolset) voiceCreate(a Args, ty discordgo.ChannelType) (*discordgo.Channel, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return nil, err
	}
	name := a.Get("name")
	if name == "" {
		return nil, errors.New("name cannot be null")
	}
	body := map[string]any{"name": name, "type": ty}
	if id := a.Get("categoryId"); id != "" {
		c, err := t.forumCategory(g, id)
		if err != nil {
			return nil, err
		}
		body["parent_id"] = c.ID
	}
	if v := a.Get("bitrate"); v != "" {
		n, err := forumParseInt(v)
		if err != nil {
			return nil, err
		}
		body["bitrate"] = n
	}
	var created discordgo.Channel
	if err := t.forumRequest("POST", discordgo.EndpointGuildChannels(g.ID), body, "", &created); err != nil {
		return nil, err
	}
	return &created, nil
}

func (t *Toolset) createVoiceChannel(_ context.Context, a Args) (string, error) {
	ch, err := t.voiceCreate(a, discordgo.ChannelTypeGuildVoice)
	if err != nil {
		return "", err
	}
	if v := a.Get("userLimit"); v != "" {
		n, err := forumParseInt(v)
		if err != nil {
			return "", err
		}
		if err := t.forumRequest("PATCH", discordgo.EndpointChannel(ch.ID), map[string]any{"user_limit": n}, "", nil); err != nil {
			return "", err
		}
	}
	return "Created voice channel: " + ch.Name + " (ID: " + ch.ID + ")", nil
}

func (t *Toolset) createStageChannel(_ context.Context, a Args) (string, error) {
	ch, err := t.voiceCreate(a, discordgo.ChannelTypeGuildStageVoice)
	if err != nil {
		return "", err
	}
	return "Created stage channel: " + ch.Name + " (ID: " + ch.ID + ")", nil
}

// voiceRegion is the rtc_region value: null (automatic) for an empty region.
func voiceRegion(region string) any {
	if region == "" {
		return nil
	}
	return region
}

func (t *Toolset) editVoiceChannel(_ context.Context, a Args) (string, error) {
	channelID := a.Get("channelId")
	if channelID == "" {
		return "", errors.New("channelId cannot be null")
	}
	ch := t.channelOfType(channelID, discordgo.ChannelTypeGuildVoice, discordgo.ChannelTypeGuildStageVoice)
	if ch == nil {
		return "", errors.New("Voice or stage channel not found by channelId")
	}
	isVoice := ch.Type == discordgo.ChannelTypeGuildVoice

	body := map[string]any{}
	if v := a.Get("name"); v != "" {
		body["name"] = v
	}
	if v := a.Get("bitrate"); v != "" {
		n, err := forumParseInt(v)
		if err != nil {
			return "", err
		}
		body["bitrate"] = n
	}
	// Stage channels have no user limit, so the argument is ignored for them.
	if v := a.Get("userLimit"); v != "" && isVoice {
		n, err := forumParseInt(v)
		if err != nil {
			return "", err
		}
		body["user_limit"] = n
	}
	if a.Has("rtcRegion") {
		body["rtc_region"] = voiceRegion(a.Get("rtcRegion"))
	}
	if len(body) > 0 {
		if err := t.forumRequest("PATCH", discordgo.EndpointChannel(ch.ID), body, "", nil); err != nil {
			return "", err
		}
	}
	kind := "stage"
	if isVoice {
		kind = "voice"
	}
	return "Updated " + kind + " channel: " + ch.Name + " (ID: " + channelID + ")", nil
}

func (t *Toolset) moveMember(_ context.Context, a Args) (string, error) {
	g, m, err := t.voiceConnectedMember(a)
	if err != nil {
		return "", err
	}
	target := t.guildChannelOfType(g, a.Get("channelId"), discordgo.ChannelTypeGuildVoice, discordgo.ChannelTypeGuildStageVoice)
	if target == nil {
		return "", errors.New("Target channel is not a voice or stage channel")
	}
	if err := t.s.GuildMemberMove(g.ID, m.User.ID, &target.ID); err != nil {
		if forumIsMissingPermissions(err) {
			return "", errors.New("Bot lacks permission to move members")
		}
		return "", err
	}
	return "Moved user " + m.User.Username + " to channel: " + target.Name, nil
}

func (t *Toolset) disconnectMember(_ context.Context, a Args) (string, error) {
	g, m, err := t.voiceConnectedMember(a)
	if err != nil {
		return "", err
	}
	if err := t.s.GuildMemberMove(g.ID, m.User.ID, nil); err != nil {
		if forumIsMissingPermissions(err) {
			return "", errors.New("Bot lacks permission to disconnect members")
		}
		return "", err
	}
	return "Disconnected user " + m.User.Username + " from voice channel", nil
}

func (t *Toolset) modifyVoiceState(_ context.Context, a Args) (string, error) {
	g, m, err := t.voiceConnectedMember(a)
	if err != nil {
		return "", err
	}
	mute, deafen := a.Get("mute"), a.Get("deafen")
	if mute == "" && deafen == "" {
		return "", errors.New("At least one of 'mute' or 'deafen' must be provided")
	}
	permErr := func(err error) error {
		if forumIsMissingPermissions(err) {
			return errors.New("Bot lacks permission to mute/deafen members")
		}
		return err
	}

	var result strings.Builder
	if mute != "" {
		v := forumParseBool(mute)
		if err := t.s.GuildMemberMute(g.ID, m.User.ID, v); err != nil {
			return "", permErr(err)
		}
		if v {
			result.WriteString("Muted")
		} else {
			result.WriteString("Unmuted")
		}
		result.WriteString(" user " + m.User.Username)
	}
	if deafen != "" {
		v := forumParseBool(deafen)
		if err := t.s.GuildMemberDeafen(g.ID, m.User.ID, v); err != nil {
			return "", permErr(err)
		}
		if result.Len() > 0 {
			result.WriteString(". ")
		}
		if v {
			result.WriteString("Deafened")
		} else {
			result.WriteString("Undeafened")
		}
		result.WriteString(" user " + m.User.Username)
	}
	return result.String(), nil
}
