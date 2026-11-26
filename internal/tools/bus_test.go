package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	testGuild  = "100000000000000001"
	testText   = "200000000000000002"
	testForum  = "200000000000000010"
	testPostA  = "700000000000000001"
	testPostB  = "700000000000000002"
	testVoice  = "200000000000000020"
	testNews   = "200000000000000030"
	testBotUID = "900000000000000001"
)

// fakeAPI stands in for Discord's REST API: it records requests and answers
// from handle.
type fakeAPI struct {
	mu       sync.Mutex
	requests []apiRequest
	handle   func(method, path string, body map[string]any) (int, any)
}

type apiRequest struct {
	Method, Path string
	Body         map[string]any
}

func (f *fakeAPI) RoundTrip(r *http.Request) (*http.Response, error) {
	var body map[string]any
	if r.Body != nil {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v9")
	f.mu.Lock()
	f.requests = append(f.requests, apiRequest{r.Method, path, body})
	f.mu.Unlock()
	status, resp := http.StatusNotFound, any(map[string]any{"code": 10003, "message": "Unknown Channel"})
	if f.handle != nil {
		status, resp = f.handle(r.Method, path, body)
	}
	b, _ := json.Marshal(resp)
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(b))),
		Request:    r,
	}, nil
}

func (f *fakeAPI) last() apiRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return apiRequest{}
	}
	return f.requests[len(f.requests)-1]
}

// newBusToolset is newTestToolset plus a forum with two posts, a voice and
// an announcement channel, and a fake REST API.
func newBusToolset(t *testing.T) (*Toolset, *fakeAPI) {
	t.Helper()
	ts := newTestToolset(t)
	api := &fakeAPI{}
	ts.s.Client = &http.Client{Transport: api}
	ts.s.ShouldRetryOnRateLimit = false
	ts.burstWindow = 100 * time.Millisecond
	for _, c := range []*discordgo.Channel{
		{ID: testForum, GuildID: testGuild, Name: "agent-bus", Type: discordgo.ChannelTypeGuildForum},
		{ID: testPostA, GuildID: testGuild, Name: "task A", Type: discordgo.ChannelTypeGuildPublicThread, ParentID: testForum},
		{ID: testPostB, GuildID: testGuild, Name: "task B", Type: discordgo.ChannelTypeGuildPublicThread, ParentID: testForum},
		{ID: testVoice, GuildID: testGuild, Name: "voice", Type: discordgo.ChannelTypeGuildVoice},
		{ID: testNews, GuildID: testGuild, Name: "news", Type: discordgo.ChannelTypeGuildNews},
	} {
		if err := ts.s.State.ChannelAdd(c); err != nil {
			t.Fatal(err)
		}
	}
	return ts, api
}

func busMsg(id, channelID, content string) *discordgo.Message {
	return &discordgo.Message{
		ID: id, ChannelID: channelID, GuildID: testGuild, Content: content,
		Author: &discordgo.User{ID: testBotUID, Username: "Tukang Coding", Bot: true},
	}
}

func callTool(t *testing.T, ts *Toolset, ctx context.Context, name string, args map[string]string) (string, error) {
	t.Helper()
	a := Args{}
	for k, v := range args {
		v := v
		a[k] = &v
	}
	for _, tool := range ts.All() {
		if tool.Name == name {
			return tool.Call(ctx, a)
		}
	}
	t.Fatalf("no tool %s", name)
	return "", nil
}

// --- hub ---

func TestHubDeliversByChannelAndParent(t *testing.T) {
	h := newHub(func(id string) string {
		if id == testPostA {
			return testForum
		}
		return ""
	})
	onPost, _ := h.subscribe(testPostA)
	onForum, _ := h.subscribe(testForum)
	onOther, _ := h.subscribe(testText)
	h.deliver(busMsg("1", testPostA, "x"))
	for name, w := range map[string]*waiter{"post": onPost, "forum": onForum} {
		select {
		case m := <-w.ch:
			if m.ID != "1" {
				t.Errorf("%s got %s", name, m.ID)
			}
		default:
			t.Errorf("%s waiter got nothing", name)
		}
	}
	select {
	case <-onOther.ch:
		t.Error("unrelated waiter got the message")
	default:
	}
	for _, w := range []*waiter{onPost, onForum, onOther} {
		h.unsubscribe(w)
	}
	if h.waiting() != 0 {
		t.Errorf("waiting = %d", h.waiting())
	}
	h.unsubscribe(onPost) // twice is harmless
	if h.waiting() != 0 {
		t.Errorf("double unsubscribe changed the count: %d", h.waiting())
	}
}

