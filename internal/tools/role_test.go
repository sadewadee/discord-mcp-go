package tools

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestRoleColor(t *testing.T) {
	tests := []struct {
		color int
		text  string
		raw   int
	}{
		{0, "None", 33554431},
		{16711680, "#FF0000", 16711680},
		{0x00AB12, "#00AB12", 0x00AB12},
		{1, "#000001", 1},
	}
	for _, tt := range tests {
		if got := roleColorText(tt.color); got != tt.text {
			t.Errorf("roleColorText(%d) = %q, want %q", tt.color, got, tt.text)
		}
		if got := roleColorRaw(tt.color); got != tt.raw {
			t.Errorf("roleColorRaw(%d) = %d, want %d", tt.color, got, tt.raw)
		}
	}
}

func TestRoleHierarchyPositions(t *testing.T) {
	// Highest first, @everyone last, as guildRoles returns them.
	roles := []*discordgo.Role{
		{ID: "3", Position: 3},
		{ID: "2", Position: 2},
		{ID: "4", Position: 1},
		{ID: "1", Position: 0}, // @everyone (same ID as the guild)
	}
	got := roleHierarchyPositions(roles, "1")
	want := map[string]int{"3": 2, "2": 1, "4": 0, "1": -1}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("position of %s = %d, want %d", id, got[id], w)
		}
	}
}

func TestRoleParseSettings(t *testing.T) {
	str := func(s string) *string { return &s }

	s, err := roleParseSettings(Args{
		"color":       str("16711680"),
		"hoist":       str("TRUE"),
		"mentionable": str("nope"),
		"permissions": str("8"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.color == nil || *s.color != 16711680 {
		t.Errorf("color = %v", s.color)
	}
	if s.hoist == nil || !*s.hoist {
		t.Errorf("hoist = %v", s.hoist)
	}
	if s.mentionable == nil || *s.mentionable {
		t.Errorf("mentionable = %v", s.mentionable)
	}
	if s.permissions == nil || *s.permissions != 8 {
		t.Errorf("permissions = %v", s.permissions)
	}

	// Colors keep only the low 24 bits.
	s, err = roleParseSettings(Args{"color": str("-1")})
	if err != nil || s.color == nil || *s.color != 0xFFFFFF {
		t.Errorf("color -1 = %v, %v; want 0xFFFFFF", s.color, err)
	}

	empty, err := roleParseSettings(Args{"color": str(""), "permissions": str("")})
	if err != nil || empty.color != nil || empty.permissions != nil || empty.hoist != nil || empty.mentionable != nil {
		t.Errorf("empty args should leave every setting unset, got %+v, %v", empty, err)
	}

	for _, bad := range []Args{{"color": str("red")}, {"permissions": str("1.5")}, {"color": str("99999999999")}} {
		if _, err := roleParseSettings(bad); err != errRoleInvalidNumber {
			t.Errorf("roleParseSettings(%v) error = %v, want %v", bad, err, errRoleInvalidNumber)
		}
	}
}

func TestRoleSummary(t *testing.T) {
	r := &discordgo.Role{ID: "42", Name: "Mods", Color: 0, Hoist: true, Mentionable: false, Permissions: 8}
	want := "Successfully created role: **Mods** (ID: 42)\n" +
		"• Color: 33554431\n" +
		"• Hoisted: true\n" +
		"• Mentionable: false\n" +
		"• Permissions: 8"
	if got := roleSummary("Successfully created role", r); got != want {
		t.Errorf("roleSummary =\n%s\nwant\n%s", got, want)
	}
}
