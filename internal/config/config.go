// Package config reads the server configuration from the environment.
package config

import (
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
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
}

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
	return cfg, nil
}

var snowflakeRe = regexp.MustCompile(`^\d{17,20}$`)

func loadAuth(cfg *Config, getenv func(string) string) error {
	cfg.PublicURL = strings.TrimRight(strings.TrimSpace(getenv("MCP_PUBLIC_URL")), "/")
	cfg.DiscordClientID = strings.TrimSpace(getenv("DISCORD_CLIENT_ID"))
	cfg.DiscordClientSecret = strings.TrimSpace(getenv("DISCORD_CLIENT_SECRET"))
	cfg.AuthToken = strings.TrimSpace(getenv("MCP_AUTH_TOKEN"))
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
