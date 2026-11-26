package tools

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

// roleDefaultColorRaw is the raw color reported for a role without a color.
const roleDefaultColorRaw = 0x1FFFFFF

var (
	errRoleNotFound          = errors.New("Role not found by roleId")
	errRoleInvalidNumber     = errors.New("Invalid number format for color or permissions parameter")
	errRoleLacksManageRoles  = errors.New("Bot lacks permission to manage roles on this server")
	errRoleLacksCreateRoles  = errors.New("Bot lacks permission to create roles on this server")
	errRoleMemberNotFound    = errors.New("User not found in this server by userId")
	errRoleGuildIDNull       = errors.New("guildId cannot be null")
	errRoleRoleIDNull        = errors.New("roleId cannot be null")
	errRoleUserIDNull        = errors.New("userId cannot be null")
	errRoleNameNull          = errors.New("name cannot be null")
	errRoleDeleteManagedRole = errors.New("Cannot delete a Role that is managed. ")
)

func (t *Toolset) roleTools() []Tool {
	return []Tool{
		{
			Name:        "list_roles",
			Description: "Returns a list of all roles on the server along with their ID, names, colors, positions, and permissions",
			Params:      []Param{opt("guildId", "Discord server ID")},
			Handler:     t.listRoles,
		},
		{
			Name:        "create_role",
			Description: "Creates a new role on the server with specified parameters",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("name", "Name of the new role"),
				opt("color", "Color value as integer (e.g., 16711680 for red). Default is 0"),
				opt("hoist", "Whether the role should be displayed separately in the sidebar. Default is false"),
				opt("mentionable", "Whether the role should be mentionable. Default is false"),
				opt("permissions", "Permissions bitfield for the role as string. Default is 0"),
			},
			Handler: t.createRole,
		},
		{
			Name:        "edit_role",
			Description: "Updates settings of an existing role. All parameters except guild_id and role_id are optional",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("roleId", "ID of the role to edit"),
				opt("name", "New name for the role"),
				opt("color", "New color value as integer"),
				opt("hoist", "New hoist setting"),
				opt("mentionable", "New mentionable setting"),
				opt("permissions", "New permissions bitfield as string"),
			},
			Handler: t.editRole,
		},
		{
			Name:        "delete_role",
			Description: "Permanently deletes a role from the server",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("roleId", "ID of the role to delete"),
			},
			Handler: t.deleteRole,
		},
		{
			Name:        "assign_role",
			Description: "Assigns a specified role to a user",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("userId", "ID of the user to receive the role"),
				req("roleId", "ID of the role to assign"),
			},
			Handler: t.assignRole,
		},
		{
			Name:        "remove_role",
			Description: "Removes a specified role from a user",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("userId", "ID of the user from whom to remove the role"),
				req("roleId", "ID of the role to remove"),
			},
			Handler: t.removeRole,
		},
	}
}

// roleColorRaw is the role's color, or roleDefaultColorRaw when it has none.
func roleColorRaw(color int) int {
	if color == 0 {
		return roleDefaultColorRaw
	}
	return color
}

// roleColorText renders a role color as "#RRGGBB", or "None" without a color.
func roleColorText(color int) string {
	if color == 0 {
		return "None"
	}
	return fmt.Sprintf("#%06X", color&0xFFFFFF)
}

// roleHierarchyPositions maps role IDs to their hierarchy position: roles
// ordered highest first are numbered down to 0, and @everyone is -1.
func roleHierarchyPositions(roles []*discordgo.Role, everyoneID string) map[string]int {
	pos := make(map[string]int, len(roles))
	i := len(roles) - 2
	for _, r := range roles {
		if r.ID == everyoneID {
			pos[r.ID] = -1
		} else {
			pos[r.ID] = i
		}
		i--
	}
	return pos
}

// roleParsedSettings holds the optional numeric/boolean role settings.
type roleParsedSettings struct {
	color       *int
	hoist       *bool
	mentionable *bool
	permissions *int64
}

// roleParseSettings parses color, hoist, mentionable and permissions; only
// non-empty arguments are set. A bad number fails with errRoleInvalidNumber.
func roleParseSettings(a Args) (roleParsedSettings, error) {
	var s roleParsedSettings
	if v := a.Get("color"); v != "" {
		n, err := strconv.ParseInt(v, 10, 32)
		if err != nil {
			return s, errRoleInvalidNumber
		}
		c := int(n) & 0xFFFFFF
		s.color = &c
	}
	if v := a.Get("hoist"); v != "" {
		b := modParseBool(v)
		s.hoist = &b
	}
	if v := a.Get("mentionable"); v != "" {
		b := modParseBool(v)
		s.mentionable = &b
	}
	if v := a.Get("permissions"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return s, errRoleInvalidNumber
		}
		s.permissions = &n
	}
	return s, nil
}

