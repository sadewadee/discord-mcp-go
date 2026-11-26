// Command discord-mcp is an MCP server that manages a Discord server through
// a bot account.
//
// In the default stdio mode stdout carries MCP messages only: logs go to a
// file, and startup failures go to stderr, where MCP clients surface them.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/bwmarrin/discordgo"

	"github.com/sadewadee/discord-mcp-go/internal/config"
	"github.com/sadewadee/discord-mcp-go/internal/gateway"
	"github.com/sadewadee/discord-mcp-go/internal/server"
	"github.com/sadewadee/discord-mcp-go/internal/tools"
)

func main() {
	os.Exit(run())
}

func run() int {
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
		err = server.RunHTTP(ctx, srv, cfg.Port, logger)
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
