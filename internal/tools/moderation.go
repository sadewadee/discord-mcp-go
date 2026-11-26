package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

var errModMemberNotFound = errors.New("User not found in this server by userId")

func (t *Toolset) moderationTools() []Tool {
	return []Tool{
		{
			Name:        "kick_member",
			Description: "Kicks a member from the server. The user can rejoin with a new invite.",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("userId", "ID of the user to kick"),
				opt("reason", "Reason for kicking (visible in audit log)"),
			},
			Handler: t.kickMember,
		},
		{
			Name:        "ban_member",
			Description: "Bans a user from the server, preventing them from rejoining. Optionally deletes their recent messages.",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("userId", "ID of the user to ban"),
				opt("deleteMessageSeconds", "Duration in seconds for which to delete the user's messages (max 604800 = 7 days). 0 = no deletion."),
				opt("reason", "Reason for the ban (visible in audit log)"),
			},
			Handler: t.banMember,
		},
		{
			Name:        "unban_member",
			Description: "Removes a ban from a user, allowing them to rejoin the server.",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("userId", "ID of the user to unban"),
				opt("reason", "Reason for removing the ban (visible in audit log)"),
			},
			Handler: t.unbanMember,
		},
		{
			Name:        "timeout_member",
			Description: "Disables communication for a member for a specified duration (Timeout/Mute). Max 28 days.",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("userId", "ID of the member to timeout"),
				req("durationSeconds", "Duration of the timeout in seconds (max 2419200 = 28 days)"),
				opt("reason", "Reason for the timeout (visible in audit log)"),
			},
			Handler: t.timeoutMember,
		},
		{
			Name:        "remove_timeout",
			Description: "Removes a timeout (unmute) from a member before it expires.",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("userId", "ID of the member to remove timeout from"),
				opt("reason", "Reason for removing the timeout (visible in audit log)"),
			},
			Handler: t.removeTimeout,
		},
		{
			Name:        "set_nickname",
			Description: "Changes a member's nickname on the server. Pass empty or null nick to reset to original username.",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("userId", "ID of the user"),
				opt("nick", "New nickname. Empty or null to reset to original username."),
				opt("reason", "Reason for the nickname change (visible in audit log)"),
			},
			Handler: t.setNickname,
		},
		{
			Name:        "get_bans",
			Description: "Returns a list of banned users on the server with ban reasons.",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				opt("limit", "Maximum number of results to return (default 50)"),
			},
			Handler: t.getBans,
		},
	}
}

// modParseInt32 parses a decimal 32-bit integer; on failure the error reads
// `For input string: "<s>"`.
func modParseInt32(s string) (int, error) {
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil {
		return 0, modNumberFormatError(s)
	}
	return int(n), nil
}

// modParseInt64 parses a decimal 64-bit integer; on failure the error reads
// `For input string: "<s>"`.
func modParseInt64(s string) (int64, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, modNumberFormatError(s)
	}
	return n, nil
}

func modNumberFormatError(s string) error {
	return fmt.Errorf("For input string: \"%s\"", s)
}

// modParseBool is true only for "true", in any letter case.
func modParseBool(s string) bool { return strings.EqualFold(s, "true") }

// modReasonSuffix is " Reason: <reason>" when a reason was sent, else "".
func modReasonSuffix(a Args) string {
	if a.Has("reason") {
		return " Reason: " + a.Get("reason")
	}
	return ""
}

// modReasonOpts adds the audit-log reason header when the reason is not empty.
func modReasonOpts(reason string) []discordgo.RequestOption {
	if reason == "" {
		return nil
	}
	return []discordgo.RequestOption{discordgo.WithAuditLogReason(reason)}
}

// modSelfMember returns the bot's own member in guild g, or nil.
func (t *Toolset) modSelfMember(g *discordgo.Guild) *discordgo.Member {
	if t.s.State == nil || t.s.State.User == nil {
		return nil
	}
	return t.member(g.ID, t.s.State.User.ID, false)
}

// modAllPermissions has every permission bit set; the guild owner and
// administrators hold all permissions, including ones added after this was
// written.
const modAllPermissions = ^int64(0)

// modGuildPermissions returns the member's guild-wide permissions: everything
// for the owner or an administrator, otherwise the union of @everyone and the
// member's roles.
func (t *Toolset) modGuildPermissions(g *discordgo.Guild, m *discordgo.Member) int64 {
	if m.User != nil && m.User.ID == g.OwnerID {
		return modAllPermissions
	}
	var perms int64
	if everyone := t.cachedRole(g, g.ID); everyone != nil {
		perms |= everyone.Permissions
	}
	for _, r := range t.memberRoles(g, m) {
		perms |= r.Permissions
	}
	if perms&discordgo.PermissionAdministrator != 0 {
		return modAllPermissions
	}
	return perms
}

// modBotPermissions returns the bot's guild-wide permissions, and false when
// the bot's own member is unknown (the check is then left to Discord).
func (t *Toolset) modBotPermissions(g *discordgo.Guild) (int64, bool) {
	self := t.modSelfMember(g)
	if self == nil {
		return 0, false
	}
	return t.modGuildPermissions(g, self), true
}

