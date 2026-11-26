package config

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

var validToken = strings.Repeat("t", 32)

const (
	pipe     = fs.ModeNamedPipe
	terminal = fs.ModeDevice | fs.ModeCharDevice
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DISCORD_TOKEN": "tok"}), pipe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "tok" || cfg.Transport != TransportStdio || cfg.LogFile != DefaultLogFile || cfg.DefaultGuildID != "" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestDetectTransport(t *testing.T) {
	tests := []struct {
		name string
		mode fs.FileMode
		err  error
		want Transport
	}{
		{"pipe from an MCP client", pipe, nil, TransportStdio},
		{"socket", fs.ModeSocket, nil, TransportStdio},
		{"redirected file", 0, nil, TransportStdio},
		{"terminal", terminal, nil, TransportHTTP},
		{"/dev/null", fs.ModeDevice | fs.ModeCharDevice, nil, TransportHTTP},
		{"closed stdin", 0, errors.New("bad file descriptor"), TransportHTTP},
	}
	for _, tt := range tests {
		got, reason := DetectTransport(tt.mode, tt.err)
		if got != tt.want || reason == "" {
			t.Errorf("%s: got %q (%q), want %q", tt.name, got, reason, tt.want)
		}
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"DISCORD_GUILD_ID": " 123 ",
		"MCP_TRANSPORT":    "HTTP",
		"MCP_AUTH_TOKEN":   validToken,
		"LOG_FILE":         "/tmp/x.log",
	}), pipe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultGuildID != "123" || cfg.Transport != TransportHTTP || cfg.LogFile != "/tmp/x.log" {
		t.Errorf("unexpected config: %+v", cfg)
	}

	cfg, err = Load(env(map[string]string{"MCP_TRANSPORT": "stdio"}), terminal, nil)
	if err != nil || cfg.Transport != TransportStdio {
		t.Errorf("stdio override: %+v %v", cfg, err)
	}

	cfg, err = Load(env(map[string]string{"MCP_TRANSPORT": "auto", "MCP_AUTH_TOKEN": validToken}), terminal, nil)
	if err != nil || cfg.Transport != TransportHTTP {
		t.Errorf("auto: %+v %v", cfg, err)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(env(map[string]string{"MCP_TRANSPORT": "sse"}), pipe, nil); err == nil {
		t.Error("unknown MCP_TRANSPORT should fail")
	}
}

func TestLoadAuth(t *testing.T) {
	const guild = "100000000000000001"
	base := map[string]string{
		"MCP_PUBLIC_URL":        "https://mcp.example.com/",
		"DISCORD_CLIENT_SECRET": "secret",
		"MCP_ALLOWED_USERS":     "111111111111111111, 222222222222222222",
	}
	cfg, err := Load(env(base), pipe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.OAuthEnabled() || !cfg.AuthEnabled() || cfg.PublicURL != "https://mcp.example.com" ||
		len(cfg.AllowedUsers) != 2 || cfg.OAuthDataFile != DefaultOAuthDataFile {
		t.Errorf("unexpected config: %+v", cfg)
	}

	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for kk, vv := range base {
			m[kk] = vv
		}
		if v == "" {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	bad := []map[string]string{
		with("MCP_PUBLIC_URL", "http://mcp.example.com"),
		with("MCP_PUBLIC_URL", "https://mcp.example.com/sub"),
		with("MCP_PUBLIC_URL", "mcp.example.com"),
		with("DISCORD_CLIENT_SECRET", ""),
		with("DISCORD_CLIENT_ID", "abc"),
		with("MCP_ALLOWED_USERS", "alice"),
		with("MCP_ALLOWED_USERS", ""),       // nobody could sign in
		{"DISCORD_CLIENT_SECRET": "secret"}, // OAuth settings without MCP_PUBLIC_URL
		{"MCP_AUTH_TOKEN": "too-short"},
	}
	for _, m := range bad {
		if _, err := Load(env(m), pipe, nil); err == nil {
			t.Errorf("Load(%v) should fail", m)
		}
	}

	// The guild owner is allowed when no list is given.
	m := with("MCP_ALLOWED_USERS", "")
	m["DISCORD_GUILD_ID"] = guild
	if _, err := Load(env(m), pipe, nil); err != nil {
		t.Errorf("guild owner fallback: %v", err)
	}
	// http is fine for localhost.
	if _, err := Load(env(with("MCP_PUBLIC_URL", "http://localhost:8085")), pipe, nil); err != nil {
		t.Errorf("localhost: %v", err)
	}
	// A static token alone enables auth without OAuth.
	cfg, err = Load(env(map[string]string{"MCP_AUTH_TOKEN": strings.Repeat("x", 32)}), pipe, nil)
	if err != nil || cfg.OAuthEnabled() || !cfg.AuthEnabled() {
		t.Errorf("static token: %+v %v", cfg, err)
	}
}

func TestHTTPRequiresAuth(t *testing.T) {
	// HTTP without any authentication is refused, whether chosen
	// automatically or forced.
	for name, tc := range map[string]struct {
		env   map[string]string
		stdin fs.FileMode
	}{
		"auto from a terminal": {map[string]string{}, terminal},
		"forced http":          {map[string]string{"MCP_TRANSPORT": "http"}, pipe},
		"opt-out set to false": {map[string]string{"MCP_TRANSPORT": "http", "MCP_INSECURE_NO_AUTH": "false"}, pipe},
	} {
		_, err := Load(env(tc.env), tc.stdin, nil)
		if err == nil || !strings.Contains(err.Error(), "needs authentication") || !strings.Contains(err.Error(), "MCP_AUTH_TOKEN") {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := Load(env(map[string]string{}), terminal, nil); err == nil || !strings.Contains(err.Error(), "stdin is a terminal") {
		t.Errorf("error should say why HTTP was chosen: %v", err)
	}

	// Any form of authentication, the explicit opt-out, or stdio is fine.
	for name, tc := range map[string]struct {
		env   map[string]string
		stdin fs.FileMode
	}{
		"static token": {map[string]string{"MCP_AUTH_TOKEN": validToken}, terminal},
		"oauth": {map[string]string{
			"MCP_PUBLIC_URL": "https://mcp.example.com", "DISCORD_CLIENT_SECRET": "s", "MCP_ALLOWED_USERS": "111111111111111111",
		}, terminal},
		"opt-out":         {map[string]string{"MCP_INSECURE_NO_AUTH": "true"}, terminal},
		"stdio from pipe": {map[string]string{}, pipe},
		"forced stdio":    {map[string]string{"MCP_TRANSPORT": "stdio"}, terminal},
	} {
		if _, err := Load(env(tc.env), tc.stdin, nil); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	if _, err := Load(env(map[string]string{"MCP_INSECURE_NO_AUTH": "maybe"}), pipe, nil); err == nil {
		t.Error("invalid MCP_INSECURE_NO_AUTH accepted")
	}
}
