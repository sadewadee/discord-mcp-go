package tools

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

// newTestToolset returns a Toolset whose state cache holds one guild with a
// member, a role and a channel. No connection is made.
func newTestToolset(t *testing.T) *Toolset {
	t.Helper()
	s, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatal(err)
	}
	g := &discordgo.Guild{
		ID:       "100000000000000001",
		Name:     "Test",
		Roles:    []*discordgo.Role{{ID: "300000000000000003", Name: "Mods", Position: 1}},
		Channels: []*discordgo.Channel{{ID: "200000000000000002", GuildID: "100000000000000001", Name: "general", Type: discordgo.ChannelTypeGuildText}},
		Members: []*discordgo.Member{{
			GuildID: "100000000000000001",
			User:    &discordgo.User{ID: "400000000000000004", Username: "alice"},
			Nick:    "Ally",
		}},
	}
	if err := s.State.GuildAdd(g); err != nil {
		t.Fatal(err)
	}
	return New(s, "", nil)
}

func TestContentDisplay(t *testing.T) {
	ts := newTestToolset(t)
	m := &discordgo.Message{
		Content: "hi <@400000000000000004> and <@!400000000000000004>, <@500000000000000005> see <#200000000000000002> <@&300000000000000003> <:wave:123> </ping:999> @everyone",
		Mentions: []*discordgo.User{
			{ID: "400000000000000004", Username: "alice"},
			{ID: "500000000000000005", Username: "bob"},
		},
	}
	want := "hi @Ally and @Ally, @bob see #general @Mods :wave: /ping @everyone"
	if got := ts.contentDisplay(m, "100000000000000001"); got != want {
		t.Errorf("guild:\n got %q\nwant %q", got, want)
	}
	// Outside a guild, users are shown by username and role mentions stay raw.
	want = "hi @alice and @alice, @bob see #general <@&300000000000000003> :wave: /ping @everyone"
	if got := ts.contentDisplay(m, ""); got != want {
		t.Errorf("dm:\n got %q\nwant %q", got, want)
	}
	// A user not in the mentions list is left as-is.
	if got := ts.contentDisplay(&discordgo.Message{Content: "<@1>"}, ""); got != "<@1>" {
		t.Errorf("unknown mention: %q", got)
	}
}

func TestFormatMessage(t *testing.T) {
	ts := newTestToolset(t)
	m := &discordgo.Message{
		ID:               "175928847299117063",
		Type:             discordgo.MessageTypeReply,
		MessageReference: &discordgo.MessageReference{MessageID: "175928847299117000"},
		Author:           &discordgo.User{ID: "400000000000000004", Username: "alice"},
		Content:          "hello",
		Attachments: []*discordgo.MessageAttachment{{
			ID: "9", Filename: "a.png", Size: 2048, ContentType: "image/png", URL: "https://cdn/a.png", ProxyURL: "https://proxy/a.png",
		}, {
			ID: "10", Filename: "b.bin", Size: 10, URL: "https://cdn/b.bin",
		}},
	}
	want := "- (ID: 175928847299117063) **[alice]** (Author ID: 400000000000000004) `2016-04-30T11:18:25.796Z` [Reply to: 175928847299117000]: ```hello```" +
		"\n  Attachments:" +
		"\n    - (Attachment ID: 9) `a.png` (2.0 KB, image/png) URL: https://cdn/a.png" +
		"\n    - (Attachment ID: 10) `b.bin` (10 B, unknown) URL: https://cdn/b.bin"
	if got := ts.formatMessage(m, "100000000000000001"); got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
	if got := formatAttachmentDetail(m.Attachments[0]); got != "- (Attachment ID: 9) `a.png` (2.0 KB, image/png) URL: https://cdn/a.png\n  Proxy URL: https://proxy/a.png" {
		t.Errorf("detail: %q", got)
	}
	if got := ts.retrievedMessages(nil, ""); got != "**Retrieved 0 messages:** \n" {
		t.Errorf("empty: %q", got)
	}
}

func TestDefaultGuild(t *testing.T) {
	ts := newTestToolset(t)
	if _, err := ts.guild("", "guildId cannot be null"); err == nil || err.Error() != "guildId cannot be null" {
		t.Errorf("no default: %v", err)
	}
	ts.defaultGuildID = "100000000000000001"
	if g, err := ts.guild("", "x"); err != nil || g.ID != "100000000000000001" {
		t.Errorf("default: %v %v", g, err)
	}
	if _, err := ts.guild("999", "x"); err == nil || err.Error() != "Discord server not found by guildId" {
		t.Errorf("unknown: %v", err)
	}
}

func TestMemberMatchesAndSummary(t *testing.T) {
	ts := newTestToolset(t)
	g, _ := ts.s.State.Guild("100000000000000001")
	m := g.Members[0]
	if !memberMatches(m, "ALI") || !memberMatches(m, "ally") || memberMatches(m, "zzz") {
		t.Error("memberMatches")
	}
	want := "- User ID: 400000000000000004 | Username: alice | Global: none | Nickname: Ally | Effective: Ally | Bot: false"
	if got := ts.formatMemberSummary(g, m, false); got != want {
		t.Errorf("\n got %q\nwant %q", got, want)
	}
}