// modHasPermission reports whether the bot has perm in guild g. When the
// bot's own member is unknown it answers true and leaves the check to Discord.
func (t *Toolset) modHasPermission(g *discordgo.Guild, perm int64) bool {
	perms, ok := t.modBotPermissions(g)
	return !ok || perms&perm == perm
}

// modRoleAbove reports whether role a sits above role b in the hierarchy:
// a higher position, or the same position and an older ID.
func modRoleAbove(a, b *discordgo.Role) bool {
	if a.Position != b.Position {
		return a.Position > b.Position
	}
	return idLess(a.ID, b.ID)
}

// modTopRole returns the member's highest role, or nil when it has none.
func (t *Toolset) modTopRole(g *discordgo.Guild, m *discordgo.Member) *discordgo.Role {
	if roles := t.memberRoles(g, m); len(roles) > 0 {
		return roles[0]
	}
	return nil
}

// modCanInteractRole reports whether the bot may manage role: the owner can
// manage every role, anyone else only roles below their highest role.
func (t *Toolset) modCanInteractRole(g *discordgo.Guild, role *discordgo.Role) bool {
	self := t.modSelfMember(g)
	if self == nil {
		return true
	}
	if self.User != nil && self.User.ID == g.OwnerID {
		return true
	}
	top := t.modTopRole(g, self)
	return top != nil && modRoleAbove(top, role)
}

// modCanInteractMember reports whether the bot outranks target: the owner
// outranks everyone, nobody outranks the owner, and otherwise the bot's
// highest role must be above the target's highest role.
func (t *Toolset) modCanInteractMember(g *discordgo.Guild, target *discordgo.Member) bool {
	self := t.modSelfMember(g)
	if self == nil || target == nil || target.User == nil {
		return true
	}
	if self.User != nil && self.User.ID == g.OwnerID {
		return true
	}
	if target.User.ID == g.OwnerID {
		return false
	}
	selfTop := t.modTopRole(g, self)
	if selfTop == nil {
		return false
	}
	targetTop := t.modTopRole(g, target)
	return targetTop == nil || modRoleAbove(selfTop, targetTop)
}

// modTarget resolves the guild and the target member.
func (t *Toolset) modTarget(a Args) (*discordgo.Guild, *discordgo.Member, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return nil, nil, err
	}
	userID := a.Get("userId")
	if userID == "" {
		return nil, nil, errors.New("userId cannot be null")
	}
	m := t.member(g.ID, userID, false)
	if m == nil || m.User == nil {
		return nil, nil, errModMemberNotFound
	}
	return g, m, nil
}

func (t *Toolset) kickMember(_ context.Context, a Args) (string, error) {
	g, m, err := t.modTarget(a)
	if err != nil {
		return "", err
	}
	if !t.modHasPermission(g, discordgo.PermissionKickMembers) {
		return "", errors.New("Bot lacks permission to kick members")
	}
	if !t.modCanInteractMember(g, m) {
		return "", errors.New("Cannot kick this user - they have a higher or equal role than the bot")
	}
	if err := t.s.GuildMemberDelete(g.ID, m.User.ID, modReasonOpts(a.Get("reason"))...); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully kicked user **%s** (ID: %s).%s", m.User.Username, a.Get("userId"), modReasonSuffix(a)), nil
}

func (t *Toolset) banMember(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	userID := a.Get("userId")
	if userID == "" {
		return "", errors.New("userId cannot be null")
	}
	deleteSeconds := 0
	if s := a.Get("deleteMessageSeconds"); s != "" {
		if deleteSeconds, err = modParseInt32(s); err != nil {
			return "", err
		}
		if deleteSeconds < 0 || deleteSeconds > 604800 {
			return "", errors.New("deleteMessageSeconds must be between 0 and 604800 (7 days)")
		}
	}
	if !t.modHasPermission(g, discordgo.PermissionBanMembers) {
		return "", errors.New("Bot lacks permission to ban members")
	}
	if userID == g.OwnerID {
		return "", errors.New("Cannot ban this user - they have a higher or equal role than the bot")
	}
	if target := t.cachedMember(g.ID, userID); target != nil && !t.modCanInteractMember(g, target) {
		return "", errors.New("Cannot ban this user - they have a higher or equal role than the bot")
	}
	body := map[string]any{"delete_message_seconds": deleteSeconds}
	if _, err := t.s.RequestWithBucketID("PUT", discordgo.EndpointGuildBan(g.ID, userID), body,
		discordgo.EndpointGuildBan(g.ID, ""), modReasonOpts(a.Get("reason"))...); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully banned user ID **%s**.%s", userID, modReasonSuffix(a)), nil
}

