package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// permissionDef is one named Discord permission.
type permissionDef struct {
	Name    string // name accepted in CSV permission lists
	Display string // human-readable name used in results
	Bit     uint
	// ParseOnly marks an alternative name for a bit that is already listed;
	// it is accepted as input but not shown in results.
	ParseOnly bool
}

// permissionDefs lists the permissions in display order. A raw value is
// rendered by walking this list, so a bit with two names (Manage Roles and
// Manage Permissions) shows both.
var permissionDefs = []permissionDef{
	// General server and channel permissions
	{Name: "MANAGE_CHANNEL", Display: "Manage Channels", Bit: 4},
	{Name: "MANAGE_SERVER", Display: "Manage Server", Bit: 5},
	{Name: "VIEW_AUDIT_LOGS", Display: "View Audit Log", Bit: 7},
	{Name: "VIEW_CHANNEL", Display: "View Channels", Bit: 10},
	{Name: "VIEW_GUILD_INSIGHTS", Display: "View Server Insights", Bit: 19},
	{Name: "MANAGE_ROLES", Display: "Manage Roles", Bit: 28},
	{Name: "MANAGE_PERMISSIONS", Display: "Manage Permissions", Bit: 28},
	{Name: "MANAGE_WEBHOOKS", Display: "Manage Webhooks", Bit: 29},
	{Name: "MANAGE_GUILD_EXPRESSIONS", Display: "Manage Expressions", Bit: 30},
	{Name: "MANAGE_EVENTS", Display: "Manage Events", Bit: 33},
	{Name: "USE_EMBEDDED_ACTIVITIES", Display: "Use Activities", Bit: 39},
	{Name: "VIEW_CREATOR_MONETIZATION_ANALYTICS", Display: "View Creator Analytics", Bit: 41},
	{Name: "CREATE_GUILD_EXPRESSIONS", Display: "Create Expressions", Bit: 43},
	{Name: "CREATE_SCHEDULED_EVENTS", Display: "Create Events", Bit: 44},

	// Membership permissions
	{Name: "CREATE_INSTANT_INVITE", Display: "Create Instant Invite", Bit: 0},
	{Name: "KICK_MEMBERS", Display: "Kick Members", Bit: 1},
	{Name: "BAN_MEMBERS", Display: "Ban Members", Bit: 2},
	{Name: "NICKNAME_CHANGE", Display: "Change Nickname", Bit: 26},
	{Name: "NICKNAME_MANAGE", Display: "Manage Nicknames", Bit: 27},
	{Name: "MODERATE_MEMBERS", Display: "Timeout Members", Bit: 40},

	// Text permissions
	{Name: "MESSAGE_ADD_REACTION", Display: "Add Reactions", Bit: 6},
	{Name: "MESSAGE_SEND", Display: "Send Messages", Bit: 11},
	{Name: "MESSAGE_TTS", Display: "Send TTS Messages", Bit: 12},
	{Name: "MESSAGE_MANAGE", Display: "Manage Messages", Bit: 13},
	{Name: "MESSAGE_EMBED_LINKS", Display: "Embed Links", Bit: 14},
	{Name: "MESSAGE_ATTACH_FILES", Display: "Attach Files", Bit: 15},
	{Name: "MESSAGE_HISTORY", Display: "Read History", Bit: 16},
	{Name: "MESSAGE_MENTION_EVERYONE", Display: "Mention Everyone", Bit: 17},
	{Name: "MESSAGE_EXT_EMOJI", Display: "Use External Emojis", Bit: 18},
	{Name: "USE_APPLICATION_COMMANDS", Display: "Use Application Commands", Bit: 31},
	{Name: "MESSAGE_EXT_STICKER", Display: "Use External Stickers", Bit: 37},
	{Name: "MESSAGE_ATTACH_VOICE_MESSAGE", Display: "Send Voice Messages", Bit: 46},
	{Name: "MESSAGE_SEND_POLLS", Display: "Create Polls", Bit: 49},
	{Name: "USE_EXTERNAL_APPLICATIONS", Display: "Use External Apps", Bit: 50},
	{Name: "PIN_MESSAGES", Display: "Pin Messages", Bit: 51},
	{Name: "BYPASS_SLOWMODE", Display: "Bypass Slowmode", Bit: 52},

	// Thread permissions
	{Name: "MANAGE_THREADS", Display: "Manage Threads", Bit: 34},
	{Name: "CREATE_PUBLIC_THREADS", Display: "Create Public Threads", Bit: 35},
	{Name: "CREATE_PRIVATE_THREADS", Display: "Create Private Threads", Bit: 36},
	{Name: "MESSAGE_SEND_IN_THREADS", Display: "Send Messages in Threads", Bit: 38},

	// Voice permissions
	{Name: "PRIORITY_SPEAKER", Display: "Priority Speaker", Bit: 8},
	{Name: "VOICE_STREAM", Display: "Video", Bit: 9},
	{Name: "VOICE_CONNECT", Display: "Connect", Bit: 20},
	{Name: "VOICE_SPEAK", Display: "Speak", Bit: 21},
	{Name: "VOICE_MUTE_OTHERS", Display: "Mute Members", Bit: 22},
	{Name: "VOICE_DEAF_OTHERS", Display: "Deafen Members", Bit: 23},
	{Name: "VOICE_MOVE_OTHERS", Display: "Move Members", Bit: 24},
	{Name: "VOICE_USE_VAD", Display: "Use Voice Activity", Bit: 25},
	{Name: "VOICE_USE_SOUNDBOARD", Display: "Use Soundboard", Bit: 42},
	{Name: "VOICE_USE_EXTERNAL_SOUNDS", Display: "Use External Sounds", Bit: 45},
	{Name: "VOICE_SET_STATUS", Display: "Set Voice Channel Status", Bit: 48},

	// Stage channel permissions
	{Name: "REQUEST_TO_SPEAK", Display: "Request to Speak", Bit: 32},

	// Advanced
	{Name: "ADMINISTRATOR", Display: "Administrator", Bit: 3},
}

