package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/sadewadee/discord-mcp-go/internal/oauth"
)

// TestOAuthWithSDKClient runs the MCP Go SDK's own OAuth client against the
// server: discovery from the 401 challenge, dynamic registration, the
// authorization code flow with PKCE (a scripted browser signs in with a fake
// Discord and clicks Allow), the token exchange, and an authorized tool call.
func TestOAuthWithSDKClient(t *testing.T) {
	discord := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "dtok", "token_type": "Bearer"})
		case "/users/@me":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "111111111111111111", "username": "owner"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer discord.Close()

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	o, err := oauth.New(oauth.Config{
		PublicURL:           srv.URL,
		DiscordClientID:     "123456789012345678",
		DiscordClientSecret: "secret",
		Allowed:             func(id string) bool { return id == "111111111111111111" },
		DiscordAuthorizeURL: "https://discord.invalid/oauth2/authorize",
		DiscordAPIBase:      discord.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux.Handle("/", Handler(New(testTools(), nil), nil, o))

	// The "browser": follows the authorization URL, signs in, clicks Allow.
	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	hidden := regexp.MustCompile(`name="(request|csrf)" value="([^"]+)"`)
	fetcher := func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		resp, err := browser.Get(args.URL)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		toDiscord, _ := url.Parse(resp.Header.Get("Location"))
		resp, err = browser.Get(srv.URL + "/oauth/discord/callback?" + url.Values{
			"code": {"discord-code"}, "state": {toDiscord.Query().Get("state")},
		}.Encode())
		if err != nil {
			return nil, err
		}
		page, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		form := url.Values{"action": {"allow"}}
		for _, m := range hidden.FindAllStringSubmatch(string(page), -1) {
			form.Set(m[1], m[2])
		}
		resp, err = browser.PostForm(srv.URL+"/oauth/consent", form)
		if err != nil {
			return nil, err
		}
		resp.Body.Close()
		back, _ := url.Parse(resp.Header.Get("Location"))
		q := back.Query()
		return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
	}

	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{
			Metadata: &oauthex.ClientRegistrationMetadata{
				RedirectURIs:            []string{"http://127.0.0.1:9/callback"},
				TokenEndpointAuthMethod: "none",
				GrantTypes:              []string{"authorization_code", "refresh_token"},
				ClientName:              "sdk-test",
			},
		},
		AuthorizationCodeFetcher: fetcher,
		RequestRefreshToken:      true,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + MCPPath, OAuthHandler: handler}, nil)
	if err != nil {
		t.Fatalf("connect with OAuth: %v", err)
	}
	defer session.Close()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]any{"text": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || res.Content[0].(*mcp.TextContent).Text != "you said hi" {
		t.Errorf("result = %+v", res)
	}

	// Without a token the endpoint refuses, and /health stays open.
	resp, err := http.Post(srv.URL+MCPPath, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated /mcp = %d", resp.StatusCode)
	}
	resp, err = http.Get(srv.URL + HealthPath)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/health = %d", resp.StatusCode)
	}
}