// roleMissingPermissions returns the permission bits in perms that the bot
// does not have itself (0 when the bot's member is unknown).
func (t *Toolset) roleMissingPermissions(g *discordgo.Guild, perms int64) int64 {
	have, ok := t.modBotPermissions(g)
	if !ok {
		return 0
	}
	return perms &^ have
}

// roleSummary renders the result block shared by create_role and edit_role.
func roleSummary(prefix string, r *discordgo.Role) string {
	return fmt.Sprintf("%s: **%s** (ID: %s)\n"+
		"• Color: %d\n"+
		"• Hoisted: %s\n"+
		"• Mentionable: %s\n"+
		"• Permissions: %d",
		prefix, r.Name, r.ID, roleColorRaw(r.Color), boolText(r.Hoist), boolText(r.Mentionable), r.Permissions)
}

func (t *Toolset) listRoles(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	roles := t.guildRoles(g)
	if len(roles) == 0 {
		return "No roles found on this server.", nil
	}
	positions := roleHierarchyPositions(roles, g.ID)
	lines := make([]string, 0, len(roles))
	for _, r := range roles {
		lines = append(lines, fmt.Sprintf("- **%s** (ID: %s)\n"+
			"  • Color: %s (RGB: %d)\n"+
			"  • Position: %d\n"+
			"  • Hoisted: %s\n"+
			"  • Mentionable: %s\n"+
			"  • Permissions: %d",
			r.Name, r.ID, roleColorText(r.Color), roleColorRaw(r.Color), positions[r.ID],
			boolText(r.Hoist), boolText(r.Mentionable), r.Permissions))
	}
	return fmt.Sprintf("Retrieved %d roles:\n%s", len(roles), strings.Join(lines, "\n")), nil
}

func (t *Toolset) createRole(_ context.Context, a Args) (string, error) {
	guildID := t.resolveGuildID(a.Get("guildId"))
	if guildID == "" {
		return "", errRoleGuildIDNull
	}
	name := a.Get("name")
	if name == "" {
		return "", errRoleNameNull
	}
	g, err := t.guild(guildID, "guildId cannot be null")
	if err != nil {
		return "", err
	}
	settings, err := roleParseSettings(a)
	if err != nil {
		return "", err
	}
	if !t.modHasPermission(g, discordgo.PermissionManageRoles) {
		return "", errRoleLacksCreateRoles
	}

	color, hoist, mentionable, perms := 0, false, false, int64(0)
	if settings.color != nil {
		color = *settings.color
	}
	if settings.hoist != nil {
		hoist = *settings.hoist
	}
	if settings.mentionable != nil {
		mentionable = *settings.mentionable
	}
	if settings.permissions != nil {
		perms = *settings.permissions
	}
	if t.roleMissingPermissions(g, perms) != 0 {
		return "", errRoleLacksCreateRoles
	}

	role, err := t.s.GuildRoleCreate(g.ID, &discordgo.RoleParams{
		Name:        name,
		Color:       &color,
		Hoist:       &hoist,
		Mentionable: &mentionable,
		Permissions: &perms,
	})
	if err != nil {
		return "", err
	}
	return roleSummary("Successfully created role", role), nil
}

// roleTarget resolves the guild and the role for edit_role and delete_role.
func (t *Toolset) roleTarget(a Args) (*discordgo.Guild, *discordgo.Role, error) {
	guildID := t.resolveGuildID(a.Get("guildId"))
	if guildID == "" {
		return nil, nil, errRoleGuildIDNull
	}
	if a.Get("roleId") == "" {
		return nil, nil, errRoleRoleIDNull
	}
	g, err := t.guild(guildID, "guildId cannot be null")
	if err != nil {
		return nil, nil, err
	}
	role := t.cachedRole(g, a.Get("roleId"))
	if role == nil {
		return nil, nil, errRoleNotFound
	}
	return g, role, nil
}

