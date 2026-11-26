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

// Name and Version identify the server to MCP clients.
const (
	Name    = "discord-mcp-server"
	Version = "1.0.0"
)

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

// Handler returns the HTTP handler: streamable MCP at /mcp and a health check
// at /health that answers {"status":"UP"}.
func Handler(s *mcp.Server, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, &mcp.StreamableHTTPOptions{Logger: log})
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
func RunHTTP(ctx context.Context, s *mcp.Server, port int, log *slog.Logger) error {
	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", port),
		Handler:           Handler(s, log),
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