func (t *Toolset) unbanMember(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	userID := a.Get("userId")
	if userID == "" {
		return "", errors.New("userId cannot be null")
	}
	if !t.modHasPermission(g, discordgo.PermissionBanMembers) {
		return "", errors.New("Bot lacks permission to unban members")
	}
	if err := t.s.GuildBanDelete(g.ID, userID, modReasonOpts(a.Get("reason"))...); err != nil {
		var rest *discordgo.RESTError
		if errors.As(err, &rest) {
			return "", errors.New("User is not banned or could not be found")
		}
		return "", err
	}
	return fmt.Sprintf("Successfully unbanned user ID **%s**.%s", userID, modReasonSuffix(a)), nil
}

func (t *Toolset) timeoutMember(_ context.Context, a Args) (string, error) {
	g, m, err := t.modTarget(a)
	if err != nil {
		return "", err
	}
	raw := a.Get("durationSeconds")
	if raw == "" {
		return "", errors.New("durationSeconds cannot be null")
	}
	duration, err := modParseInt64(raw)
	if err != nil {
		return "", err
	}
	if duration <= 0 || duration > 2419200 {
		return "", errors.New("durationSeconds must be between 1 and 2419200 (28 days)")
	}
	if !t.modHasPermission(g, discordgo.PermissionModerateMembers) {
		return "", errors.New("Bot lacks permission to timeout members")
	}
	if !t.modCanInteractMember(g, m) {
		return "", errors.New("Cannot timeout this user - they have a higher or equal role than the bot")
	}
	until := time.Now().Add(time.Duration(duration) * time.Second)
	if err := t.s.GuildMemberTimeout(g.ID, m.User.ID, &until, modReasonOpts(a.Get("reason"))...); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully timed out user **%s** (ID: %s) for %d seconds.%s",
		m.User.Username, a.Get("userId"), duration, modReasonSuffix(a)), nil
}

func (t *Toolset) removeTimeout(_ context.Context, a Args) (string, error) {
	g, m, err := t.modTarget(a)
	if err != nil {
		return "", err
	}
	if !t.modHasPermission(g, discordgo.PermissionModerateMembers) {
		return "", errors.New("Bot lacks permission to moderate members")
	}
	if !t.modCanInteractMember(g, m) {
		return "", errors.New("Cannot remove timeout - user has a higher or equal role than the bot")
	}
	if err := t.s.GuildMemberTimeout(g.ID, m.User.ID, nil, modReasonOpts(a.Get("reason"))...); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully removed timeout from user **%s** (ID: %s).%s",
		m.User.Username, a.Get("userId"), modReasonSuffix(a)), nil
}

func (t *Toolset) setNickname(_ context.Context, a Args) (string, error) {
	g, m, err := t.modTarget(a)
	if err != nil {
		return "", err
	}
	nick := a.Get("nick")

	isSelf := t.s.State != nil && t.s.State.User != nil && m.User.ID == t.s.State.User.ID
	if isSelf {
		if !t.modHasPermission(g, discordgo.PermissionChangeNickname) && !t.modHasPermission(g, discordgo.PermissionManageNicknames) {
			return "", errors.New("Bot lacks permission to change nicknames")
		}
	} else {
		if !t.modHasPermission(g, discordgo.PermissionManageNicknames) {
			return "", errors.New("Bot lacks permission to change nicknames")
		}
		if !t.modCanInteractMember(g, m) {
			return "", errors.New("Cannot change nickname - user has a higher or equal role than the bot")
		}
	}

	target := m.User.ID
	if isSelf {
		target = "@me"
	}
	if err := t.s.GuildMemberNickname(g.ID, target, nick, modReasonOpts(a.Get("reason"))...); err != nil {
		return "", err
	}
	action := "set"
	if nick == "" {
		action = "reset"
	}
	return fmt.Sprintf("Successfully %s nickname of user **%s** (ID: %s).%s",
		action, m.User.Username, a.Get("userId"), modReasonSuffix(a)), nil
}

func (t *Toolset) getBans(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	maxResults := 50
	if s := a.Get("limit"); s != "" {
		if maxResults, err = modParseInt32(s); err != nil {
			return "", err
		}
		if maxResults <= 0 {
			return "", errors.New("limit must be a positive integer")
		}
	}
	if !t.modHasPermission(g, discordgo.PermissionBanMembers) {
		return "", errors.New("Bot lacks permission to view ban list")
	}
	bans, err := t.s.GuildBans(g.ID, 1000, "", "")
	if err != nil {
		return "", err
	}
	if len(bans) == 0 {
		return "No banned users found on this server.", nil
	}
	limited := bans
	if len(limited) > maxResults {
		limited = limited[:maxResults]
	}
	lines := make([]string, 0, len(limited))
	for _, b := range limited {
		var name, id string
		if b.User != nil {
			name, id = b.User.Username, b.User.ID
		}
		reason := b.Reason
		if reason == "" {
			reason = "No reason provided"
		}
		lines = append(lines, fmt.Sprintf("- **%s** (ID: %s) — Reason: %s", name, id, reason))
	}
	return fmt.Sprintf("Retrieved %d of %d bans:\n%s", len(limited), len(bans), strings.Join(lines, "\n")), nil
}
