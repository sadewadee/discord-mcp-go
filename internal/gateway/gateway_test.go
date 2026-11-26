package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gorilla/websocket"
)

func TestStartupFailureToken(t *testing.T) {
	err := &websocket.CloseError{Code: 4004, Text: "Authentication failed."}
	msg := StartupFailure(err)
	if !strings.HasPrefix(msg, "ERROR: Could not connect to Discord; the MCP server cannot start.\n") {
		t.Errorf("missing header: %q", msg)
	}
	if !strings.Contains(msg, "Discord rejected DISCORD_TOKEN") || !strings.Contains(msg, "Bot -> Reset Token") {
		t.Errorf("missing token advice: %q", msg)
	}
	if !strings.Contains(msg, "  Details: websocket: close 4004: Authentication failed.") {
		t.Errorf("missing details: %q", msg)
	}
}

func TestStartupFailureREST401(t *testing.T) {
	err := &discordgo.RESTError{Response: &http.Response{Status: "401 Unauthorized", StatusCode: 401}, ResponseBody: []byte(`{"message": "401: Unauthorized"}`)}
	if msg := StartupFailure(err); !strings.Contains(msg, "Discord rejected DISCORD_TOKEN") {
		t.Errorf("got %q", msg)
	}
}

func TestStartupFailureIntent(t *testing.T) {
	err := fmt.Errorf("open: %w", &websocket.CloseError{Code: 4014, Text: "Disallowed intent(s)."})
	msg := StartupFailure(err)
	if !strings.Contains(msg, "privileged gateway intent") || !strings.Contains(msg, "'Server Members Intent' (GUILD_MEMBERS)") {
		t.Errorf("missing intent advice: %q", msg)
	}
	if strings.Contains(msg, "DISCORD_TOKEN") {
		t.Errorf("intent failure must not blame the token: %q", msg)
	}
}

func TestStartupFailureOther(t *testing.T) {
	msg := StartupFailure(errors.New("dial tcp: lookup gateway.discord.gg: no such host"))
	if strings.Contains(msg, "Likely cause") {
		t.Errorf("unknown failures get no guess: %q", msg)
	}
	if !strings.HasSuffix(msg, "  Details: dial tcp: lookup gateway.discord.gg: no such host\n") {
		t.Errorf("got %q", msg)
	}
}

func TestReadyWaiter(t *testing.T) {
	w := newReadyWaiter()
	w.ready(&discordgo.Ready{Guilds: []*discordgo.Guild{{ID: "1"}, {ID: "2"}}})
	if !w.guildCreated("1") {
		t.Fatal("first GUILD_CREATE should request members")
	}
	if w.guildCreated("1") {
		t.Fatal("repeated GUILD_CREATE must not request members again")
	}
	w.chunked("1")
	select {
	case <-w.done:
		t.Fatal("done before guild 2 arrived")
	default:
	}
	w.guildCreated("2")
	w.chunked("2")
	select {
	case <-w.done:
	case <-time.After(time.Second):
		t.Fatal("not done after every guild was chunked")
	}
	w.chunked("2") // must not panic on a closed channel
}

func TestReadyWaiterNoGuilds(t *testing.T) {
	w := newReadyWaiter()
	w.ready(&discordgo.Ready{})
	select {
	case <-w.done:
	default:
		t.Fatal("a bot in no guilds is ready at once")
	}
}
