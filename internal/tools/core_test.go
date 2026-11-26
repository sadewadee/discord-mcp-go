package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

func TestFormatTimestamp(t *testing.T) {
	plus7 := time.FixedZone("", 7*3600)
	minus530 := time.FixedZone("", -(5*3600 + 30*60))
	tests := []struct {
		in   time.Time
		want string
	}{
		{time.Date(2024, 5, 1, 12, 34, 56, 789_000_000, time.UTC), "2024-05-01T12:34:56.789Z"},
		{time.Date(2024, 5, 1, 12, 34, 56, 0, time.UTC), "2024-05-01T12:34:56Z"},
		{time.Date(2024, 5, 1, 12, 34, 0, 0, time.UTC), "2024-05-01T12:34Z"},
		{time.Date(2024, 5, 1, 12, 34, 0, 5_000_000, time.UTC), "2024-05-01T12:34:00.005Z"},
		{time.Date(2021, 1, 2, 3, 4, 5, 789_123_000, time.UTC), "2021-01-02T03:04:05.789123Z"},
		{time.Date(2021, 1, 2, 3, 4, 5, 1, time.UTC), "2021-01-02T03:04:05.000000001Z"},
		{time.Date(2024, 5, 1, 12, 0, 0, 0, plus7), "2024-05-01T12:00+07:00"},
		{time.Date(2024, 5, 1, 12, 0, 30, 0, minus530), "2024-05-01T12:00:30-05:30"},
		{time.Date(2024, 5, 1, 12, 0, 0, 0, time.FixedZone("", 0)), "2024-05-01T12:00Z"},
	}
	for _, tt := range tests {
		if got := formatTimestamp(tt.in); got != tt.want {
			t.Errorf("formatTimestamp(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatTimestampParsedDiscordTime(t *testing.T) {
	ts, err := time.Parse(time.RFC3339Nano, "2021-05-04T12:34:56.789000+00:00")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := formatTimestamp(ts), "2021-05-04T12:34:56.789Z"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSnowflakeTime(t *testing.T) {
	// 175928847299117063 is the example ID from the Discord API reference.
	got := snowflakeTime("175928847299117063")
	if want := time.Date(2016, 4, 30, 11, 18, 25, 796_000_000, time.UTC); !got.Equal(want) {
		t.Errorf("snowflakeTime = %v, want %v", got, want)
	}
	if !snowflakeTime("not-a-number").IsZero() {
		t.Error("invalid ID should give the zero time")
	}
	if got := formatDate(snowflakeTime("175928847299117063")); got != "2016-04-30" {
		t.Errorf("formatDate = %q", got)
	}
}

func TestIsSnowflake(t *testing.T) {
	for in, want := range map[string]bool{
		"175928847299117063":    true,
		"12345678901234567":     true,
		"12345678901234567890":  true,
		"1234567890123456":      false,
		"123456789012345678901": false,
		"17592884729911706a":    false,
		"":                      false,
	} {
		if got := isSnowflake(in); got != want {
			t.Errorf("isSnowflake(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestFormatFileSize(t *testing.T) {
	tests := map[int]string{
		0:                  "0 B",
		1023:               "1023 B",
		1024:               "1.0 KB",
		1536:               "1.5 KB",
		1024 * 1024:        "1.0 MB",
		5 * 1024 * 1024:    "5.0 MB",
		1024 * 1024 * 1024: "1.0 GB",
	}
	for in, want := range tests {
		if got := formatFileSize(in); got != want {
			t.Errorf("formatFileSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestDiscriminator(t *testing.T) {
	for in, want := range map[string]string{"0": "0000", "": "0000", "42": "0042", "1234": "1234"} {
		if got := discriminator(&discordgo.User{Discriminator: in}); got != want {
			t.Errorf("discriminator(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEffectiveNames(t *testing.T) {
	u := &discordgo.User{Username: "alice", GlobalName: "Alice A"}
	if got := memberEffectiveName(&discordgo.Member{User: u, Nick: "Ally"}); got != "Ally" {
		t.Errorf("nick: %q", got)
	}
	if got := memberEffectiveName(&discordgo.Member{User: u}); got != "Alice A" {
		t.Errorf("global: %q", got)
	}
	if got := memberEffectiveName(&discordgo.Member{User: &discordgo.User{Username: "bob"}}); got != "bob" {
		t.Errorf("username: %q", got)
	}
}

func TestParseMessageLimit(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr string
	}{
		{"", 100, ""},
		{"  ", 100, ""},
		{"1", 1, ""},
		{"100", 100, ""},
		{"0", 0, "count must be between 1 and 100"},
		{"101", 0, "count must be between 1 and 100"},
		{"abc", 0, "count must be an integer between 1 and 100"},
		{" 5", 0, "count must be an integer between 1 and 100"},
	}
	for _, tt := range tests {
		got, err := parseMessageLimit(tt.in)
		checkLimit(t, "parseMessageLimit", tt.in, got, err, tt.want, tt.wantErr)
	}
}

func TestParseScanLimit(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr string
	}{
		{"", 500, ""},
		{" 250 ", 250, ""},
		{"1000", 1000, ""},
		{"1001", 0, "maxMessages must be between 1 and 1000"},
		{"0", 0, "maxMessages must be between 1 and 1000"},
		{"x", 0, "maxMessages must be an integer between 1 and 1000"},
	}
	for _, tt := range tests {
		got, err := parseScanLimit(tt.in)
		checkLimit(t, "parseScanLimit", tt.in, got, err, tt.want, tt.wantErr)
	}
}

func TestParseMemberLimit(t *testing.T) {
	if n, err := parseMemberLimit(""); err != nil || n != 25 {
		t.Errorf("default: %d %v", n, err)
	}
	if _, err := parseMemberLimit("101"); err == nil || err.Error() != "count must be between 1 and 100" {
		t.Errorf("range: %v", err)
	}
}

func checkLimit(t *testing.T, fn, in string, got int, err error, want int, wantErr string) {
	t.Helper()
	if wantErr != "" {
		if err == nil || err.Error() != wantErr {
			t.Errorf("%s(%q) error = %v, want %q", fn, in, err, wantErr)
		}
		return
	}
	if err != nil || got != want {
		t.Errorf("%s(%q) = %d, %v; want %d", fn, in, got, err, want)
	}
}

func strp(s string) *string { return &s }

func TestValidateCursors(t *testing.T) {
	tests := []struct {
		args    Args
		wantErr string
	}{
		{Args{}, ""},
		{Args{"before": strp("123")}, ""},
		{Args{"before": strp("")}, "before cannot be blank"},
		{Args{"after": strp("  ")}, "after cannot be blank"},
		{Args{"around": strp("")}, "around cannot be blank"},
		{Args{"before": strp("1"), "after": strp("2")}, "before, after, and around are mutually exclusive; provide only one"},
	}
	for _, tt := range tests {
		err := validateCursors(tt.args)
		if (err == nil) != (tt.wantErr == "") || (err != nil && err.Error() != tt.wantErr) {
			t.Errorf("validateCursors(%v) = %v, want %q", tt.args, err, tt.wantErr)
		}
	}
}

func TestParseArgs(t *testing.T) {
	a, err := ParseArgs(json.RawMessage(`{"channelId":"123","count":5,"flag":true,"none":null}`))
	if err != nil {
		t.Fatal(err)
	}
	if a.Get("channelId") != "123" || a.Get("count") != "5" || a.Get("flag") != "true" {
		t.Errorf("unexpected args: %v", a)
	}
	if a.Has("none") || a.Has("missing") {
		t.Error("null and missing arguments must be absent")
	}
	if _, err := ParseArgs(json.RawMessage(`[1]`)); err == nil {
		t.Error("non-object arguments must fail")
	}
	if a, err := ParseArgs(nil); err != nil || len(a) != 0 {
		t.Error("empty arguments must give empty Args")
	}
}

func TestRequireArgs(t *testing.T) {
	err := requireArgs(Args{"channelId": strp("1"), "message": strp("")}, "channelId", "message")
	if err == nil || err.Error() != "message cannot be null" {
		t.Errorf("got %v", err)
	}
}

func TestErrorText(t *testing.T) {
	rest := &discordgo.RESTError{
		Response: &http.Response{Status: "404 Not Found", StatusCode: 404},
		Message:  &discordgo.APIErrorMessage{Code: 10008, Message: "Unknown Message"},
	}
	if got := ErrorText(rest); got != "10008: Unknown Message" {
		t.Errorf("got %q", got)
	}
	noBody := &discordgo.RESTError{Response: &http.Response{Status: "500 Internal Server Error"}}
	if got := ErrorText(noBody); got != "HTTP 500 Internal Server Error" {
		t.Errorf("got %q", got)
	}
	if got := ErrorText(errors.New("Channel not found by channelId")); got != "Channel not found by channelId" {
		t.Errorf("got %q", got)
	}
}

func TestToolCallRecoversPanic(t *testing.T) {
	tool := Tool{Name: "boom", Handler: func(_ context.Context, _ Args) (string, error) { panic("bad") }}
	_, err := tool.Call(context.Background(), Args{})
	if err == nil || !strings.Contains(err.Error(), "internal error in boom") {
		t.Errorf("got %v", err)
	}
}

func TestInputSchema(t *testing.T) {
	tool := Tool{Params: []Param{req("channelId", "Discord channel ID"), opt("count", "Number")}}
	s := tool.InputSchema()
	if s["type"] != "object" || s["additionalProperties"] != false {
		t.Errorf("schema: %v", s)
	}
	if r := s["required"].([]string); len(r) != 1 || r[0] != "channelId" {
		t.Errorf("required: %v", r)
	}
	props := s["properties"].(map[string]any)
	if props["count"].(map[string]any)["type"] != "string" {
		t.Errorf("props: %v", props)
	}
}

func TestReactionEmoji(t *testing.T) {
	for in, want := range map[string]string{
		"👍":                   "👍",
		"<:party:123456789>":  "party:123456789",
		"<a:dance:987654321>": "dance:987654321",
		"party:123456789":     "party:123456789",
		"not:an-id":           "not:an-id",
	} {
		if got := reactionEmoji(in); got != want {
			t.Errorf("reactionEmoji(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepliedToID(t *testing.T) {
	ref := &discordgo.MessageReference{MessageID: "42"}
	if got := repliedToID(&discordgo.Message{Type: discordgo.MessageTypeReply, MessageReference: ref}); got != "42" {
		t.Errorf("reply: %q", got)
	}
	if got := repliedToID(&discordgo.Message{Type: discordgo.MessageTypeDefault, MessageReference: ref}); got != "" {
		t.Errorf("forward/crosspost must not count: %q", got)
	}
}

func TestJumpURL(t *testing.T) {
	if got := jumpURL("1", "2", "3"); got != "https://discord.com/channels/1/2/3" {
		t.Error(got)
	}
	if got := jumpURL("", "2", "3"); got != "https://discord.com/channels/@me/2/3" {
		t.Error(got)
	}
}

func TestIDLess(t *testing.T) {
	if !idLess("99", "100") || idLess("100", "99") || !idLess("123", "124") {
		t.Error("idLess must compare numerically")
	}
}