const (
	permBitAdministrator = int64(1) << 3
	permBitManageRoles   = int64(1) << 28
)

// permissionByName finds a permission by its exact (case-sensitive) name.
func permissionByName(name string) (permissionDef, bool) {
	for _, p := range permissionDefs {
		if p.Name == name {
			return p, true
		}
	}
	return permissionDef{}, false
}

// parsePermissions reads one allow or deny value, given either as a raw
// bitfield or as a comma-separated list of permission names (not both).
func parsePermissions(raw, names string) (int64, error) {
	if raw != "" && names != "" {
		return 0, errors.New("Cannot specify both raw bitfield and permission names for the same field")
	}
	if raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, errors.New("Invalid permission bitfield: " + raw)
		}
		return n, nil
	}
	if names != "" {
		var result int64
		parts := strings.Split(names, ",")
		// Like a regex split, trailing empty entries ("A,B,") are dropped.
		for len(parts) > 0 && parts[len(parts)-1] == "" {
			parts = parts[:len(parts)-1]
		}
		for _, name := range parts {
			p, ok := permissionByName(strings.TrimSpace(name))
			if !ok {
				return 0, errors.New("Invalid permission name: " + strings.TrimSpace(name))
			}
			result |= int64(1) << p.Bit
		}
		return result, nil
	}
	return 0, nil
}

// formatPermissions renders a raw value as "none" or a comma-separated list
// of permission display names.
func formatPermissions(raw int64) string {
	if raw == 0 {
		return "none"
	}
	var names []string
	for _, p := range permissionDefs {
		if !p.ParseOnly && raw&(int64(1)<<p.Bit) != 0 {
			names = append(names, p.Display)
		}
	}
	return strings.Join(names, ", ")
}

