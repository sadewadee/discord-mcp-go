package tools

import (
	"errors"
	"sort"

	"github.com/bwmarrin/discordgo"
)

// errGuildNotFound is returned when a guild ID is not in the state cache.
var errGuildNotFound = errors.New("Discord server not found by guildId")

// resolveGuildID returns guildID, or the default guild when guildID is empty.
func (t *Toolset) resolveGuildID(guildID string) string {
	if guildID == "" && t.defaultGuildID != "" {
		return t.defaultGuildID
	}
	return guildID
}

// guild resolves guildID (falling back to the default guild) and looks it up
// in the state cache. emptyMsg is the error text used when no guild ID is
// available; tools word it differently, for example "guildId cannot be null".
func (t *Toolset) guild(guildID, emptyMsg string) (*discordgo.Guild, error) {
	guildID = t.resolveGuildID(guildID)
	if guildID == "" {
		return nil, errors.New(emptyMsg)
	}
	g := t.guildSnapshot(guildID)
	if g == nil {
		return nil, errGuildNotFound
	}
	return g, nil
}

// guildSnapshot returns a copy of a cached guild, or nil. The gateway updates
// cached entities in place, so tools work on copies taken under the state
// lock. Channels, threads, roles and emojis are copied; members, presences
// and voice states are left out (use guildMembers and voiceChannelOf).
func (t *Toolset) guildSnapshot(guildID string) *discordgo.Guild {
	if guildID == "" {
		return nil
	}
	// State accessors take the read lock themselves and it must not be
	// taken twice, so fetch the pointer first and copy under the lock.
	g, err := t.s.State.Guild(guildID)
	if err != nil || g == nil {
		return nil
	}
	t.s.State.RLock()
	defer t.s.State.RUnlock()
	cp := *g
	cp.Members, cp.Presences, cp.VoiceStates = nil, nil, nil
	cp.Channels = copyChannels(g.Channels)
	cp.Threads = copyChannels(g.Threads)
	cp.Roles = make([]*discordgo.Role, len(g.Roles))
	for i, r := range g.Roles {
		rc := *r
		cp.Roles[i] = &rc
	}
	cp.Emojis = make([]*discordgo.Emoji, len(g.Emojis))
	for i, e := range g.Emojis {
		ec := *e
		cp.Emojis[i] = &ec
	}
	return &cp
}

func copyChannels(in []*discordgo.Channel) []*discordgo.Channel {
	out := make([]*discordgo.Channel, len(in))
	for i, c := range in {
		cc := *c
		out[i] = &cc
	}
	return out
}

// cachedChannel returns a channel or thread from the state cache, or nil.
func (t *Toolset) cachedChannel(id string) *discordgo.Channel {
	if id == "" {
		return nil
	}
	c, err := t.s.State.Channel(id)
	if err != nil || c == nil {
		return nil
	}
	t.s.State.RLock()
	defer t.s.State.RUnlock()
	cp := *c
	return &cp
}

// channelOfType returns the cached channel with the given ID when it has one
// of the given types, or nil.
func (t *Toolset) channelOfType(id string, types ...discordgo.ChannelType) *discordgo.Channel {
	c := t.cachedChannel(id)
	if c == nil {
		return nil
	}
	for _, ty := range types {
		if c.Type == ty {
			return c
		}
	}
	return nil
}

// guildChannelOfType is channelOfType restricted to channels in guild g.
func (t *Toolset) guildChannelOfType(g *discordgo.Guild, id string, types ...discordgo.ChannelType) *discordgo.Channel {
	c := t.channelOfType(id, types...)
	if c == nil || c.GuildID != g.ID {
		return nil
	}
	return c
}

// threadTypes are the three thread channel types.
var threadTypes = []discordgo.ChannelType{
	discordgo.ChannelTypeGuildPublicThread,
	discordgo.ChannelTypeGuildPrivateThread,
	discordgo.ChannelTypeGuildNewsThread,
}

// messageChannel returns a cached text, announcement or thread channel, or nil.
// These are the channel kinds the message tools accept.
func (t *Toolset) messageChannel(id string) *discordgo.Channel {
	types := append([]discordgo.ChannelType{discordgo.ChannelTypeGuildText, discordgo.ChannelTypeGuildNews}, threadTypes...)
	return t.channelOfType(id, types...)
}

