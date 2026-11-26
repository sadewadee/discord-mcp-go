// Package config reads the server configuration from the environment.
package config

import (
	"fmt"
	"io/fs"
	"os"
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

// Config holds the values read from the environment.
type Config struct {
	Token          string
	DefaultGuildID string
	Transport      Transport
	// TransportReason says why Transport was chosen, for the log.
	TransportReason string
	LogFile         string
}

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
	return cfg, nil
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
