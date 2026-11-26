// Package server exposes the tools over MCP, on stdio or streamable HTTP.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sadewadee/discord-mcp-go/internal/tools"
)

// Name identifies the server to MCP clients.
const Name = "discord-mcp-server"

// Version is reported to MCP clients. Release builds set it from the tag.
var Version = "dev"

// MCPPath is where streamable HTTP is served; HealthPath reports liveness.
const (
	MCPPath    = "/mcp"
	HealthPath = "/health"
)

// New builds an MCP server with every tool registered.
func New(ts []tools.Tool, log *slog.Logger) *mcp.Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := mcp.NewServer(&mcp.Implementation{Name: Name, Version: Version}, &mcp.ServerOptions{Logger: log})
	tools.Register(s, ts, log)
	return s
}

// RunStdio serves MCP on stdin/stdout until the client disconnects or ctx ends.
func RunStdio(ctx context.Context, s *mcp.Server) error {
	return s.Run(ctx, &mcp.StdioTransport{})
}

// Auth protects the MCP endpoint. Register adds any endpoints it needs
// (OAuth metadata, authorization, token); Protect wraps the MCP handler.
type Auth interface {
	Register(mux *http.ServeMux)
	Protect(h http.Handler) http.Handler
}

// Handler returns the HTTP handler: streamable MCP at /mcp and a health check
// at /health that answers {"status":"UP"}. With a non-nil auth, /mcp
// requires a bearer token and the auth endpoints are added.
func Handler(s *mcp.Server, log *slog.Logger, auth Auth) http.Handler {
	mux := http.NewServeMux()
	opts := &mcp.StreamableHTTPOptions{Logger: log}
	var mcpHandler http.Handler
	if auth != nil {
		// DNS rebinding protection rejects requests that reach a loopback
		// address with another Host header, which is what a reverse proxy on
		// the same machine sends. With authentication in place a rebinding
		// page has no token to send, so the protection is not needed.
		opts.DisableLocalhostProtection = true
		auth.Register(mux)
		mcpHandler = auth.Protect(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, opts))
	} else {
		mcpHandler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, opts)
	}
	mux.Handle(MCPPath, mcpHandler)
	mux.HandleFunc(HealthPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"UP"}`))
	})
	return mux
}

// RunHTTP serves Handler on port until ctx ends, then shuts down gracefully.
func RunHTTP(ctx context.Context, s *mcp.Server, port int, log *slog.Logger, auth Auth) error {
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           Handler(s, log, auth),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info(fmt.Sprintf("MCP transport: HTTP streamable endpoint available at http://localhost:%d%s", port, MCPPath))

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
