package tools

import (
	"reflect"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestForumParseInt(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr string
	}{
		{"0", 0, ""},
		{"21600", 21600, ""},
		{"-5", -5, ""},
		{"+7", 7, ""},
		{"abc", 0, `For input string: "abc"`},
		{" 5", 0, `For input string: " 5"`},
		{"2147483648", 0, `For input string: "2147483648"`},
	}
	for _, tt := range tests {
		got, err := forumParseInt(tt.in)
		if tt.wantErr != "" {
			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("forumParseInt(%q) error = %v, want %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("forumParseInt(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
		}
	}
}

func TestForumParseBool(t *testing.T) {
	for in, want := range map[string]bool{"true": true, "TRUE": true, "True": true, "false": false, "yes": false, "1": false, "": false} {
		if got := forumParseBool(in); got != want {
			t.Errorf("forumParseBool(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestForumSplitIDs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"1", []string{"1"}},
		{" 1 , 2,,3 ", []string{"1", "2", "3"}},
		{" , ", []string{}},
	}
	for _, tt := range tests {
		if got := forumSplitIDs(tt.in); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("forumSplitIDs(%q) = %#v, want %#v", tt.in, got, tt.want)
		}
	}
}

func TestForumSortAndLayout(t *testing.T) {
	latest, created := discordgo.ForumSortOrderLatestActivity, discordgo.ForumSortOrderCreationDate
	other := discordgo.ForumSortOrderType(7)
	sortNames := []struct {
		in   *discordgo.ForumSortOrderType
		want string
	}{{nil, "UNKNOWN"}, {&latest, "RECENT_ACTIVITY"}, {&created, "CREATION_TIME"}, {&other, "UNKNOWN"}}
	for _, tt := range sortNames {
		if got := forumSortOrderName(tt.in); got != tt.want {
			t.Errorf("forumSortOrderName = %q, want %q", got, tt.want)
		}
	}
	if v, ok := forumParseSort("recent_activity"); !ok || v != latest {
		t.Errorf("forumParseSort(recent_activity) = %v, %v", v, ok)
	}
	if v, ok := forumParseSort("CREATION_TIME"); !ok || v != created {
		t.Errorf("forumParseSort(CREATION_TIME) = %v, %v", v, ok)
	}
	if _, ok := forumParseSort("UNKNOWN"); ok {
		t.Error("forumParseSort(UNKNOWN) accepted")
	}

	layouts := map[discordgo.ForumLayout]string{
		discordgo.ForumLayoutNotSet:      "DEFAULT_VIEW",
		discordgo.ForumLayoutListView:    "LIST_VIEW",
		discordgo.ForumLayoutGalleryView: "GALLERY_VIEW",
		9:                                "UNKNOWN",
	}
	for l, want := range layouts {
		if got := forumLayoutName(l); got != want {
			t.Errorf("forumLayoutName(%d) = %q, want %q", l, got, want)
		}
	}
	if v, ok := forumParseLayout("gallery_view"); !ok || v != discordgo.ForumLayoutGalleryView {
		t.Errorf("forumParseLayout(gallery_view) = %v, %v", v, ok)
	}
	if _, ok := forumParseLayout("grid"); ok {
		t.Error("forumParseLayout(grid) accepted")
	}
}

func TestForumTagFormatting(t *testing.T) {
	tests := []struct {
		tag   discordgo.ForumTag
		line  string
		emoji string
	}{
		{discordgo.ForumTag{ID: "1", Name: "bug"}, "bug (ID: 1)", ""},
		{discordgo.ForumTag{ID: "2", Name: "staff", Moderated: true, EmojiName: "🔒"}, "staff (ID: 2) [moderated]", "🔒"},
		{discordgo.ForumTag{ID: "3", Name: "custom", EmojiID: "99", EmojiName: "party"}, "custom (ID: 3)", "<:party:99>"},
	}
	for _, tt := range tests {
		if got := forumTagLine(tt.tag); got != tt.line {
			t.Errorf("forumTagLine = %q, want %q", got, tt.line)
		}
		if got := forumTagEmoji(tt.tag); got != tt.emoji {
			t.Errorf("forumTagEmoji = %q, want %q", got, tt.emoji)
		}
	}
}