// guildChannels returns the channels of the given types from guild snapshot
// g, in position order (position, then ID). Threads are not included unless a
// thread type is asked for.
func (t *Toolset) guildChannels(g *discordgo.Guild, types ...discordgo.ChannelType) []*discordgo.Channel {
	var out []*discordgo.Channel
	match := func(c *discordgo.Channel) bool {
		for _, ty := range types {
			if c.Type == ty {
				return true
			}
		}
		return false
	}
	for _, c := range g.Channels {
		if match(c) {
			out = append(out, c)
		}
	}
	for _, c := range g.Threads {
		if match(c) {
			out = append(out, c)
		}
	}
	sortChannels(out)
	return out
}

// sortChannels sorts by position, then by ID (oldest first).
func sortChannels(cs []*discordgo.Channel) {
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].Position != cs[j].Position {
			return cs[i].Position < cs[j].Position
		}
		return idLess(cs[i].ID, cs[j].ID)
	})
}

// guildRoles returns the roles of guild snapshot g, highest position first, ties
// broken by ID (oldest first). The @everyone role is included.
func (t *Toolset) guildRoles(g *discordgo.Guild) []*discordgo.Role {
	roles := append([]*discordgo.Role(nil), g.Roles...)
	sort.SliceStable(roles, func(i, j int) bool {
		if roles[i].Position != roles[j].Position {
			return roles[i].Position > roles[j].Position
		}
		return idLess(roles[i].ID, roles[j].ID)
	})
	return roles
}

// cachedRole returns a role of guild g from the state cache, or nil.
func (t *Toolset) cachedRole(g *discordgo.Guild, roleID string) *discordgo.Role {
	if roleID == "" {
		return nil
	}
	r, err := t.s.State.Role(g.ID, roleID)
	if err != nil || r == nil {
		return nil
	}
	t.s.State.RLock()
	defer t.s.State.RUnlock()
	cp := *r
	return &cp
}

// cachedMember returns a member of guild g from the state cache, or nil.
func (t *Toolset) cachedMember(guildID, userID string) *discordgo.Member {
	if guildID == "" || userID == "" {
		return nil
	}
	m, err := t.s.State.Member(guildID, userID)
	if err != nil || m == nil {
		return nil
	}
	t.s.State.RLock()
	defer t.s.State.RUnlock()
	cp := *m
	return &cp
}

// member returns a guild member, from the cache when present and otherwise
// from the API (the result is added to the cache). With refresh it always
// asks the API. It returns nil when the user is not a member.
func (t *Toolset) member(guildID, userID string, refresh bool) *discordgo.Member {
	if !refresh {
		if m := t.cachedMember(guildID, userID); m != nil {
			return m
		}
	}
	m, err := t.s.GuildMember(guildID, userID)
	if err != nil || m == nil {
		return nil
	}
	if m.GuildID == "" {
		m.GuildID = guildID
	}
	_ = t.s.State.MemberAdd(m)
	return m
}

// guildMembers returns copies of the guild's cached members.
func (t *Toolset) guildMembers(g *discordgo.Guild) []*discordgo.Member {
	live, err := t.s.State.Guild(g.ID)
	if err != nil || live == nil {
		return nil
	}
	t.s.State.RLock()
	defer t.s.State.RUnlock()
	out := make([]*discordgo.Member, len(live.Members))
	for i, m := range live.Members {
		cp := *m
		out[i] = &cp
	}
	return out
}

// memberRoles returns the member's roles (without @everyone), highest first.
func (t *Toolset) memberRoles(g *discordgo.Guild, m *discordgo.Member) []*discordgo.Role {
	has := map[string]bool{}
	for _, id := range m.Roles {
		has[id] = true
	}
	var out []*discordgo.Role
	for _, r := range t.guildRoles(g) {
		if has[r.ID] {
			out = append(out, r)
		}
	}
	return out
}

// idLess compares two snowflakes numerically.
func idLess(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// jumpURL is the link to a message. Messages outside a guild use "@me".
func jumpURL(guildID, channelID, messageID string) string {
	if guildID == "" {
		guildID = "@me"
	}
	return "https://discord.com/channels/" + guildID + "/" + channelID + "/" + messageID
}
