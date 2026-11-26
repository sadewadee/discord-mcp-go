// Package oauth protects the MCP HTTP endpoint with OAuth 2.1.
//
// The server is its own authorization server, as the MCP authorization spec
// allows: clients register themselves (RFC 7591), send the user through the
// authorization code flow with PKCE, and receive access and refresh tokens.
// Users authenticate by signing in with Discord; only allowed Discord users
// get tokens, and the check is repeated on every request.
//
// A fixed bearer token can also be configured for clients that send a
// static Authorization header.
package oauth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// Lifetimes and limits.
const (
	AccessTokenTTL  = time.Hour
	RefreshTokenTTL = 30 * 24 * time.Hour
	codeTTL         = 5 * time.Minute
	pendingTTL      = 10 * time.Minute
	maxClients      = 1000
	maxPending      = 1000
	// Scope is the only scope; it grants use of every tool.
	Scope = "mcp"
)

// User is a signed-in Discord user.
type User struct {
	ID       string
	Username string
}

// Config configures the OAuth server.
type Config struct {
	// PublicURL is the externally reachable base URL, e.g.
	// "https://mcp.example.com". It is the issuer, and PublicURL+"/mcp" is
	// the protected resource.
	PublicURL string
	// DiscordClientID and DiscordClientSecret identify the Discord
	// application used for "Sign in with Discord". Its redirect URI must be
	// PublicURL+"/oauth/discord/callback".
	DiscordClientID     string
	DiscordClientSecret string
	// StaticToken, when set, is accepted as a bearer token as well.
	StaticToken string
	// Allowed reports whether a Discord user may use the server. It is
	// called at sign-in and on every request.
	Allowed func(userID string) bool
	// DataFile stores registered clients and tokens. Empty keeps them in
	// memory only.
	DataFile string
	Logger   *slog.Logger

	// For tests: Discord endpoints, HTTP client and clock.
	DiscordAuthorizeURL string // default https://discord.com/oauth2/authorize
	DiscordAPIBase      string // default https://discord.com/api/v10
	HTTPClient          *http.Client
	Now                 func() time.Time
}

// Server implements the authorization server endpoints and the bearer token
// check for the MCP endpoint.
type Server struct {
	cfg      Config
	issuer   string
	resource string
	store    *store
	log      *slog.Logger

	mu      sync.Mutex
	pending map[string]*pendingAuth // by ID
	codes   map[string]*authCode    // by code hash
}

// New validates cfg and opens the data file.
func New(cfg Config) (*Server, error) {
	if cfg.PublicURL == "" {
		return nil, errors.New("oauth: PublicURL is required")
	}
	if cfg.DiscordClientID == "" || cfg.DiscordClientSecret == "" {
		return nil, errors.New("oauth: Discord client ID and secret are required")
	}
	if cfg.Allowed == nil {
		return nil, errors.New("oauth: Allowed is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.DiscordAuthorizeURL == "" {
		cfg.DiscordAuthorizeURL = "https://discord.com/oauth2/authorize"
	}
	if cfg.DiscordAPIBase == "" {
		cfg.DiscordAPIBase = "https://discord.com/api/v10"
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	issuer := strings.TrimRight(cfg.PublicURL, "/")
	st, err := openStore(cfg.DataFile, cfg.Now)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg:      cfg,
		issuer:   issuer,
		resource: issuer + "/mcp",
		store:    st,
		log:      cfg.Logger,
		pending:  map[string]*pendingAuth{},
		codes:    map[string]*authCode{},
	}, nil
}

// Resource is the protected resource identifier (the MCP endpoint URL).
func (s *Server) Resource() string { return s.resource }

// Register adds the OAuth endpoints to mux.
func (s *Server) Register(mux *http.ServeMux) {
	prm := auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               s.resource,
		AuthorizationServers:   []string{s.issuer},
		ScopesSupported:        []string{Scope},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "discord-mcp",
	})
	mux.Handle("/.well-known/oauth-protected-resource", prm)
	mux.Handle("/.well-known/oauth-protected-resource/mcp", prm)
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.handleAuthServerMetadata)
	mux.HandleFunc("/oauth/register", s.handleRegister)
	mux.HandleFunc("/oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("/oauth/discord/callback", s.handleDiscordCallback)
	mux.HandleFunc("/oauth/consent", s.handleConsent)
	mux.HandleFunc("/oauth/token", s.handleToken)
	mux.HandleFunc("/oauth/revoke", s.handleRevoke)
}

// Protect wraps the MCP handler so that it requires a valid bearer token.
// A missing or invalid token gets 401 with a WWW-Authenticate header that
// points to the protected resource metadata.
func (s *Server) Protect(h http.Handler) http.Handler {
	return auth.RequireBearerToken(s.verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL:    s.issuer + "/.well-known/oauth-protected-resource/mcp",
		AllowMissingExpiration: true, // the static token does not expire
	})(h)
}

// StaticTokenOnly protects h with a fixed bearer token, without OAuth.
func StaticTokenOnly(token string, h http.Handler) http.Handler {
	verify := func(_ context.Context, got string, _ *http.Request) (*auth.TokenInfo, error) {
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1 {
			return &auth.TokenInfo{Scopes: []string{Scope}, UserID: "static-token"}, nil
		}
		return nil, auth.ErrInvalidToken
	}
	return auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(h)
}

func (s *Server) verify(_ context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
	if s.cfg.StaticToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.cfg.StaticToken)) == 1 {
		return &auth.TokenInfo{Scopes: []string{Scope}, UserID: "static-token"}, nil
	}
	t := s.store.lookup(token)
	if t == nil || t.Kind != kindAccess || t.Resource != s.resource {
		return nil, auth.ErrInvalidToken
	}
	if !s.cfg.Allowed(t.UserID) {
		return nil, fmt.Errorf("%w: Discord user %s is no longer allowed", auth.ErrInvalidToken, t.UserID)
	}
	return &auth.TokenInfo{
		Scopes:     strings.Fields(t.Scope),
		Expiration: t.ExpiresAt,
		UserID:     t.UserID,
		Extra:      map[string]any{"username": t.UserName, "client_id": t.ClientID},
	}, nil
}

func (s *Server) handleAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	if cors(w, r) {
		return
	}
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         s.issuer,
		"authorization_endpoint":                         s.issuer + "/oauth/authorize",
		"token_endpoint":                                 s.issuer + "/oauth/token",
		"registration_endpoint":                          s.issuer + "/oauth/register",
		"revocation_endpoint":                            s.issuer + "/oauth/revoke",
		"scopes_supported":                               []string{Scope},
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_basic", "client_secret_post"},
		"revocation_endpoint_auth_methods_supported":     []string{"none", "client_secret_basic", "client_secret_post"},
		"code_challenge_methods_supported":               []string{"S256"},
		"authorization_response_iss_parameter_supported": true,
	})
}

// cors sets permissive CORS headers for the metadata, registration, token
// and revocation endpoints, which browser-based MCP clients call directly.
// They carry no cookies. It reports whether the request was a preflight
// that has been answered.
func cors(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// oauthError writes an RFC 6749 error response.
func oauthError(w http.ResponseWriter, status int, code, description string) {
	body := map[string]string{"error": code}
	if description != "" {
		body["error_description"] = description
	}
	writeJSON(w, status, body)
}