func (t *Toolset) channelPermissionTools() []Tool {
	upsertParams := func(targetParam Param, allowExample, denyExample string) []Param {
		return []Param{
			opt("guildId", "Discord server ID"),
			req("channelId", "Channel ID"),
			targetParam,
			opt("allowRaw", "Allow permissions raw bitfield"),
			opt("denyRaw", "Deny permissions raw bitfield"),
			opt("allowPermissions", "Allow permissions as CSV of names (e.g. "+allowExample+")"),
			opt("denyPermissions", "Deny permissions as CSV of names (e.g. "+denyExample+")"),
			opt("reason", "Reason for audit log"),
		}
	}
	return []Tool{
		{
			Name:        "list_channel_permission_overwrites",
			Description: "Returns all permission overwrites for a channel with role/member breakdown and allow/deny details",
			Params:      []Param{opt("guildId", "Discord server ID"), req("channelId", "Channel ID")},
			Handler:     t.listChannelPermissionOverwrites,
		},
		{
			Name:        "upsert_role_channel_permissions",
			Description: "Creates or updates permission overwrite for a role on a channel",
			Params:      upsertParams(req("roleId", "Role ID"), "VIEW_CHANNEL,MESSAGE_SEND", "MESSAGE_SEND,MANAGE_MESSAGES"),
			Handler:     t.upsertRoleChannelPermissions,
		},
		{
			Name:        "upsert_member_channel_permissions",
			Description: "Creates or updates permission overwrite for a member on a channel",
			Params:      upsertParams(req("userId", "User ID"), "VIEW_CHANNEL,VOICE_CONNECT", "MESSAGE_SEND,VOICE_SPEAK"),
			Handler:     t.upsertMemberChannelPermissions,
		},
		{
			Name:        "delete_channel_permission_overwrite",
			Description: "Deletes a permission overwrite for a role or member from a channel",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("channelId", "Channel ID"),
				req("targetType", "Target type: 'role' or 'member'"),
				req("targetId", "Target role or user ID"),
				opt("reason", "Reason for audit log"),
			},
			Handler: t.deleteChannelPermissionOverwrite,
		},
	}
}

// permissionContainer returns a guild channel that can hold permission
// overwrites (any channel except a thread).
func (t *Toolset) permissionContainer(g *discordgo.Guild, channelID string) (*discordgo.Channel, error) {
	if channelID == "" {
		return nil, errors.New("channelId cannot be null")
	}
	c := t.guildChannel(g, channelID)
	if c == nil {
		return nil, errChannelNotFound
	}
	if c.IsThread() {
		return nil, errors.New("Channel type does not support permission overwrites (threads are not supported)")
	}
	return c, nil
}

// overwriteTargetName names the role or member an overwrite applies to.
func (t *Toolset) overwriteTargetName(g *discordgo.Guild, o *discordgo.PermissionOverwrite) string {
	if o.Type == discordgo.PermissionOverwriteTypeRole {
		if r := t.cachedRole(g, o.ID); r != nil {
			return r.Name
		}
		return "Unknown Role"
	}
	if m := t.cachedMember(g.ID, o.ID); m != nil && m.User != nil {
		return m.User.Username
	}
	return "Unknown Member"
}

// botChannelPermissions returns the bot's effective permissions in a channel,
// and false when they cannot be computed from the cache.
func (t *Toolset) botChannelPermissions(channelID string) (int64, bool) {
	if t.s.State.User == nil {
		return 0, false
	}
	perms, err := t.s.State.UserChannelPermissions(t.s.State.User.ID, channelID)
	if err != nil {
		return 0, false
	}
	return perms, true
}

var errBotLacksChannelPermissions = errors.New("Bot lacks permission to manage channel permissions")

// checkCanManageOverwrites verifies the bot may edit overwrites on the
// channel. Which bits it may grant or deny depends on existing overwrites
// and is left to Discord; see overwriteError.
func (t *Toolset) checkCanManageOverwrites(channelID string) error {
	perms, ok := t.botChannelPermissions(channelID)
	if !ok || perms&permBitAdministrator != 0 {
		return nil
	}
	if perms&permBitManageRoles == 0 {
		return errBotLacksChannelPermissions
	}
	return nil
}

// overwriteError reports a Discord missing-permissions answer to an
// overwrite change with the same message as the local check.
func overwriteError(err error) error {
	var rest *discordgo.RESTError
	if errors.As(err, &rest) && rest.Message != nil &&
		(rest.Message.Code == discordgo.ErrCodeMissingPermissions || rest.Message.Code == discordgo.ErrCodeMissingAccess) {
		return errBotLacksChannelPermissions
	}
	return err
}

func (t *Toolset) listChannelPermissionOverwrites(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	c, err := t.permissionContainer(g, a.Get("channelId"))
	if err != nil {
		return "", err
	}
	if len(c.PermissionOverwrites) == 0 {
		return "No permission overwrites found for this channel.", nil
	}
	lines := make([]string, 0, len(c.PermissionOverwrites))
	for _, o := range c.PermissionOverwrites {
		kind := "Member"
		if o.Type == discordgo.PermissionOverwriteTypeRole {
			kind = "Role"
		}
		lines = append(lines, fmt.Sprintf("- **%s: %s** (ID: %s)\n  • Allow: %d (%s)\n  • Deny: %d (%s)",
			kind, t.overwriteTargetName(g, o), o.ID,
			o.Allow, formatPermissions(o.Allow), o.Deny, formatPermissions(o.Deny)))
	}
	return fmt.Sprintf("Retrieved %d permission overwrites:\n%s", len(lines), strings.Join(lines, "\n")), nil
}

