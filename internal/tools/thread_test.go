package tools

import (
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestThreadTypeName(t *testing.T) {
	tests := map[discordgo.ChannelType]string{
		discordgo.ChannelTypeGuildPublicThread:  "GUILD_PUBLIC_THREAD",
		discordgo.ChannelTypeGuildPrivateThread: "GUILD_PRIVATE_THREAD",
		discordgo.ChannelTypeGuildNewsThread:    "GUILD_NEWS_THREAD",
		discordgo.ChannelTypeGuildText:          "UNKNOWN",
	}
	for ty, want := range tests {
		if got := threadTypeName(ty); got != want {
			t.Errorf("threadTypeName(%d) = %q, want %q", ty, got, want)
		}
	}
}

func TestThreadStateFlags(t *testing.T) {
	tests := []struct {
		meta             *discordgo.ThreadMetadata
		archived, locked bool
	}{
		{nil, false, false},
		{&discordgo.ThreadMetadata{}, false, false},
		{&discordgo.ThreadMetadata{Archived: true}, true, false},
		{&discordgo.ThreadMetadata{Archived: true, Locked: true}, true, true},
	}
	for _, tt := range tests {
		c := &discordgo.Channel{ThreadMetadata: tt.meta}
		if got := threadArchived(c); got != tt.archived {
			t.Errorf("threadArchived(%+v) = %v, want %v", tt.meta, got, tt.archived)
		}
		if got := threadLocked(c); got != tt.locked {
			t.Errorf("threadLocked(%+v) = %v, want %v", tt.meta, got, tt.locked)
		}
	}
}

func TestThreadJumpURL(t *testing.T) {
	c := &discordgo.Channel{ID: "222", GuildID: "111"}
	if got, want := threadJumpURL(c), "https://discord.com/channels/111/222"; got != want {
		t.Errorf("threadJumpURL = %q, want %q", got, want)
	}
}
