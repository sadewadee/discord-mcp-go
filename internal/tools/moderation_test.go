package tools

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestModParseInt32(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr string
	}{
		{"0", 0, ""},
		{"604800", 604800, ""},
		{"-5", -5, ""},
		{"+7", 7, ""},
		{"abc", 0, `For input string: "abc"`},
		{" 5", 0, `For input string: " 5"`},
		{"2147483648", 0, `For input string: "2147483648"`},
	}
	for _, tt := range tests {
		got, err := modParseInt32(tt.in)
		if tt.wantErr != "" {
			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("modParseInt32(%q) error = %v, want %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("modParseInt32(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
		}
	}
}

func TestModParseInt64(t *testing.T) {
	if n, err := modParseInt64("2419200"); err != nil || n != 2419200 {
		t.Fatalf("modParseInt64 = %d, %v", n, err)
	}
	if _, err := modParseInt64("1.5"); err == nil || err.Error() != `For input string: "1.5"` {
		t.Fatalf("modParseInt64(1.5) error = %v", err)
	}
}

func TestModParseBool(t *testing.T) {
	for in, want := range map[string]bool{"true": true, "TRUE": true, "True": true, "false": false, "yes": false, "1": false, "": false} {
		if got := modParseBool(in); got != want {
			t.Errorf("modParseBool(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestModReasonSuffix(t *testing.T) {
	empty, spam := "", "spam"
	tests := []struct {
		args Args
		want string
	}{
		{Args{}, ""},
		{Args{"reason": &spam}, " Reason: spam"},
		{Args{"reason": &empty}, " Reason: "},
	}
	for _, tt := range tests {
		if got := modReasonSuffix(tt.args); got != tt.want {
			t.Errorf("modReasonSuffix(%v) = %q, want %q", tt.args, got, tt.want)
		}
	}
	if opts := modReasonOpts(""); opts != nil {
		t.Errorf("modReasonOpts(\"\") = %v, want nil", opts)
	}
	if opts := modReasonOpts("x"); len(opts) != 1 {
		t.Errorf("modReasonOpts(\"x\") has %d options, want 1", len(opts))
	}
}

func TestModRoleAbove(t *testing.T) {
	high := &discordgo.Role{ID: "200000000000000000", Position: 5}
	low := &discordgo.Role{ID: "100000000000000000", Position: 2}
	sameOld := &discordgo.Role{ID: "100000000000000000", Position: 3}
	sameNew := &discordgo.Role{ID: "300000000000000000", Position: 3}
	if !modRoleAbove(high, low) || modRoleAbove(low, high) {
		t.Error("higher position should be above lower position")
	}
	if !modRoleAbove(sameOld, sameNew) || modRoleAbove(sameNew, sameOld) {
		t.Error("at equal position the older role should be above")
	}
	if modRoleAbove(high, high) {
		t.Error("a role is not above itself")
	}
}