// upsertOverwrite sets the allow/deny values of an overwrite, replacing any
// existing values for that target.
func (t *Toolset) upsertOverwrite(c *discordgo.Channel, targetType discordgo.PermissionOverwriteType,
	typeLabel, targetName, targetID string, a Args) (string, error) {
	allow, err := parsePermissions(a.Get("allowRaw"), a.Get("allowPermissions"))
	if err != nil {
		return "", err
	}
	deny, err := parsePermissions(a.Get("denyRaw"), a.Get("denyPermissions"))
	if err != nil {
		return "", err
	}
	if allow == 0 && deny == 0 {
		return "", errors.New("Must specify at least one allow or deny permission")
	}
	if allow&deny != 0 {
		return "", errors.New("allow and deny cannot contain the same permission bits")
	}
	if err := t.checkCanManageOverwrites(c.ID); err != nil {
		return "", err
	}
	if err := t.s.ChannelPermissionSet(c.ID, targetID, targetType, allow, deny, reasonOptions(a.Get("reason"))...); err != nil {
		return "", overwriteError(err)
	}
	return fmt.Sprintf("Successfully set permission overwrite for %s **%s** (ID: %s):\n• Allow: %d (%s)\n• Deny: %d (%s)",
		typeLabel, targetName, targetID, allow, formatPermissions(allow), deny, formatPermissions(deny)), nil
}

func (t *Toolset) upsertRoleChannelPermissions(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	c, err := t.permissionContainer(g, a.Get("channelId"))
	if err != nil {
		return "", err
	}
	if err := requireArgs(a, "roleId"); err != nil {
		return "", err
	}
	role := t.cachedRole(g, a.Get("roleId"))
	if role == nil {
		return "", errors.New("Role not found by roleId")
	}
	return t.upsertOverwrite(c, discordgo.PermissionOverwriteTypeRole, "role", role.Name, role.ID, a)
}

func (t *Toolset) upsertMemberChannelPermissions(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	c, err := t.permissionContainer(g, a.Get("channelId"))
	if err != nil {
		return "", err
	}
	if err := requireArgs(a, "userId"); err != nil {
		return "", err
	}
	m := t.member(g.ID, a.Get("userId"), false)
	if m == nil || m.User == nil {
		return "", errors.New("User not found in this server by userId")
	}
	return t.upsertOverwrite(c, discordgo.PermissionOverwriteTypeMember, "member", m.User.Username, m.User.ID, a)
}

func (t *Toolset) deleteChannelPermissionOverwrite(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	c, err := t.permissionContainer(g, a.Get("channelId"))
	if err != nil {
		return "", err
	}
	if err := requireArgs(a, "targetType", "targetId"); err != nil {
		return "", err
	}
	targetType, targetID := a.Get("targetType"), a.Get("targetId")
	if targetType != "role" && targetType != "member" {
		return "", errors.New("targetType must be 'role' or 'member'")
	}
	var override *discordgo.PermissionOverwrite
	for _, o := range c.PermissionOverwrites {
		if o.ID == targetID {
			override = o
			break
		}
	}
	if override == nil {
		return "", errors.New("No permission overwrite found for target ID: " + targetID)
	}
	isRole := override.Type == discordgo.PermissionOverwriteTypeRole
	if targetType == "role" && !isRole {
		return "", errors.New("Target ID refers to a member overwrite, not a role overwrite")
	}
	if targetType == "member" && isRole {
		return "", errors.New("Target ID refers to a role overwrite, not a member overwrite")
	}
	targetName := t.overwriteTargetName(g, override)
	if err := t.checkCanManageOverwrites(c.ID); err != nil {
		return "", err
	}
	if err := t.s.ChannelPermissionDelete(c.ID, targetID, reasonOptions(a.Get("reason"))...); err != nil {
		return "", overwriteError(err)
	}
	return fmt.Sprintf("Successfully deleted permission overwrite for %s **%s** (ID: %s) from channel", targetType, targetName, targetID), nil
}
