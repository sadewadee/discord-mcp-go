package tools

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestOrderChannelsForDisplay(t *testing.T) {
	ch := func(id string, ty discordgo.ChannelType, pos int, parent string) *discordgo.Channel {
		return &discordgo.Channel{ID: id, Name: "c" + id, Type: ty, Position: pos, ParentID: parent}
	}
	all := []*discordgo.Channel{
		ch("100000000000000010", discordgo.ChannelTypeGuildCategory, 1, ""),
		ch("100000000000000011", discordgo.ChannelTypeGuildCategory, 0, ""),
		ch("100000000000000001", discordgo.ChannelTypeGuildVoice, 0, ""),
		ch("100000000000000002", discordgo.ChannelTypeGuildText, 5, ""),
		ch("100000000000000003", discordgo.ChannelTypeGuildText, 0, "100000000000000010"),
		ch("100000000000000004", discordgo.ChannelTypeGuildVoice, 0, "100000000000000010"),
		ch("100000000000000005", discordgo.ChannelTypeGuildText, 2, "100000000000000011"),
		ch("100000000000000006", discordgo.ChannelTypeGuildText, 1, "100000000000000011"),
		ch("100000000000000007", discordgo.ChannelTypeGuildPublicThread, 0, "100000000000000006"),
	}
	var ids []string
	for _, c := range orderChannelsForDisplay(all) {
		ids = append(ids, c.ID[len(c.ID)-2:])
	}
	want := "02 01 11 06 05 10 03 04"
	if got := strings.Join(ids, " "); got != want {
		t.Fatalf("order = %s, want %s", got, want)
	}
}

func TestChannelTypeName(t *testing.T) {
	tests := map[discordgo.ChannelType]string{
		discordgo.ChannelTypeGuildText:          "TEXT",
		discordgo.ChannelTypeGuildVoice:         "VOICE",
		discordgo.ChannelTypeGuildCategory:      "CATEGORY",
		discordgo.ChannelTypeGuildNews:          "NEWS",
		discordgo.ChannelTypeGuildStageVoice:    "STAGE",
		discordgo.ChannelTypeGuildForum:         "FORUM",
		discordgo.ChannelTypeGuildMedia:         "MEDIA",
		discordgo.ChannelTypeGuildPublicThread:  "GUILD_PUBLIC_THREAD",
		discordgo.ChannelTypeGuildPrivateThread: "GUILD_PRIVATE_THREAD",
		discordgo.ChannelTypeGuildNewsThread:    "GUILD_NEWS_THREAD",
		discordgo.ChannelType(99):               "UNKNOWN",
	}
	for ty, want := range tests {
		if got := channelTypeName(ty); got != want {
			t.Errorf("channelTypeName(%d) = %q, want %q", ty, got, want)
		}
	}
}

func TestParseChannelIntAndSlowmode(t *testing.T) {
	if n, err := parseChannelInt("+15"); err != nil || n != 15 {
		t.Fatalf("parseChannelInt(+15) = %d, %v", n, err)
	}
	if _, err := parseChannelInt("abc"); err == nil || err.Error() != `For input string: "abc"` {
		t.Fatalf("parseChannelInt(abc) error = %v", err)
	}
	if _, err := parseChannelInt("99999999999"); err == nil {
		t.Fatal("expected overflow error")
	}
	if err := checkSlowmode(21600); err != nil {
		t.Fatalf("21600 rejected: %v", err)
	}
	if err := checkSlowmode(21601); err == nil {
		t.Fatal("21601 accepted")
	}
	if !parseChannelBool("TRUE") || parseChannelBool("yes") {
		t.Fatal("parseChannelBool mismatch")
	}
}

func TestChannelList(t *testing.T) {
	got := channelList([]*discordgo.Channel{
		{ID: "1", Name: "general", Type: discordgo.ChannelTypeGuildText},
		{ID: "2", Name: "Voice", Type: discordgo.ChannelTypeGuildVoice},
	})
	want := "Retrieved 2 channels:\n- TEXT channel: general (ID: 1)\n- VOICE channel: Voice (ID: 2)"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
