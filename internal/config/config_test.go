package config

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"
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

func TestAutoReplyConfig(t *testing.T) {
	base := map[string]string{
		"MCP_TRANSPORT":          "http",
		"MCP_AUTH_TOKEN":         validToken,
		"AUTOREPLY_ENABLED":      "true",
		"AUTOREPLY_LLM_BASE_URL": "https://9api.example.com/v1/",
		"AUTOREPLY_LLM_API_KEY":  "key",
		"AUTOREPLY_LLM_MODEL":    "some-model",
	}
	with := func(kv ...string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			if kv[i+1] == "" {
				delete(m, kv[i])
			} else {
				m[kv[i]] = kv[i+1]
			}
		}
		return m
	}

	cfg, err := Load(env(base), pipe, nil)
	if err != nil {
		t.Fatal(err)
	}
	a := cfg.AutoReply
	if !a.Enabled || a.BaseURL != "https://9api.example.com/v1" || a.Model != "some-model" || a.SystemPrompt != DefaultAutoReplyPrompt ||
		a.ContextMessages != 15 || a.MaxTokens != 800 || a.Timeout != 60*time.Second || a.UserCooldown != 10*time.Second ||
		a.MaxConcurrent != 4 || len(a.ChannelIDs) != 0 {
		t.Errorf("defaults: %+v", a)
	}

	// Disabled: nothing else is read or validated.
	cfg, err = Load(env(map[string]string{"AUTOREPLY_LLM_MODEL": "x", "AUTOREPLY_CONTEXT_MESSAGES": "999"}), pipe, nil)
	if err != nil || cfg.AutoReply.Enabled {
		t.Errorf("disabled: %+v %v", cfg.AutoReply, err)
	}

	cfg, err = Load(env(with(
		"AUTOREPLY_CHANNEL_IDS", "200000000000000002, 200000000000000003",
		"AUTOREPLY_CONTEXT_MESSAGES", "0",
		"AUTOREPLY_USER_COOLDOWN_SECONDS", "0",
		"AUTOREPLY_SYSTEM_PROMPT", "Be brief.",
	)), pipe, nil)
	if err != nil || len(cfg.AutoReply.ChannelIDs) != 2 || cfg.AutoReply.ContextMessages != 0 ||
		cfg.AutoReply.UserCooldown != 0 || cfg.AutoReply.SystemPrompt != "Be brief." {
		t.Errorf("overrides: %+v %v", cfg.AutoReply, err)
	}

	errCases := map[string]struct {
		env  map[string]string
		want string
	}{
		"missing base URL":    {with("AUTOREPLY_LLM_BASE_URL", ""), "AUTOREPLY_LLM_BASE_URL is not set"},
		"missing key":         {with("AUTOREPLY_LLM_API_KEY", ""), "AUTOREPLY_LLM_API_KEY is not set"},
		"missing model":       {with("AUTOREPLY_LLM_MODEL", ""), "AUTOREPLY_LLM_MODEL is not set"},
		"bad base URL":        {with("AUTOREPLY_LLM_BASE_URL", "9api.example.com"), "AUTOREPLY_LLM_BASE_URL must be"},
		"bad enabled value":   {with("AUTOREPLY_ENABLED", "maybe"), "AUTOREPLY_ENABLED must be true or false"},
		"stdio":               {with("MCP_TRANSPORT", "stdio"), "answer once per connected client"},
		"context too large":   {with("AUTOREPLY_CONTEXT_MESSAGES", "51"), "AUTOREPLY_CONTEXT_MESSAGES must be a whole number from 0 to 50"},
		"context negative":    {with("AUTOREPLY_CONTEXT_MESSAGES", "-1"), "from 0 to 50"},
		"max tokens zero":     {with("AUTOREPLY_MAX_TOKENS", "0"), "AUTOREPLY_MAX_TOKENS must be a whole number from 1"},
		"timeout not number":  {with("AUTOREPLY_TIMEOUT_SECONDS", "1m"), "AUTOREPLY_TIMEOUT_SECONDS must be a whole number from 1 to 600"},
		"cooldown too large":  {with("AUTOREPLY_USER_COOLDOWN_SECONDS", "3601"), "AUTOREPLY_USER_COOLDOWN_SECONDS"},
		"concurrency zero":    {with("AUTOREPLY_MAX_CONCURRENT", "0"), "AUTOREPLY_MAX_CONCURRENT must be a whole number from 1 to 32"},
		"bad channel ID":      {with("AUTOREPLY_CHANNEL_IDS", "general"), "AUTOREPLY_CHANNEL_IDS must be"},
		"missing prompt file": {with("AUTOREPLY_SYSTEM_PROMPT_FILE", "/nonexistent/prompt.txt"), "AUTOREPLY_SYSTEM_PROMPT_FILE cannot be read"},
	}
	for name, tc := range errCases {
		_, err := Load(env(tc.env), pipe, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want error containing %q", name, err, tc.want)
		}
	}
	// The stdio check also applies when stdio was chosen automatically.
	auto := with("MCP_TRANSPORT", "")
	if _, err := Load(env(auto), pipe, nil); err == nil || !strings.Contains(err.Error(), "stdin is a pipe") {
		t.Errorf("auto stdio: %v", err)
	}
}

func TestAutoReplyPromptFile(t *testing.T) {
	cfg := Config{Transport: TransportHTTP}
	files := map[string]string{"/p.txt": "  From the file.\n", "/empty.txt": " \n"}
	read := func(name string) ([]byte, error) {
		if c, ok := files[name]; ok {
			return []byte(c), nil
		}
		return nil, errors.New("no such file")
	}
	get := func(file string) func(string) string {
		return env(map[string]string{
			"AUTOREPLY_ENABLED": "true", "AUTOREPLY_LLM_BASE_URL": "https://x.example/v1", "AUTOREPLY_LLM_API_KEY": "k",
			"AUTOREPLY_LLM_MODEL": "m", "AUTOREPLY_SYSTEM_PROMPT": "from env", "AUTOREPLY_SYSTEM_PROMPT_FILE": file,
		})
	}
	if err := loadAutoReply(&cfg, get("/p.txt"), read); err != nil || cfg.AutoReply.SystemPrompt != "From the file." {
		t.Errorf("file wins over env: %q %v", cfg.AutoReply.SystemPrompt, err)
	}
	if err := loadAutoReply(&cfg, get("/empty.txt"), read); err == nil {
		t.Error("empty prompt file accepted")
	}
}