func TestHubSkipsParentLookupWithoutOtherWaiters(t *testing.T) {
	lookups := 0
	h := newHub(func(string) string { lookups++; return "" })
	h.deliver(busMsg("1", testText, "x")) // nobody waiting
	w, _ := h.subscribe(testText)
	h.deliver(busMsg("2", testText, "x")) // only waiters on this channel
	h.unsubscribe(w)
	if lookups != 0 {
		t.Errorf("parent looked up %d times", lookups)
	}
}

func TestHubWaiterCapAndClose(t *testing.T) {
	h := newHub(func(string) string { return "" })
	var ws []*waiter
	for i := 0; i < MaxWaiters; i++ {
		w, err := h.subscribe(testText)
		if err != nil {
			t.Fatal(err)
		}
		ws = append(ws, w)
	}
	if _, err := h.subscribe(testText); !errors.Is(err, errTooManyWaiters) {
		t.Errorf("waiter %d: %v", MaxWaiters+1, err)
	}
	h.unsubscribe(ws[0])
	if _, err := h.subscribe(testText); err != nil {
		t.Errorf("a freed slot should be reusable: %v", err)
	}
	h.close()
	select {
	case <-h.done():
	default:
		t.Error("done not closed")
	}
	if _, err := h.subscribe(testText); !errors.Is(err, errHubClosed) {
		t.Errorf("subscribe after close: %v", err)
	}
}

// --- wait_for_messages ---

