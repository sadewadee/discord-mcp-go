// Package config reads the server configuration from the environment.
package config

import (
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Transport selects how MCP clients talk to the server.
type Transport string

const (
	// TransportStdio serves MCP over stdin/stdout.
	TransportStdio Transport = "stdio"
	// TransportHTTP serves streamable HTTP at /mcp on Port.
	TransportHTTP Transport = "http"
)

// Port is the HTTP port.
const Port = 8085

// DefaultLogFile is where logs go when LOG_FILE is not set.
const DefaultLogFile = "./logs/discord-mcp-server.log"

// DefaultOAuthDataFile is where OAuth clients and tokens are kept when
// OAUTH_DATA_FILE is not set.
const DefaultOAuthDataFile = "./data/oauth.json"

// minAuthTokenLength is the shortest MCP_AUTH_TOKEN accepted.
const minAuthTokenLength = 32

// Config holds the values read from the environment.
type Config struct {
	Token          string
	DefaultGuildID string
	Transport      Transport
	// TransportReason says why Transport was chosen, for the log.
	TransportReason string
	LogFile         string

	// HTTP authentication. OAuth is enabled when PublicURL is set.
	PublicURL           string   // MCP_PUBLIC_URL
	DiscordClientID     string   // DISCORD_CLIENT_ID; empty means the bot's own application
	DiscordClientSecret string   // DISCORD_CLIENT_SECRET
	AllowedUsers        []string // MCP_ALLOWED_USERS
	OAuthDataFile       string   // OAUTH_DATA_FILE
	AuthToken           string   // MCP_AUTH_TOKEN
	// InsecureNoAuth allows HTTP without authentication (MCP_INSECURE_NO_AUTH=true),
	// for example behind a proxy that authenticates on its own.
	InsecureNoAuth bool

	AutoReply AutoReply
}

// AutoReply configures the listener that answers mentions of the bot.
type AutoReply struct {
	Enabled         bool          // AUTOREPLY_ENABLED
	BaseURL         string        // AUTOREPLY_LLM_BASE_URL
	APIKey          string        // AUTOREPLY_LLM_API_KEY
	Model           string        // AUTOREPLY_LLM_MODEL
	SystemPrompt    string        // AUTOREPLY_SYSTEM_PROMPT or the content of AUTOREPLY_SYSTEM_PROMPT_FILE
	ChannelIDs      []string      // AUTOREPLY_CHANNEL_IDS
	ContextMessages int           // AUTOREPLY_CONTEXT_MESSAGES
	MaxTokens       int           // AUTOREPLY_MAX_TOKENS
	Timeout         time.Duration // AUTOREPLY_TIMEOUT_SECONDS
	UserCooldown    time.Duration // AUTOREPLY_USER_COOLDOWN_SECONDS
	MaxConcurrent   int           // AUTOREPLY_MAX_CONCURRENT
}

// DefaultAutoReplyPrompt is the system prompt used when none is configured.
const DefaultAutoReplyPrompt = "You are a helpful assistant in a Discord server. Answer the last message briefly and clearly, " +
	"in the language it was written in. Earlier messages are context; each user message starts with the author's name. " +
	"If you do not know something, say so instead of guessing."

// OAuthEnabled reports whether OAuth sign-in is configured.
func (c Config) OAuthEnabled() bool { return c.PublicURL != "" }

// AuthEnabled reports whether the HTTP endpoint requires a token.
func (c Config) AuthEnabled() bool { return c.OAuthEnabled() || c.AuthToken != "" }

// Load reads the configuration using getenv. stdin describes the process's
// standard input and is used to pick the transport when MCP_TRANSPORT is not
// set (see DetectTransport). It does not check that DISCORD_TOKEN is set; the
// caller reports that separately.
func Load(getenv func(string) string, stdin fs.FileMode, stdinErr error) (Config, error) {
	cfg := Config{
		Token:          getenv("DISCORD_TOKEN"),
		DefaultGuildID: strings.TrimSpace(getenv("DISCORD_GUILD_ID")),
		LogFile:        DefaultLogFile,
	}

	switch t := strings.ToLower(strings.TrimSpace(getenv("MCP_TRANSPORT"))); t {
	case "", "auto":
		cfg.Transport, cfg.TransportReason = DetectTransport(stdin, stdinErr)
	case "stdio":
		cfg.Transport, cfg.TransportReason = TransportStdio, "set by MCP_TRANSPORT"
	case "http":
		cfg.Transport, cfg.TransportReason = TransportHTTP, "set by MCP_TRANSPORT"
	default:
		return cfg, fmt.Errorf("MCP_TRANSPORT must be \"auto\", \"stdio\" or \"http\", got %q", t)
	}

	if f := strings.TrimSpace(getenv("LOG_FILE")); f != "" {
		cfg.LogFile = f
	}
	if err := loadAuth(&cfg, getenv); err != nil {
		return cfg, err
	}
	if err := requireHTTPAuth(cfg); err != nil {
		return cfg, err
	}
	if err := loadAutoReply(&cfg, getenv, os.ReadFile); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// loadAutoReply reads the AUTOREPLY_* variables. With AUTOREPLY_ENABLED
// unset or false the others are ignored.
func loadAutoReply(cfg *Config, getenv func(string) string, readFile func(string) ([]byte, error)) error {
	enabled, err := parseBool(getenv, "AUTOREPLY_ENABLED")
	if err != nil || !enabled {
		return err
	}
	a := AutoReply{
		Enabled: true,
		BaseURL: strings.TrimRight(strings.TrimSpace(getenv("AUTOREPLY_LLM_BASE_URL")), "/"),
		APIKey:  strings.TrimSpace(getenv("AUTOREPLY_LLM_API_KEY")),
		Model:   strings.TrimSpace(getenv("AUTOREPLY_LLM_MODEL")),
	}
	for _, req := range []struct{ name, value string }{
		{"AUTOREPLY_LLM_BASE_URL", a.BaseURL},
		{"AUTOREPLY_LLM_API_KEY", a.APIKey},
		{"AUTOREPLY_LLM_MODEL", a.Model},
	} {
		if req.value == "" {
			return fmt.Errorf("AUTOREPLY_ENABLED is true but %s is not set", req.name)
		}
	}
	if u, err := url.Parse(a.BaseURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("AUTOREPLY_LLM_BASE_URL must be an http(s) URL including /v1, e.g. https://api.example.com/v1, got %q", a.BaseURL)
	}
	if cfg.Transport != TransportHTTP {
		return fmt.Errorf("AUTOREPLY_ENABLED needs the HTTP transport, but this process uses stdio (%s).\n"+
			"  In stdio mode every MCP client starts its own server process, so the listener would answer once per connected client.\n"+
			"  Run the listener in the long-running HTTP server (for example the Docker container), or set AUTOREPLY_ENABLED=false here",
			cfg.TransportReason)
	}

	a.SystemPrompt = DefaultAutoReplyPrompt
	if p := strings.TrimSpace(getenv("AUTOREPLY_SYSTEM_PROMPT")); p != "" {
		a.SystemPrompt = p
	}
	if f := strings.TrimSpace(getenv("AUTOREPLY_SYSTEM_PROMPT_FILE")); f != "" {
		b, err := readFile(f)
		if err != nil {
			return fmt.Errorf("AUTOREPLY_SYSTEM_PROMPT_FILE cannot be read: %w", err)
		}
		if strings.TrimSpace(string(b)) == "" {
			return fmt.Errorf("AUTOREPLY_SYSTEM_PROMPT_FILE %s is empty", f)
		}
		a.SystemPrompt = strings.TrimSpace(string(b))
	}

	for _, id := range strings.Split(getenv("AUTOREPLY_CHANNEL_IDS"), ",") {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		if !snowflakeRe.MatchString(id) {
			return fmt.Errorf("AUTOREPLY_CHANNEL_IDS must be a comma-separated list of channel IDs, got %q", id)
		}
		a.ChannelIDs = append(a.ChannelIDs, id)
	}

	type intVar struct {
		name          string
		def, min, max int
		dst           *int
	}
	var timeout, cooldown int
	for _, n := range []intVar{
		{"AUTOREPLY_CONTEXT_MESSAGES", 15, 0, 50, &a.ContextMessages},
		{"AUTOREPLY_MAX_TOKENS", 800, 1, 32000, &a.MaxTokens},
		{"AUTOREPLY_TIMEOUT_SECONDS", 60, 1, 600, &timeout},
		{"AUTOREPLY_USER_COOLDOWN_SECONDS", 10, 0, 3600, &cooldown},
		{"AUTOREPLY_MAX_CONCURRENT", 4, 1, 32, &a.MaxConcurrent},
	} {
		v, err := parseIntRange(getenv, n.name, n.def, n.min, n.max)
		if err != nil {
			return err
		}
		*n.dst = v
	}
	a.Timeout = time.Duration(timeout) * time.Second
	a.UserCooldown = time.Duration(cooldown) * time.Second
	cfg.AutoReply = a
	return nil
}

func parseBool(getenv func(string) string, name string) (bool, error) {
	switch v := strings.ToLower(strings.TrimSpace(getenv(name))); v {
	case "", "false", "0", "no":
		return false, nil
	case "true", "1", "yes":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be true or false, got %q", name, v)
	}
}

func parseIntRange(getenv func(string) string, name string, def, min, max int) (int, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be a whole number from %d to %d, got %q", name, min, max, raw)
	}
	return n, nil
}

// requireHTTPAuth refuses to serve HTTP without authentication: the HTTP
// endpoint controls the bot, so an open one is never the default.
func requireHTTPAuth(cfg Config) error {
	if cfg.Transport != TransportHTTP || cfg.AuthEnabled() || cfg.InsecureNoAuth {
		return nil
	}
	return fmt.Errorf("the HTTP transport needs authentication, and none is configured (HTTP was chosen because %s).\n"+
		"  Set one of:\n"+
		"    MCP_AUTH_TOKEN         a bearer token of at least %d characters, e.g. from: openssl rand -hex 32\n"+
		"    MCP_PUBLIC_URL + DISCORD_CLIENT_SECRET   OAuth with \"Sign in with Discord\" (see README)\n"+
		"  To serve HTTP without authentication anyway, for example behind a proxy that authenticates, set MCP_INSECURE_NO_AUTH=true.\n"+
		"  To use stdio instead, start the server from an MCP client or set MCP_TRANSPORT=stdio",
		cfg.TransportReason, minAuthTokenLength)
}

var snowflakeRe = regexp.MustCompile(`^\d{17,20}$`)

func loadAuth(cfg *Config, getenv func(string) string) error {
	cfg.PublicURL = strings.TrimRight(strings.TrimSpace(getenv("MCP_PUBLIC_URL")), "/")
	cfg.DiscordClientID = strings.TrimSpace(getenv("DISCORD_CLIENT_ID"))
	cfg.DiscordClientSecret = strings.TrimSpace(getenv("DISCORD_CLIENT_SECRET"))
	cfg.AuthToken = strings.TrimSpace(getenv("MCP_AUTH_TOKEN"))
	switch v := strings.ToLower(strings.TrimSpace(getenv("MCP_INSECURE_NO_AUTH"))); v {
	case "", "false", "0", "no":
	case "true", "1", "yes":
		cfg.InsecureNoAuth = true
	default:
		return fmt.Errorf("MCP_INSECURE_NO_AUTH must be true or false, got %q", v)
	}
	cfg.OAuthDataFile = strings.TrimSpace(getenv("OAUTH_DATA_FILE"))
	if cfg.OAuthDataFile == "" {
		cfg.OAuthDataFile = DefaultOAuthDataFile
	}
	for _, id := range strings.Split(getenv("MCP_ALLOWED_USERS"), ",") {
		if id = strings.TrimSpace(id); id == "" {
			continue
		}
		if !snowflakeRe.MatchString(id) {
			return fmt.Errorf("MCP_ALLOWED_USERS must be a comma-separated list of Discord user IDs, got %q", id)
		}
		cfg.AllowedUsers = append(cfg.AllowedUsers, id)
	}

	if cfg.AuthToken != "" && len(cfg.AuthToken) < minAuthTokenLength {
		return fmt.Errorf("MCP_AUTH_TOKEN must be at least %d characters; generate one with: openssl rand -hex 32", minAuthTokenLength)
	}
	if cfg.PublicURL == "" {
		if cfg.DiscordClientSecret != "" || cfg.DiscordClientID != "" {
			return fmt.Errorf("DISCORD_CLIENT_ID/DISCORD_CLIENT_SECRET are set but MCP_PUBLIC_URL is not; set MCP_PUBLIC_URL to enable OAuth")
		}
		return nil
	}
	if err := validatePublicURL(cfg.PublicURL); err != nil {
		return err
	}
	if cfg.DiscordClientSecret == "" {
		return fmt.Errorf("MCP_PUBLIC_URL enables OAuth, which needs DISCORD_CLIENT_SECRET (Developer Portal -> OAuth2 -> Client Secret)")
	}
	if cfg.DiscordClientID != "" && !snowflakeRe.MatchString(cfg.DiscordClientID) {
		return fmt.Errorf("DISCORD_CLIENT_ID must be a Discord application ID, got %q", cfg.DiscordClientID)
	}
	if len(cfg.AllowedUsers) == 0 && cfg.DefaultGuildID == "" {
		return fmt.Errorf("OAuth needs to know who may sign in: set MCP_ALLOWED_USERS (Discord user IDs) or DISCORD_GUILD_ID (its owner is allowed)")
	}
	return nil
}

// validatePublicURL requires an https origin (http only for localhost)
// without a path, query or fragment.
func validatePublicURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("MCP_PUBLIC_URL must be an absolute URL such as https://mcp.example.com, got %q", raw)
	}
	if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("MCP_PUBLIC_URL must be just the scheme and host (no path), got %q", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		host := u.Hostname()
		if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
	}
	return fmt.Errorf("MCP_PUBLIC_URL must use https (http is allowed only for localhost), got %q", raw)
}

// DetectTransport picks the transport from what standard input is. An MCP
// client that launches the server connects a pipe (or a file or socket) to
// stdin, so that means stdio. A terminal, /dev/null (docker run -d, systemd)
// or a closed stdin means nobody is speaking MCP on stdin, so the server
// serves HTTP instead.
func DetectTransport(stdin fs.FileMode, stdinErr error) (Transport, string) {
	switch {
	case stdinErr != nil:
		return TransportHTTP, "stdin is not available"
	case stdin&fs.ModeCharDevice != 0:
		return TransportHTTP, "stdin is a terminal or /dev/null"
	default:
		return TransportStdio, "stdin is a pipe"
	}
}

// FromEnv loads the configuration from the process environment and stdin.
func FromEnv() (Config, error) {
	var mode fs.FileMode
	fi, err := os.Stdin.Stat()
	if err == nil {
		mode = fi.Mode()
	}
	return Load(os.Getenv, mode, err)
}
