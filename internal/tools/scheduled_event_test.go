package tools

import (
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestEventParseTime(t *testing.T) {
	tests := []struct {
		in   string
		want string // formatted back with formatTimestamp
	}{
		{"2025-01-31T18:00:00Z", "2025-01-31T18:00Z"},
		{"2025-01-31T18:00Z", "2025-01-31T18:00Z"},
		{"2025-01-31T18:00:30.5Z", "2025-01-31T18:00:30.500Z"},
		{"2025-01-31T18:00:00+07:00", "2025-01-31T18:00+07:00"},
		{"2025-01-31T18:00:00.123456-05:30", "2025-01-31T18:00:00.123456-05:30"},
	}
	for _, tt := range tests {
		got, err := eventParseTime(tt.in)
		if err != nil {
			t.Errorf("eventParseTime(%q) error: %v", tt.in, err)
			continue
		}
		if s := formatTimestamp(got); s != tt.want {
			t.Errorf("eventParseTime(%q) formats as %q, want %q", tt.in, s, tt.want)
		}
	}
	for _, bad := range []string{"2025-01-31", "2025-01-31T18:00:00", "tomorrow", "2025-01-31 18:00:00Z"} {
		_, err := eventParseTime(bad)
		if err == nil || err.Error() != "Invalid ISO8601 timestamp: "+bad {
			t.Errorf("eventParseTime(%q) error = %v", bad, err)
		}
	}
}

func TestEventStatusFromArg(t *testing.T) {
	for in, want := range map[string]discordgo.GuildScheduledEventStatus{
		"1": discordgo.GuildScheduledEventStatusScheduled,
		"2": discordgo.GuildScheduledEventStatusActive,
		"3": discordgo.GuildScheduledEventStatusCompleted,
		"4": discordgo.GuildScheduledEventStatusCanceled,
	} {
		if got, err := eventStatusFromArg(in); err != nil || got != want {
			t.Errorf("eventStatusFromArg(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := eventStatusFromArg("5"); err == nil || err.Error() != "status must be 1 (Scheduled), 2 (Active), 3 (Completed), or 4 (Canceled)" {
		t.Errorf("status 5 error = %v", err)
	}
	if _, err := eventStatusFromArg("x"); err == nil || err.Error() != `For input string: "x"` {
		t.Errorf("status x error = %v", err)
	}
}

func TestEventNames(t *testing.T) {
	if eventTypeName(discordgo.GuildScheduledEventEntityTypeVoice) != "VOICE" ||
		eventTypeName(discordgo.GuildScheduledEventEntityTypeStageInstance) != "STAGE_INSTANCE" ||
		eventTypeName(discordgo.GuildScheduledEventEntityTypeExternal) != "EXTERNAL" ||
		eventTypeName(9) != "UNKNOWN" {
		t.Error("unexpected event type names")
	}
	if eventStatusName(discordgo.GuildScheduledEventStatusCanceled) != "CANCELED" || eventStatusName(0) != "UNKNOWN" {
		t.Error("unexpected event status names")
	}
}

func TestFormatScheduledEvent(t *testing.T) {
	start := time.Date(2025, 3, 1, 18, 0, 0, 0, time.UTC)
	end := time.Date(2025, 3, 1, 20, 30, 0, 0, time.UTC)
	e := &discordgo.GuildScheduledEvent{
		ID:                 "99",
		Name:               "Game night",
		Description:        "Bring snacks",
		ScheduledStartTime: start,
		ScheduledEndTime:   &end,
		Status:             discordgo.GuildScheduledEventStatusScheduled,
		EntityType:         discordgo.GuildScheduledEventEntityTypeVoice,
		ChannelID:          "7",
		UserCount:          3,
	}
	ch := &discordgo.Channel{ID: "7", Name: "Lounge"}
	want := "**Game night** (ID: 99)\n" +
		"  • Type: VOICE\n" +
		"  • Status: SCHEDULED\n" +
		"  • Start: 2025-03-01T18:00Z\n" +
		"  • End: 2025-03-01T20:30Z\n" +
		"  • Channel: Lounge (ID: 7)\n" +
		"  • Description: Bring snacks\n" +
		"  • Interested: 3 users"
	if got := formatScheduledEvent(e, ch); got != want {
		t.Errorf("formatScheduledEvent =\n%s\nwant\n%s", got, want)
	}

	ext := &discordgo.GuildScheduledEvent{
		ID: "100", Name: "Meetup", ScheduledStartTime: start,
		Status: discordgo.GuildScheduledEventStatusActive, EntityType: discordgo.GuildScheduledEventEntityTypeExternal,
		EntityMetadata: discordgo.GuildScheduledEventEntityMetadata{Location: "Park"},
	}
	wantExt := "**Meetup** (ID: 100)\n" +
		"  • Type: EXTERNAL\n" +
		"  • Status: ACTIVE\n" +
		"  • Start: 2025-03-01T18:00Z\n" +
		"  • Location: Park\n" +
		"  • Interested: 0 users"
	if got := formatScheduledEvent(ext, nil); got != wantExt {
		t.Errorf("formatScheduledEvent(external) =\n%s\nwant\n%s", got, wantExt)
	}
}

func TestFormatScheduledEventListItem(t *testing.T) {
	start := time.Date(2025, 3, 1, 18, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	e := &discordgo.GuildScheduledEvent{
		ID: "1", Name: "Talk", ScheduledStartTime: start, ScheduledEndTime: &end,
		Status: discordgo.GuildScheduledEventStatusScheduled, EntityType: discordgo.GuildScheduledEventEntityTypeStageInstance,
		UserCount: 12,
	}
	want := "- **Talk** (ID: 1)\n  • Type: STAGE_INSTANCE | Status: SCHEDULED\n  • Start: 2025-03-01T18:00Z | End: 2025-03-01T19:00Z\n  • Interested: 12 users"
	if got := formatScheduledEventListItem(e, true); got != want {
		t.Errorf("with count =\n%s\nwant\n%s", got, want)
	}
	wantNoCount := "- **Talk** (ID: 1)\n  • Type: STAGE_INSTANCE | Status: SCHEDULED\n  • Start: 2025-03-01T18:00Z | End: 2025-03-01T19:00Z"
	if got := formatScheduledEventListItem(e, false); got != wantNoCount {
		t.Errorf("without count =\n%s\nwant\n%s", got, wantNoCount)
	}
}

func TestSortScheduledEvents(t *testing.T) {
	early := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	late := early.Add(time.Hour)
	events := []*discordgo.GuildScheduledEvent{
		{ID: "30", ScheduledStartTime: late},
		{ID: "20", ScheduledStartTime: early},
		{ID: "10", ScheduledStartTime: early},
	}
	sortScheduledEvents(events)
	if events[0].ID != "10" || events[1].ID != "20" || events[2].ID != "30" {
		t.Errorf("order = %s, %s, %s", events[0].ID, events[1].ID, events[2].ID)
	}
}

func TestEventDefaultTrue(t *testing.T) {
	for in, want := range map[string]bool{"": true, "true": true, "TRUE": true, "false": false, "no": false} {
		if got := eventDefaultTrue(in); got != want {
			t.Errorf("eventDefaultTrue(%q) = %v, want %v", in, got, want)
		}
	}
}
