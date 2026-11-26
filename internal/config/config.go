// Package config reads the server configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Transport selects how MCP clients talk to the server.
type Transport string

const (
	// TransportStdio serves MCP over stdin/stdout. It is the default.
	TransportStdio Transport = "stdio"
	// TransportHTTP serves streamable HTTP at /mcp.
	TransportHTTP Transport = "http"
)

// DefaultPort is the HTTP port used when PORT is not set.
const DefaultPort = 8085

// DefaultLogFile is where logs go when LOG_FILE is not set.
const DefaultLogFile = "./logs/discord-mcp-server.log"

// Config holds the values read from the environment.
type Config struct {
	Token          string
	DefaultGuildID string
	Transport      Transport
	Port           int
	LogFile        string
}

// Load reads the configuration from the environment using getenv.
// It does not check that DISCORD_TOKEN is set; the caller reports that
// separately so the message matches the startup error format.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Token:          getenv("DISCORD_TOKEN"),
		DefaultGuildID: strings.TrimSpace(getenv("DISCORD_GUILD_ID")),
		Transport:      TransportStdio,
		Port:           DefaultPort,
		LogFile:        DefaultLogFile,
	}

	switch t := strings.ToLower(strings.TrimSpace(getenv("MCP_TRANSPORT"))); t {
	case "", "stdio":
	case "http":
		cfg.Transport = TransportHTTP
	default:
		return cfg, fmt.Errorf("MCP_TRANSPORT must be \"stdio\" or \"http\", got %q", t)
	}

	if p := strings.TrimSpace(getenv("PORT")); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return cfg, fmt.Errorf("PORT must be a number between 1 and 65535, got %q", p)
		}
		cfg.Port = port
	}

	if f := strings.TrimSpace(getenv("LOG_FILE")); f != "" {
		cfg.LogFile = f
	}
	return cfg, nil
}

// FromEnv loads the configuration from the process environment.
func FromEnv() (Config, error) {
	return Load(os.Getenv)
}
