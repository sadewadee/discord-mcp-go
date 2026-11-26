// Command discord-mcp is an MCP server that manages a Discord server through
// a bot account.
//
// In the default stdio mode stdout carries MCP messages only: logs go to a
// file, and startup failures go to stderr, where MCP clients surface them.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/bwmarrin/discordgo"

	"github.com/sadewadee/discord-mcp-go/internal/config"
	"github.com/sadewadee/discord-mcp-go/internal/gateway"
	"github.com/sadewadee/discord-mcp-go/internal/oauth"
	"github.com/sadewadee/discord-mcp-go/internal/server"
	"github.com/sadewadee/discord-mcp-go/internal/tools"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return 0
	}
	server.Version = version

	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: "+err.Error())
		return 1
	}
	if cfg.Token == "" {
		fmt.Fprintln(os.Stderr, "ERROR: The environment variable DISCORD_TOKEN is not set. Please set it to run the application properly.")
		return 1
	}

	logger, closeLog, err := newLogger(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR: cannot open log file: "+err.Error())
		return 1
	}
	defer closeLog()

	logger.Info("MCP transport selected", "transport", cfg.Transport, "reason", cfg.TransportReason)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	session, err := gateway.Connect(cfg.Token, logger)
	if err != nil {
		logger.Error("discord connection failed", "error", err)
		fmt.Fprint(os.Stderr, gateway.StartupFailure(err))
		return 1
	}
	defer session.Close()

	toolset := tools.New(session, cfg.DefaultGuildID, logger)
	all := toolset.All()
	srv := server.New(all, logger)
	logger.Info("tools registered", "count", len(all))

	switch cfg.Transport {
	case config.TransportHTTP:
		auth, aerr := httpAuth(cfg, session, logger)
		if aerr != nil {
			logger.Error("HTTP authentication setup failed", "error", aerr)
			fmt.Fprintln(os.Stderr, "ERROR: "+aerr.Error())
			return 1
		}
		err = server.RunHTTP(ctx, srv, config.Port, logger, auth)
	default:
		logger.Info("MCP transport: stdio. Each client session starts a separate server process.")
		err = server.RunStdio(ctx, srv)
	}
	if err != nil && ctx.Err() == nil {
		logger.Error("server stopped", "error", err)
		fmt.Fprintln(os.Stderr, "ERROR: "+err.Error())
		return 1
	}
	return 0
}

// newLogger logs to cfg.LogFile, and also to stdout in HTTP mode. In stdio
// mode stdout belongs to MCP, so nothing else may be written there. The
// standard library logger and discordgo's logger are routed to the same
// place.
func newLogger(cfg config.Config) (*slog.Logger, func(), error) {
	if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0o755); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	var w io.Writer = f
	if cfg.Transport == config.TransportHTTP {
		w = io.MultiWriter(f, os.Stdout)
	}
	logger := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.SetOutput(w)

	discordLog := logger.With("component", "discordgo")
	discordgo.Logger = func(level, _ int, format string, a ...any) {
		msg := fmt.Sprintf(format, a...)
		switch level {
		case discordgo.LogError:
			discordLog.Error(msg)
		case discordgo.LogWarning:
			discordLog.Warn(msg)
		case discordgo.LogInformational:
			discordLog.Info(msg)
		default:
			discordLog.Debug(msg)
		}
	}
	return logger, func() { _ = f.Close() }, nil
}

// staticAuth protects /mcp with MCP_AUTH_TOKEN only.
type staticAuth struct{ token string }

func (staticAuth) Register(*http.ServeMux) {}

func (a staticAuth) Protect(h http.Handler) http.Handler { return oauth.StaticTokenOnly(a.token, h) }

// httpAuth builds the authentication for the HTTP transport: OAuth with
// Discord sign-in when MCP_PUBLIC_URL is set, a static bearer token when
// only MCP_AUTH_TOKEN is set, or none (with a warning).
func httpAuth(cfg config.Config, session *discordgo.Session, logger *slog.Logger) (server.Auth, error) {
	switch {
	case cfg.OAuthEnabled():
		clientID := cfg.DiscordClientID
		if clientID == "" && session.State.Application != nil {
			clientID = session.State.Application.ID
		}
		if clientID == "" {
			return nil, fmt.Errorf("cannot determine the Discord application ID; set DISCORD_CLIENT_ID")
		}
		o, err := oauth.New(oauth.Config{
			PublicURL:           cfg.PublicURL,
			DiscordClientID:     clientID,
			DiscordClientSecret: cfg.DiscordClientSecret,
			StaticToken:         cfg.AuthToken,
			Allowed:             allowedUsers(cfg, session),
			DataFile:            cfg.OAuthDataFile,
			Logger:              logger.With("component", "oauth"),
		})
		if err != nil {
			return nil, err
		}
		logger.Info("OAuth enabled", "issuer", cfg.PublicURL, "resource", o.Resource(),
			"discord_redirect_uri", cfg.PublicURL+"/oauth/discord/callback",
			"allowed_users", len(cfg.AllowedUsers), "guild_owner_allowed", cfg.DefaultGuildID != "",
			"static_token", cfg.AuthToken != "")
		return o, nil
	case cfg.AuthToken != "":
		logger.Info("HTTP endpoint protected by MCP_AUTH_TOKEN")
		return staticAuth{cfg.AuthToken}, nil
	default:
		// Only reachable with MCP_INSECURE_NO_AUTH=true; config refuses otherwise.
		logger.Warn("HTTP endpoint has NO authentication (MCP_INSECURE_NO_AUTH=true): anyone who can reach port 8085 can use every tool")
		return nil, nil
	}
}

// allowedUsers returns the sign-in check: a user listed in
// MCP_ALLOWED_USERS, or the owner of DISCORD_GUILD_ID.
func allowedUsers(cfg config.Config, session *discordgo.Session) func(string) bool {
	listed := map[string]bool{}
	for _, id := range cfg.AllowedUsers {
		listed[id] = true
	}
	return func(userID string) bool {
		if listed[userID] {
			return true
		}
		if cfg.DefaultGuildID == "" {
			return false
		}
		g, err := session.State.Guild(cfg.DefaultGuildID)
		if err != nil || g == nil {
			return false
		}
		session.State.RLock()
		defer session.State.RUnlock()
		return g.OwnerID == userID
	}
}