// waitAsync starts wait_for_messages and returns its result channel once the
// waiter is subscribed.
func waitAsync(t *testing.T, ts *Toolset, ctx context.Context, args map[string]string) <-chan [2]any {
	t.Helper()
	before := ts.hub.waiting()
	out := make(chan [2]any, 1)
	go func() {
		s, err := callTool(t, ts, ctx, "wait_for_messages", args)
		out <- [2]any{s, err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for ts.hub.waiting() == before {
		if time.Now().After(deadline) {
			t.Fatal("waiter never subscribed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return out
}

func result(t *testing.T, ch <-chan [2]any) (string, error) {
	t.Helper()
	select {
	case r := <-ch:
		err, _ := r[1].(error)
		return r[0].(string), err
	case <-time.After(5 * time.Second):
		t.Fatal("wait_for_messages did not return")
		return "", nil
	}
}

func TestWaitReturnsBurstOldestFirst(t *testing.T) {
	ts, _ := newBusToolset(t)
	out := waitAsync(t, ts, context.Background(), map[string]string{"channelId": testText, "timeoutSeconds": "5"})
	start := time.Now()
	ts.hub.deliver(busMsg("800000000000000003", testText, "third"))
	ts.hub.deliver(busMsg("800000000000000001", testText, "first"))
	ts.hub.deliver(busMsg("800000000000000002", testText, "second"))
	ts.hub.deliver(busMsg("800000000000000002", testText, "second")) // duplicate event
	got, err := result(t, out)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
	if !strings.HasPrefix(got, "**Received 3 new messages:**\n- (ID: 800000000000000001)") {
		t.Errorf("result:\n%s", got)
	}
	i1, i2, i3 := strings.Index(got, "first"), strings.Index(got, "second"), strings.Index(got, "third")
	if !(i1 < i2 && i2 < i3) {
		t.Errorf("not oldest first:\n%s", got)
	}
	// Same line format as read_messages; the bot's own messages are included.
	want := ts.formatMessage(busMsg("800000000000000001", testText, "first"), testGuild)
	if !strings.Contains(got, want) {
		t.Errorf("line format differs from read_messages:\n%s\nwant line %s", got, want)
	}
	if ts.hub.waiting() != 0 {
		t.Error("waiter leaked after delivery")
	}
}

func TestWaitBurstWindowEnds(t *testing.T) {
	ts, _ := newBusToolset(t)
	out := waitAsync(t, ts, context.Background(), map[string]string{"channelId": testText})
	ts.hub.deliver(busMsg("800000000000000001", testText, "in burst"))
	time.Sleep(ts.burstWindow * 3)
	ts.hub.deliver(busMsg("800000000000000002", testText, "too late"))
	got, _ := result(t, out)
	if !strings.Contains(got, "in burst") || strings.Contains(got, "too late") {
		t.Errorf("result:\n%s", got)
	}
}

func TestWaitTimeoutIsNotAnError(t *testing.T) {
	ts, _ := newBusToolset(t)
	start := time.Now()
	got, err := callTool(t, ts, context.Background(), "wait_for_messages", map[string]string{"channelId": testText, "timeoutSeconds": "1"})
	if err != nil || got != "No new messages (waited 1s)" {
		t.Errorf("got %q, %v", got, err)
	}
	if d := time.Since(start); d < time.Second || d > 3*time.Second {
		t.Errorf("waited %s", d)
	}
	if ts.hub.waiting() != 0 {
		t.Error("waiter leaked after timeout")
	}
}

func TestWaitAfterReturnsExistingAtOnce(t *testing.T) {
	ts, api := newBusToolset(t)
	api.handle = func(method, path string, _ map[string]any) (int, any) {
		if method == http.MethodGet && path == "/channels/"+testText+"/messages" {
			// Discord answers newest first.
			return 200, []*discordgo.Message{
				busMsg("800000000000000006", testText, "newer"),
				busMsg("800000000000000005", testText, "older"),
			}
		}
		return 404, map[string]any{"code": 0, "message": "unexpected"}
	}
	start := time.Now()
	got, err := callTool(t, ts, context.Background(), "wait_for_messages",
		map[string]string{"channelId": testText, "after": "800000000000000004", "timeoutSeconds": "10"})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("did not return at once: %s", time.Since(start))
	}
	if strings.Index(got, "older") > strings.Index(got, "newer") || !strings.HasPrefix(got, "**Received 2 new messages:**") {
		t.Errorf("result:\n%s", got)
	}
	req := api.last()
	if !strings.Contains(req.Path, "/channels/"+testText+"/messages") {
		t.Errorf("request %+v", req)
	}
	if ts.hub.waiting() != 0 {
		t.Error("waiter leaked")
	}
}

func TestWaitAfterFiltersOlderEvents(t *testing.T) {
	ts, api := newBusToolset(t)
	api.handle = func(string, string, map[string]any) (int, any) { return 200, []any{} }
	out := waitAsync(t, ts, context.Background(),
		map[string]string{"channelId": testText, "after": "800000000000000005", "timeoutSeconds": "5"})
	ts.hub.deliver(busMsg("800000000000000004", testText, "old"))
	ts.hub.deliver(busMsg("800000000000000006", testText, "new"))
	got, _ := result(t, out)
	if strings.Contains(got, "old") || !strings.Contains(got, "new") {
		t.Errorf("result:\n%s", got)
	}
}

func TestWaitOnForumGroupsByPost(t *testing.T) {
	ts, api := newBusToolset(t)
	out := waitAsync(t, ts, context.Background(),
		map[string]string{"channelId": testForum, "after": "800000000000000000", "timeoutSeconds": "5"})
	ts.hub.deliver(busMsg("800000000000000002", testPostB, "b1"))
	ts.hub.deliver(busMsg("800000000000000001", testPostA, "a1"))
	ts.hub.deliver(busMsg("800000000000000003", testPostA, "a2"))
	got, err := result(t, out)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"**Received 3 new messages:**",
		"Post: **task A** (ID: " + testPostA + ")", "a1", "a2",
		"Post: **task B** (ID: " + testPostB + ")", "b1",
	}
	pos := 0
	for _, w := range want {
		i := strings.Index(got[pos:], w)
		if i < 0 {
			t.Fatalf("missing %q in order:\n%s", w, got)
		}
		pos += i + len(w)
	}
	// With after on a forum, no REST check is made.
	if len(api.requests) != 0 {
		t.Errorf("unexpected REST calls: %+v", api.requests)
	}
}

func TestWaitCancellationAndShutdown(t *testing.T) {
	ts, _ := newBusToolset(t)
	ctx, cancel := context.WithCancel(context.Background())
	out := waitAsync(t, ts, ctx, map[string]string{"channelId": testText, "timeoutSeconds": "30"})
	cancel()
	if _, err := result(t, out); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: %v", err)
	}
	if ts.hub.waiting() != 0 {
		t.Error("waiter leaked after cancellation")
	}

	out = waitAsync(t, ts, context.Background(), map[string]string{"channelId": testText, "timeoutSeconds": "30"})
	start := time.Now()
	ts.Close()
	if _, err := result(t, out); !errors.Is(err, errHubClosed) {
		t.Errorf("shutdown: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("shutdown took %s", time.Since(start))
	}
	if ts.hub.waiting() != 0 {
		t.Error("waiter leaked after shutdown")
	}
}

func TestWaitParameterErrors(t *testing.T) {
	ts, _ := newBusToolset(t)
	for _, tc := range []struct {
		args map[string]string
		want string
	}{
		{map[string]string{}, "channelId cannot be null"},
		{map[string]string{"channelId": testText, "timeoutSeconds": "0"}, "timeoutSeconds must be between 1 and 50"},
		{map[string]string{"channelId": testText, "timeoutSeconds": "51"}, "timeoutSeconds must be between 1 and 50"},
		{map[string]string{"channelId": testText, "timeoutSeconds": "x"}, "timeoutSeconds must be an integer between 1 and 50"},
		{map[string]string{"channelId": testText, "after": " "}, "after cannot be blank"},
		{map[string]string{"channelId": testVoice}, "Channel not found by channelId"},
	} {
		_, err := callTool(t, ts, context.Background(), "wait_for_messages", tc.args)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%v: got %v, want %q", tc.args, err, tc.want)
		}
	}
	if ts.hub.waiting() != 0 {
		t.Error("waiter leaked on a parameter error")
	}
}

// --- create_thread ---

func TestCreateThreadValidation(t *testing.T) {
	ts, api := newBusToolset(t)
	for _, tc := range []struct {
		args map[string]string
		want string
	}{
		{map[string]string{"name": "x"}, "channelId cannot be null"},
		{map[string]string{"channelId": testText}, "name cannot be null"},
		{map[string]string{"channelId": testText, "name": "   "}, "name must be 1 to 100 characters"},
		{map[string]string{"channelId": testText, "name": strings.Repeat("é", 101)}, "name must be 1 to 100 characters"},
		{map[string]string{"channelId": testText, "name": "x", "private": "yes"}, "private must be true or false"},
		{map[string]string{"channelId": testText, "name": "x", "private": "true", "messageId": "800000000000000001"},
			"private threads cannot be started from a message; omit messageId or set private to false"},
		{map[string]string{"channelId": testText, "name": "x", "autoArchiveMinutes": "30"}, "autoArchiveMinutes must be 60, 1440, 4320 or 10080"},
		{map[string]string{"channelId": "200000000000000099", "name": "x"}, "Channel not found by channelId"},
		{map[string]string{"channelId": testForum, "name": "x"}, "This is a forum channel; use create_forum_post to start a post (a post is a thread)"},
		{map[string]string{"channelId": testVoice, "name": "x"}, "Threads can only be created in text or announcement channels"},
		{map[string]string{"channelId": testNews, "name": "x", "private": "true"}, "Announcement channels do not support private threads"},
	} {
		_, err := callTool(t, ts, context.Background(), "create_thread", tc.args)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%v: got %v, want %q", tc.args, err, tc.want)
		}
	}
	if len(api.requests) != 0 {
		t.Errorf("validation errors must not call Discord: %+v", api.requests)
	}
}

func TestCreateThreadRequests(t *testing.T) {
	ts, api := newBusToolset(t)
	api.handle = func(method, path string, body map[string]any) (int, any) {
		typ := discordgo.ChannelTypeGuildPublicThread
		if v, ok := body["type"].(float64); ok {
			typ = discordgo.ChannelType(v)
		}
		return 201, &discordgo.Channel{ID: "750000000000000001", Name: body["name"].(string), Type: typ, ParentID: testText}
	}
	for _, tc := range []struct {
		name     string
		args     map[string]string
		path     string
		wantType float64 // 0: no type sent
		archive  float64
	}{
		{"standalone public", map[string]string{"channelId": testText, "name": "plan"}, "/channels/" + testText + "/threads", 11, 1440},
		{"private", map[string]string{"channelId": testText, "name": "plan", "private": "true", "autoArchiveMinutes": "60"}, "/channels/" + testText + "/threads", 12, 60},
		{"from message", map[string]string{"channelId": testText, "name": "plan", "messageId": "800000000000000001", "autoArchiveMinutes": "10080"},
			"/channels/" + testText + "/messages/800000000000000001/threads", 0, 10080},
		{"announcement", map[string]string{"channelId": testNews, "name": "plan"}, "/channels/" + testNews + "/threads", 10, 1440},
	} {
		got, err := callTool(t, ts, context.Background(), "create_thread", tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		req := api.last()
		if req.Method != http.MethodPost || req.Path != tc.path {
			t.Errorf("%s: request %s %s", tc.name, req.Method, req.Path)
		}
		if typ, _ := req.Body["type"].(float64); typ != tc.wantType {
			t.Errorf("%s: type %v, want %v", tc.name, req.Body["type"], tc.wantType)
		}
		if req.Body["auto_archive_duration"] != tc.archive || req.Body["name"] != "plan" {
			t.Errorf("%s: body %v", tc.name, req.Body)
		}
		if !strings.HasPrefix(got, "Created thread: **plan** (ID: 750000000000000001)") ||
			!strings.Contains(got, "Use the thread ID as channelId in send_message and read_messages.") {
			t.Errorf("%s: result\n%s", tc.name, got)
		}
		// The new thread is usable at once with the message tools.
		if ts.messageChannel("750000000000000001") == nil {
			t.Errorf("%s: thread not in the cache", tc.name)
		}
	}
}

// --- send_message ---

func TestSendMessageUnchangedWithoutReply(t *testing.T) {
	ts, api := newBusToolset(t)
	api.handle = func(string, string, map[string]any) (int, any) {
		return 200, busMsg("810000000000000001", testText, "hello")
	}
	got, err := callTool(t, ts, context.Background(), "send_message", map[string]string{"channelId": testText, "message": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "Message sent successfully. Message link: https://discord.com/channels/"+testGuild+"/"+testText+"/810000000000000001" {
		t.Errorf("result %q", got)
	}
	body := api.last().Body
	if _, ok := body["message_reference"]; ok {
		t.Errorf("plain send carries a reference: %v", body)
	}
	if _, ok := body["allowed_mentions"]; ok {
		t.Errorf("plain send changed allowed mentions: %v", body)
	}
}

func TestSendMessageAsReply(t *testing.T) {
	ts, api := newBusToolset(t)
	api.handle = func(string, string, map[string]any) (int, any) {
		return 200, busMsg("810000000000000002", testText, "answer")
	}
	got, err := callTool(t, ts, context.Background(), "send_message",
		map[string]string{"channelId": testText, "message": "answer", "replyToMessageId": "800000000000000001"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "Message sent successfully.") {
		t.Errorf("result %q", got)
	}
	body := api.last().Body
	ref, _ := body["message_reference"].(map[string]any)
	if ref["message_id"] != "800000000000000001" || ref["channel_id"] != testText || ref["fail_if_not_exists"] != true {
		t.Errorf("reference %v", ref)
	}
	am, _ := body["allowed_mentions"].(map[string]any)
	if am["replied_user"] != false {
		t.Errorf("reply pings its author: %v", am)
	}
	if parse, _ := am["parse"].([]any); len(parse) != 3 {
		t.Errorf("mention parsing in the text should be unchanged: %v", am)
	}

	// An unknown message ID returns Discord's error.
	api.handle = func(string, string, map[string]any) (int, any) {
		return 400, map[string]any{"code": 50035, "message": "Invalid Form Body"}
	}
	_, err = callTool(t, ts, context.Background(), "send_message",
		map[string]string{"channelId": testText, "message": "x", "replyToMessageId": "800000000000000099"})
	if err == nil || ErrorText(err) != "50035: Invalid Form Body" {
		t.Errorf("unknown reply target: %v", err)
	}
}

func TestToolCount(t *testing.T) {
	ts, _ := newBusToolset(t)
	if n := len(ts.All()); n != 84 {
		t.Errorf("tool count = %d, want 84", n)
	}
}