func (t *Toolset) editRole(_ context.Context, a Args) (string, error) {
	g, role, err := t.roleTarget(a)
	if err != nil {
		return "", err
	}
	if role.ID == g.ID {
		return "", errors.New("Cannot edit the @everyone role directly. This operation is risky and restricted.")
	}

	params := &discordgo.RoleParams{Name: a.Get("name")}
	settings, err := roleParseSettings(a)
	if err != nil {
		return "", err
	}
	params.Color, params.Hoist, params.Mentionable, params.Permissions =
		settings.color, settings.hoist, settings.mentionable, settings.permissions
	// The manage-roles and hierarchy checks apply even when nothing changes.
	if !t.modHasPermission(g, discordgo.PermissionManageRoles) {
		return "", errRoleLacksManageRoles
	}
	if !t.modCanInteractRole(g, role) {
		return "", errors.New("Cannot manage this role - it is higher in the hierarchy than the bot's highest role")
	}
	// Granting a permission the bot lacks is refused, but bits the role
	// already has may be kept.
	if params.Permissions != nil && t.roleMissingPermissions(g, *params.Permissions&^role.Permissions) != 0 {
		return "", errRoleLacksManageRoles
	}

	updated := role
	if params.Name != "" || params.Color != nil || params.Hoist != nil || params.Mentionable != nil || params.Permissions != nil {
		if updated, err = t.s.GuildRoleEdit(g.ID, role.ID, params); err != nil {
			return "", err
		}
	}
	return roleSummary("Successfully updated role", updated), nil
}

func (t *Toolset) deleteRole(_ context.Context, a Args) (string, error) {
	g, role, err := t.roleTarget(a)
	if err != nil {
		return "", err
	}
	if role.ID == g.ID {
		return "", errors.New("Cannot delete the @everyone role")
	}
	if !t.modHasPermission(g, discordgo.PermissionManageRoles) {
		return "", errRoleLacksManageRoles
	}
	if !t.modCanInteractRole(g, role) {
		return "", errors.New("Cannot delete this role - it is higher in the hierarchy than the bot's highest role")
	}
	if role.Managed {
		return "", errRoleDeleteManagedRole
	}
	name := role.Name
	if err := t.s.GuildRoleDelete(g.ID, role.ID); err != nil {
		return "", err
	}
	return "Successfully deleted role: **" + name + "** (ID: " + a.Get("roleId") + ")", nil
}

// roleMemberTarget resolves the guild, member and role for assign_role and
// remove_role.
func (t *Toolset) roleMemberTarget(a Args) (*discordgo.Guild, *discordgo.Member, *discordgo.Role, error) {
	guildID := t.resolveGuildID(a.Get("guildId"))
	if guildID == "" {
		return nil, nil, nil, errRoleGuildIDNull
	}
	if a.Get("userId") == "" {
		return nil, nil, nil, errRoleUserIDNull
	}
	if a.Get("roleId") == "" {
		return nil, nil, nil, errRoleRoleIDNull
	}
	g, err := t.guild(guildID, "guildId cannot be null")
	if err != nil {
		return nil, nil, nil, err
	}
	m := t.member(g.ID, a.Get("userId"), false)
	if m == nil || m.User == nil {
		return nil, nil, nil, errRoleMemberNotFound
	}
	role := t.cachedRole(g, a.Get("roleId"))
	if role == nil {
		return nil, nil, nil, errRoleNotFound
	}
	return g, m, role, nil
}

func (t *Toolset) assignRole(_ context.Context, a Args) (string, error) {
	g, m, role, err := t.roleMemberTarget(a)
	if err != nil {
		return "", err
	}
	if role.ID == g.ID {
		return "", errors.New("Cannot assign the @everyone role - all members have it by default")
	}
	if !t.modHasPermission(g, discordgo.PermissionManageRoles) {
		return "", errRoleLacksManageRoles
	}
	if !t.modCanInteractRole(g, role) {
		return "", errors.New("Cannot assign this role - it is higher in the hierarchy than the bot's highest role")
	}
	if err := t.s.GuildMemberRoleAdd(g.ID, m.User.ID, role.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully assigned role **%s** to user **%s** (ID: %s)", role.Name, m.User.Username, m.User.ID), nil
}

func (t *Toolset) removeRole(_ context.Context, a Args) (string, error) {
	g, m, role, err := t.roleMemberTarget(a)
	if err != nil {
		return "", err
	}
	if role.ID == g.ID {
		return "", errors.New("Cannot remove the @everyone role - all members have it by default")
	}
	if !t.modHasPermission(g, discordgo.PermissionManageRoles) {
		return "", errRoleLacksManageRoles
	}
	if !t.modCanInteractRole(g, role) {
		return "", errors.New("Cannot remove this role - it is higher in the hierarchy than the bot's highest role")
	}
	if err := t.s.GuildMemberRoleRemove(g.ID, m.User.ID, role.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully removed role **%s** from user **%s** (ID: %s)", role.Name, m.User.Username, m.User.ID), nil
}
