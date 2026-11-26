package tools

import "testing"

func TestInviteExtractCode(t *testing.T) {
	tests := []struct{ in, want, err string }{
		{in: "ABCde", want: "ABCde"},
		{in: "https://discord.gg/ABCde", want: "ABCde"},
		{in: "http://discord.gg/ABCde", want: "ABCde"},
		{in: "https://discord.com/invite/ABCde", want: "ABCde"},
		{in: "http://discord.com/invite/ABCde ", want: "ABCde"},
		{in: "  ABCde  ", want: "ABCde"},
		{in: "", err: "invite_code cannot be null"},
	}
	for _, tt := range tests {
		got, err := inviteExtractCode(tt.in)
		if tt.err != "" {
			if err == nil || err.Error() != tt.err {
				t.Errorf("inviteExtractCode(%q) error = %v, want %q", tt.in, err, tt.err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("inviteExtractCode(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestInviteParseHelpers(t *testing.T) {
	if n, err := inviteParseInt("3600"); err != nil || n != 3600 {
		t.Errorf("inviteParseInt(3600) = %d, %v", n, err)
	}
	if _, err := inviteParseInt("1h"); err == nil || err.Error() != `For input string: "1h"` {
		t.Errorf("inviteParseInt(1h) error = %v", err)
	}
	for in, want := range map[string]bool{"true": true, "TRUE": true, "True": true, "yes": false, "1": false, "false": false} {
		if got := inviteParseBool(in); got != want {
			t.Errorf("inviteParseBool(%q) = %v, want %v", in, got, want)
		}
	}
	if got := inviteMaxUses(0); got != "Unlimited" {
		t.Errorf("inviteMaxUses(0) = %q", got)
	}
	if got := inviteMaxUses(5); got != "5" {
		t.Errorf("inviteMaxUses(5) = %q", got)
	}
}
