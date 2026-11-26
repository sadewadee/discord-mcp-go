package tools

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestWebhookFormat(t *testing.T) {
	w := &discordgo.Webhook{ID: "123", Name: "Deploys", Token: "abc-DEF_1"}
	want := "- (ID: 123) **[Deploys]** ```https://discord.com/api/v10/webhooks/123/abc-DEF_1```"
	if got := webhookFormat(w); got != want {
		t.Errorf("webhookFormat = %q, want %q", got, want)
	}
	noToken := &discordgo.Webhook{ID: "456", Name: "Follower"}
	if got := webhookURL(noToken); got != "https://discord.com/api/v10/webhooks/456" {
		t.Errorf("webhookURL(no token) = %q", got)
	}
}

func TestWebhookParseURL(t *testing.T) {
	tests := []struct {
		in, id, token string
		ok            bool
	}{
		{"https://discord.com/api/webhooks/123/abc-DEF_1", "123", "abc-DEF_1", true},
		{"https://discord.com/api/v10/webhooks/123/tok", "123", "tok", true},
		{"https://canary.discord.com/api/webhooks/123/tok", "123", "tok", true},
		{"https://discordapp.com/api/webhooks/123/tok/github", "123", "tok", true},
		{"http://discord.com/api/webhooks/123/tok", "123", "tok", true},
		{"https://example.com/api/webhooks/123/tok", "", "", false},
		{"https://discord.com/api/webhooks/abc/tok", "", "", false},
		{"not a url", "", "", false},
	}
	for _, tt := range tests {
		id, token, err := webhookParseURL(tt.in)
		if !tt.ok {
			if err == nil || err.Error() != "Provided invalid webhook URL" {
				t.Errorf("webhookParseURL(%q) error = %v, want invalid", tt.in, err)
			}
			continue
		}
		if err != nil || id != tt.id || token != tt.token {
			t.Errorf("webhookParseURL(%q) = %q, %q, %v; want %q, %q", tt.in, id, token, err, tt.id, tt.token)
		}
	}
}
