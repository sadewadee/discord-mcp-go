package oauth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testRedirect = "http://127.0.0.1:33418/callback"
	verifier     = "dBjftJeZ4CVP-mJ92K5vT3mfyoWPeNxsoVnHMlGQmSTvN0e-2aBc"
)

func challengeFor(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// fakeDiscord serves the two Discord endpoints used at sign-in. The code
// "code-<id>" signs in user <id>.
func fakeDiscord(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok || id != "discord-app" || secret != "discord-secret" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		_ = r.ParseForm()
		code := r.PostForm.Get("code")
		if !strings.HasPrefix(code, "code-") || !strings.HasSuffix(r.PostForm.Get("redirect_uri"), "/oauth/discord/callback") {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "dtok-" + strings.TrimPrefix(code, "code-"), "token_type": "Bearer"})
	})
	mux.HandleFunc("/users/@me", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer dtok-")
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "username": "user" + id})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type harness struct {
	t       *testing.T
	srv     *httptest.Server
	oauth   *Server
	client  *http.Client // cookie jar, does not follow redirects
	mu      sync.Mutex
	allowed map[string]bool
	clock   time.Time
}

func (h *harness) now() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.clock
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clock = h.clock.Add(d)
}

func newHarness(t *testing.T, dataFile, staticToken string) *harness {
	t.Helper()
	discord := fakeDiscord(t)
	h := &harness{t: t, allowed: map[string]bool{"111": true}, clock: time.Now()}
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	o, err := New(Config{
		PublicURL:           srv.URL,
		DiscordClientID:     "discord-app",
		DiscordClientSecret: "discord-secret",
		StaticToken:         staticToken,
		Allowed: func(id string) bool {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.allowed[id]
		},
		DataFile:            dataFile,
		DiscordAuthorizeURL: "https://discord.invalid/oauth2/authorize",
		DiscordAPIBase:      discord.URL,
		Now:                 h.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	o.Register(mux)
	mux.Handle("/mcp", o.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "mcp ok")
	})))
	jar, _ := cookiejar.New(nil)
	h.srv, h.oauth = srv, o
	h.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return h
}

func (h *harness) setAllowed(id string, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.allowed[id] = ok
}

func (h *harness) register(body string) (int, map[string]any) {
	h.t.Helper()
	resp, err := h.client.Post(h.srv.URL+"/oauth/register", "application/json", strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func (h *harness) publicClient() string {
	h.t.Helper()
	code, m := h.register(`{"redirect_uris":["` + testRedirect + `"],"token_endpoint_auth_method":"none","client_name":"Test <Client>"}`)
	if code != http.StatusCreated {
		h.t.Fatalf("register: %d %v", code, m)
	}
	return m["client_id"].(string)
}

func (h *harness) authorizeURL(clientID string, extra url.Values) string {
	v := url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {testRedirect},
		"code_challenge":        {challengeFor(verifier)},
		"code_challenge_method": {"S256"},
		"state":                 {"xyz"},
		"resource":              {h.srv.URL + "/mcp"},
	}
	for k, vs := range extra {
		v[k] = vs
	}
	return h.srv.URL + "/oauth/authorize?" + v.Encode()
}

func (h *harness) get(u string) *http.Response {
	h.t.Helper()
	resp, err := h.client.Get(u)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func body(resp *http.Response) string {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

var hiddenRe = regexp.MustCompile(`name="(request|csrf)" value="([^"]+)"`)

// signIn runs authorize -> Discord callback for userID and returns the
// consent form fields and page.
func (h *harness) signIn(clientID, userID string) (url.Values, string) {
	h.t.Helper()
	resp := h.get(h.authorizeURL(clientID, nil))
	if resp.StatusCode != http.StatusFound {
		h.t.Fatalf("authorize: %d %s", resp.StatusCode, body(resp))
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	resp.Body.Close()
	if loc.Host != "discord.invalid" || loc.Query().Get("scope") != "identify" || loc.Query().Get("client_id") != "discord-app" {
		h.t.Fatalf("unexpected Discord redirect %s", loc)
	}
	state := loc.Query().Get("state")
	resp = h.get(h.srv.URL + "/oauth/discord/callback?" + url.Values{"code": {"code-" + userID}, "state": {state}}.Encode())
	page := body(resp)
	form := url.Values{}
	for _, m := range hiddenRe.FindAllStringSubmatch(page, -1) {
		form.Set(m[1], m[2])
	}
	return form, page
}

// approve submits the consent form and returns the client redirect.
func (h *harness) consent(form url.Values, action string) *url.URL {
	h.t.Helper()
	form.Set("action", action)
	resp, err := h.client.PostForm(h.srv.URL+"/oauth/consent", form)
	if err != nil {
		h.t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		h.t.Fatalf("consent: %d", resp.StatusCode)
	}
	u, _ := url.Parse(resp.Header.Get("Location"))
	return u
}

func (h *harness) token(form url.Values, basicID, basicSecret string) (int, map[string]any) {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if basicID != "" {
		req.SetBasicAuth(basicID, basicSecret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return resp.StatusCode, m
}

func (h *harness) codeExchange(clientID, code string) (int, map[string]any) {
	return h.token(url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"code":          {code},
		"redirect_uri":  {testRedirect},
		"code_verifier": {verifier},
		"resource":      {h.srv.URL + "/mcp"},
	}, "", "")
}

func (h *harness) callMCP(token string) *http.Response {
	h.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/mcp", strings.NewReader("{}"))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

// fullFlow signs user 111 in and returns the token response.
func (h *harness) fullFlow(clientID string) map[string]any {
	h.t.Helper()
	form, page := h.signIn(clientID, "111")
	if form.Get("request") == "" || form.Get("csrf") == "" {
		h.t.Fatalf("no consent form in page:\n%s", page)
	}
	redirect := h.consent(form, "allow")
	q := redirect.Query()
	if q.Get("state") != "xyz" || q.Get("iss") != h.srv.URL || q.Get("code") == "" {
		h.t.Fatalf("bad redirect %s", redirect)
	}
	if got := redirect.Scheme + "://" + redirect.Host + redirect.Path; got != testRedirect {
		h.t.Fatalf("redirected to %s", got)
	}
	status, tok := h.codeExchange(clientID, q.Get("code"))
	if status != http.StatusOK {
		h.t.Fatalf("token: %d %v", status, tok)
	}
	return tok
}

func TestFullFlow(t *testing.T) {
	h := newHarness(t, "", "")
	clientID := h.publicClient()
	tok := h.fullFlow(clientID)
	if tok["token_type"] != "Bearer" || tok["scope"] != Scope || tok["expires_in"].(float64) != AccessTokenTTL.Seconds() {
		t.Errorf("token response: %v", tok)
	}
	resp := h.callMCP(tok["access_token"].(string))
	if resp.StatusCode != http.StatusOK || body(resp) != "mcp ok" {
		t.Fatalf("mcp with token: %d", resp.StatusCode)
	}
}

func TestConsentPageEscapesClientName(t *testing.T) {
	h := newHarness(t, "", "")
	_, page := h.signIn(h.publicClient(), "111")
	if !strings.Contains(page, "Test &lt;Client&gt;") || strings.Contains(page, "Test <Client>") {
		t.Errorf("client name not escaped:\n%s", page)
	}
	if !strings.Contains(page, "user111") || !strings.Contains(page, "http://127.0.0.1:33418") {
		t.Errorf("consent page lacks user or redirect host:\n%s", page)
	}
}

func TestMCPRequiresToken(t *testing.T) {
	h := newHarness(t, "", "")
	resp := h.callMCP("")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
	want := `resource_metadata="` + h.srv.URL + `/.well-known/oauth-protected-resource/mcp"`
	if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, want) {
		t.Errorf("WWW-Authenticate = %q", got)
	}
	resp = h.callMCP("not-a-token")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad token status %d", resp.StatusCode)
	}
}

func TestStaticToken(t *testing.T) {
	h := newHarness(t, "", "static-secret-value")
	if resp := h.callMCP("static-secret-value"); resp.StatusCode != http.StatusOK {
		t.Errorf("static token rejected: %d", resp.StatusCode)
	}
	if resp := h.callMCP("static-secret-valuX"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong static token accepted: %d", resp.StatusCode)
	}

	only := httptest.NewServer(StaticTokenOnly("abc", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})))
	defer only.Close()
	req, _ := http.NewRequest(http.MethodGet, only.URL, nil)
	req.Header.Set("Authorization", "Bearer abc")
	if resp, _ := http.DefaultClient.Do(req); resp.StatusCode != http.StatusOK {
		t.Errorf("StaticTokenOnly rejected the token: %d", resp.StatusCode)
	}
	if resp, _ := http.Get(only.URL); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("StaticTokenOnly allowed no token: %d", resp.StatusCode)
	}
}

func TestMetadata(t *testing.T) {
	h := newHarness(t, "", "")
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		var prm map[string]any
		_ = json.NewDecoder(h.get(h.srv.URL + path).Body).Decode(&prm)
		if prm["resource"] != h.srv.URL+"/mcp" || prm["authorization_servers"].([]any)[0] != h.srv.URL {
			t.Errorf("%s: %v", path, prm)
		}
	}
	var as map[string]any
	_ = json.NewDecoder(h.get(h.srv.URL + "/.well-known/oauth-authorization-server").Body).Decode(&as)
	if as["issuer"] != h.srv.URL || as["registration_endpoint"] != h.srv.URL+"/oauth/register" ||
		as["code_challenge_methods_supported"].([]any)[0] != "S256" {
		t.Errorf("AS metadata: %v", as)
	}
	if _, ok := as["jwks_uri"]; ok {
		t.Error("jwks_uri must be omitted")
	}
}

func TestRefreshRotationAndReuse(t *testing.T) {
	h := newHarness(t, "", "")
	clientID := h.publicClient()
	tok := h.fullFlow(clientID)
	refresh1 := tok["refresh_token"].(string)

	status, tok2 := h.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refresh1}}, "", "")
	if status != http.StatusOK || tok2["refresh_token"] == refresh1 {
		t.Fatalf("refresh: %d %v", status, tok2)
	}
	if resp := h.callMCP(tok2["access_token"].(string)); resp.StatusCode != http.StatusOK {
		t.Fatalf("new access token rejected: %d", resp.StatusCode)
	}
	// Within the grace period a second use is refused but revokes nothing
	// (a client retrying after a network error).
	status, m := h.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refresh1}}, "", "")
	if status != http.StatusBadRequest || m["error"] != "invalid_grant" {
		t.Fatalf("retry: %d %v", status, m)
	}
	if resp := h.callMCP(tok2["access_token"].(string)); resp.StatusCode != http.StatusOK {
		t.Fatalf("retry within grace revoked the family: %d", resp.StatusCode)
	}
	// Later, replaying the old refresh token revokes the whole family.
	h.advance(refreshGrace + time.Second)
	status, m = h.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {refresh1}}, "", "")
	if status != http.StatusBadRequest || m["error"] != "invalid_grant" {
		t.Fatalf("reuse: %d %v", status, m)
	}
	if resp := h.callMCP(tok2["access_token"].(string)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("family not revoked: %d", resp.StatusCode)
	}
	status, _ = h.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {tok2["refresh_token"].(string)}}, "", "")
	if status != http.StatusBadRequest {
		t.Errorf("rotated refresh token still valid: %d", status)
	}
}

func TestCodeReuseRevokes(t *testing.T) {
	h := newHarness(t, "", "")
	clientID := h.publicClient()
	form, _ := h.signIn(clientID, "111")
	code := h.consent(form, "allow").Query().Get("code")
	status, tok := h.codeExchange(clientID, code)
	if status != http.StatusOK {
		t.Fatal(status)
	}
	status, m := h.codeExchange(clientID, code)
	if status != http.StatusBadRequest || m["error"] != "invalid_grant" {
		t.Fatalf("reuse: %d %v", status, m)
	}
	if resp := h.callMCP(tok["access_token"].(string)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("tokens from a replayed code still work: %d", resp.StatusCode)
	}
}

func TestPKCEAndRedirectChecks(t *testing.T) {
	h := newHarness(t, "", "")
	clientID := h.publicClient()
	form, _ := h.signIn(clientID, "111")
	code := h.consent(form, "allow").Query().Get("code")
	status, m := h.token(url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "code": {code},
		"redirect_uri": {testRedirect}, "code_verifier": {strings.Repeat("a", 43)},
	}, "", "")
	if status != http.StatusBadRequest || m["error"] != "invalid_grant" {
		t.Errorf("wrong verifier: %d %v", status, m)
	}

	// Invalid authorization requests get an error page, never a redirect
	// (which would make the server an open redirector).
	for name, extra := range map[string]url.Values{
		"plain PKCE":        {"code_challenge_method": {"plain"}},
		"no PKCE":           {"code_challenge": {""}},
		"bad response_type": {"response_type": {"token"}},
		"foreign resource":  {"resource": {"https://other.example/mcp"}},
		"huge state":        {"state": {strings.Repeat("s", maxStateLength+1)}},
	} {
		resp := h.get(h.authorizeURL(clientID, extra))
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
			t.Errorf("%s: %d, Location %q", name, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	// An unregistered redirect URI is never redirected to.
	resp := h.get(h.authorizeURL(clientID, url.Values{"redirect_uri": {"https://evil.example/cb"}}))
	if resp.StatusCode != http.StatusBadRequest || resp.Header.Get("Location") != "" {
		t.Errorf("unregistered redirect: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp.Body.Close()
}

func TestLoopbackRedirectPortMayDiffer(t *testing.T) {
	h := newHarness(t, "", "")
	clientID := h.publicClient()
	resp := h.get(h.authorizeURL(clientID, url.Values{"redirect_uri": {"http://127.0.0.1:50123/callback"}}))
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || !strings.Contains(resp.Header.Get("Location"), "discord.invalid") {
		t.Errorf("loopback port change refused: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestUserNotAllowed(t *testing.T) {
	h := newHarness(t, "", "")
	clientID := h.publicClient()
	resp := h.get(h.authorizeURL(clientID, nil))
	loc, _ := url.Parse(resp.Header.Get("Location"))
	resp.Body.Close()
	resp = h.get(h.srv.URL + "/oauth/discord/callback?" + url.Values{"code": {"code-999"}, "state": {loc.Query().Get("state")}}.Encode())
	resp.Body.Close()
	back, _ := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || back.Query().Get("error") != "access_denied" || back.Query().Get("state") != "xyz" {
		t.Errorf("not-allowed user: %d %s", resp.StatusCode, back)
	}
}

func TestAccessRevokedWhenUserRemoved(t *testing.T) {
	h := newHarness(t, "", "")
	clientID := h.publicClient()
	tok := h.fullFlow(clientID)
	h.setAllowed("111", false)
	if resp := h.callMCP(tok["access_token"].(string)); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("removed user still has access: %d", resp.StatusCode)
	}
	status, _ := h.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {tok["refresh_token"].(string)}}, "", "")
	if status != http.StatusBadRequest {
		t.Errorf("removed user can refresh: %d", status)
	}
}

func TestDenyConsent(t *testing.T) {
	h := newHarness(t, "", "")
	form, _ := h.signIn(h.publicClient(), "111")
	q := h.consent(form, "deny").Query()
	if q.Get("error") != "access_denied" || q.Get("code") != "" {
		t.Errorf("deny: %v", q)
	}
}

func TestCallbackNeedsSameBrowser(t *testing.T) {
	h := newHarness(t, "", "")
	clientID := h.publicClient()
	resp := h.get(h.authorizeURL(clientID, nil))
	loc, _ := url.Parse(resp.Header.Get("Location"))
	resp.Body.Close()
	// A different browser (no cookie) opens the callback link.
	other := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := other.Get(h.srv.URL + "/oauth/discord/callback?" + url.Values{"code": {"code-111"}, "state": {loc.Query().Get("state")}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	page := body(resp)
	if resp.StatusCode != http.StatusBadRequest || strings.Contains(page, `name="csrf"`) {
		t.Errorf("callback without cookie: %d", resp.StatusCode)
	}
}

func TestConsentNeedsCSRFToken(t *testing.T) {
	h := newHarness(t, "", "")
	form, _ := h.signIn(h.publicClient(), "111")
	form.Set("csrf", "forged")
	form.Set("action", "allow")
	resp, err := h.client.PostForm(h.srv.URL+"/oauth/consent", form)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("forged CSRF token accepted: %d", resp.StatusCode)
	}
}

func TestConfidentialClient(t *testing.T) {
	h := newHarness(t, "", "")
	status, reg := h.register(`{"redirect_uris":["https://app.example/cb"],"client_name":"Web"}`)
	if status != http.StatusCreated || reg["token_endpoint_auth_method"] != "client_secret_basic" || reg["client_secret"] == nil {
		t.Fatalf("register: %d %v", status, reg)
	}
	id, secret := reg["client_id"].(string), reg["client_secret"].(string)
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"x"}}
	if status, m := h.token(form, id, "wrong"); status != http.StatusUnauthorized || m["error"] != "invalid_client" {
		t.Errorf("wrong secret: %d %v", status, m)
	}
	if status, m := h.token(form, id, secret); status != http.StatusBadRequest || m["error"] != "invalid_grant" {
		t.Errorf("right secret should pass client auth: %d %v", status, m)
	}
	// A confidential client cannot authenticate as public.
	form.Set("client_id", id)
	if status, _ := h.token(form, "", ""); status != http.StatusUnauthorized {
		t.Errorf("missing secret accepted: %d", status)
	}
}

func TestRegisterValidation(t *testing.T) {
	h := newHarness(t, "", "")
	for _, body := range []string{
		`{}`,
		`not json`,
		`{"redirect_uris":["http://evil.example/cb"]}`,
		`{"redirect_uris":["javascript:alert(1)"]}`,
		`{"redirect_uris":["https://a.example/cb#frag"]}`,
		`{"redirect_uris":["relative/path"]}`,
		`{"redirect_uris":["https://a.example/cb"],"token_endpoint_auth_method":"private_key_jwt"}`,
		`{"redirect_uris":["https://a.example/cb"],"grant_types":["client_credentials"]}`,
	} {
		if status, m := h.register(body); status != http.StatusBadRequest || m["error"] == nil {
			t.Errorf("register(%s) = %d %v", body, status, m)
		}
	}
	for _, uri := range []string{"http://localhost:1234/cb", "http://[::1]/cb", "cursor://anysphere.cursor-retrieval/oauth/callback", "https://claude.ai/api/mcp/auth_callback"} {
		if status, m := h.register(`{"redirect_uris":["` + uri + `"],"token_endpoint_auth_method":"none"}`); status != http.StatusCreated {
			t.Errorf("register %s = %d %v", uri, status, m)
		}
	}
}

func TestRedirectMatches(t *testing.T) {
	tests := []struct {
		reg, req string
		want     bool
	}{
		{"https://a.example/cb", "https://a.example/cb", true},
		{"https://a.example/cb", "https://a.example/cb2", false},
		{"https://a.example:443/cb", "https://a.example:8443/cb", false},
		{"http://127.0.0.1:1/cb", "http://127.0.0.1:2/cb", true},
		{"http://localhost/cb", "http://localhost:9/cb", true},
		{"http://127.0.0.1/cb", "http://127.0.0.1/other", false},
		{"http://127.0.0.1/cb", "http://localhost/cb", false},
	}
	for _, tt := range tests {
		if got := redirectMatches(tt.reg, tt.req); got != tt.want {
			t.Errorf("redirectMatches(%q, %q) = %v", tt.reg, tt.req, got)
		}
	}
}

func TestPersistence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "data", "oauth.json")
	h := newHarness(t, file, "")
	clientID := h.publicClient()
	tok := h.fullFlow(clientID)

	// A new server on the same file still knows the client and the tokens.
	h2 := newHarness(t, file, "")
	if h2.oauth.store.client(clientID) == nil {
		t.Fatal("client lost after restart")
	}
	if h2.oauth.store.lookup(tok["access_token"].(string)) == nil {
		t.Fatal("access token lost after restart")
	}
	raw, _ := filepath.Glob(file)
	if len(raw) != 1 {
		t.Fatal("data file missing")
	}
}

func TestVerifyPKCEKnownVector(t *testing.T) {
	// Challenge computed independently with:
	//   printf %s "$verifier" | openssl dgst -sha256 -binary | base64 | tr '+/' '-_' | tr -d '='
	if !verifyPKCE(verifier, "O3DwBzU6BYtXoNRarC71BUPmvX4_6Sggit07VD7aLWA") {
		t.Error("known S256 vector rejected")
	}
}

func TestVerifyPKCERoundTrip(t *testing.T) {
	if !verifyPKCE(verifier, challengeFor(verifier)) {
		t.Error("valid verifier rejected")
	}
	if verifyPKCE("short", challengeFor("short")) {
		t.Error("verifier shorter than 43 characters accepted")
	}
	if verifyPKCE(verifier, challengeFor(verifier+"x")) {
		t.Error("mismatched verifier accepted")
	}
}

func TestConsentPageHasNoFormAction(t *testing.T) {
	h := newHarness(t, "", "")
	clientID := h.publicClient()
	resp := h.get(h.authorizeURL(clientID, nil))
	loc, _ := url.Parse(resp.Header.Get("Location"))
	resp.Body.Close()
	resp = h.get(h.srv.URL + "/oauth/discord/callback?" + url.Values{"code": {"code-111"}, "state": {loc.Query().Get("state")}}.Encode())
	defer resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	// Browsers apply form-action to the redirect after the form, which goes
	// to the client's own origin or scheme.
	if strings.Contains(csp, "form-action") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q", csp)
	}
}

func TestConsentIsSingleUse(t *testing.T) {
	h := newHarness(t, "", "")
	form, _ := h.signIn(h.publicClient(), "111")
	replay := url.Values{}
	for k, v := range form {
		replay[k] = v
	}
	if code := h.consent(form, "allow").Query().Get("code"); code == "" {
		t.Fatal("no code")
	}
	replay.Set("action", "allow")
	resp, err := h.client.PostForm(h.srv.URL+"/oauth/consent", replay)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("second consent answer accepted: %d", resp.StatusCode)
	}
}

func TestTamperedFlowCookieRejected(t *testing.T) {
	h := newHarness(t, "", "")
	clientID := h.publicClient()
	resp := h.get(h.authorizeURL(clientID, nil))
	loc, _ := url.Parse(resp.Header.Get("Location"))
	resp.Body.Close()
	nonce := loc.Query().Get("state")
	u, _ := url.Parse(h.srv.URL)
	for _, c := range h.client.Jar.Cookies(u) {
		if strings.HasPrefix(c.Name, "dmcp_flow_") {
			body, sig, _ := strings.Cut(c.Value, ".")
			c.Value = body + "x." + sig
			h.client.Jar.SetCookies(u, []*http.Cookie{c})
		}
	}
	resp = h.get(h.srv.URL + "/oauth/discord/callback?" + url.Values{"code": {"code-111"}, "state": {nonce}}.Encode())
	page := body(resp)
	if resp.StatusCode != http.StatusBadRequest || strings.Contains(page, `name="csrf"`) {
		t.Errorf("tampered cookie accepted: %d", resp.StatusCode)
	}
}

func TestSignInExpires(t *testing.T) {
	h := newHarness(t, "", "")
	clientID := h.publicClient()
	resp := h.get(h.authorizeURL(clientID, nil))
	loc, _ := url.Parse(resp.Header.Get("Location"))
	resp.Body.Close()
	h.advance(pendingTTL + time.Second)
	resp = h.get(h.srv.URL + "/oauth/discord/callback?" + url.Values{"code": {"code-111"}, "state": {loc.Query().Get("state")}}.Encode())
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expired sign-in accepted: %d", resp.StatusCode)
	}
}

func TestManyAuthorizeRequestsDoNotBlockSignIn(t *testing.T) {
	h := newHarness(t, "", "")
	attacker := h.publicClient()
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for i := 0; i < 1500; i++ {
		resp, err := anon.Get(h.authorizeURL(attacker, nil))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	// Sign-in state lives in the browser, so a real user is unaffected.
	if tok := h.fullFlow(h.publicClient()); tok["access_token"] == nil {
		t.Fatal("sign-in failed after an authorize flood")
	}
}

func TestRegistrationFloodKeepsSignedInClients(t *testing.T) {
	h := newHarness(t, "", "")
	clientID := h.publicClient()
	tok := h.fullFlow(clientID)
	for i := 0; i < maxClients+50; i++ {
		if status, m := h.register(`{"redirect_uris":["https://x.example/cb"],"token_endpoint_auth_method":"none"}`); status != http.StatusCreated {
			t.Fatalf("registration %d: %d %v", i, status, m)
		}
	}
	if resp := h.callMCP(tok["access_token"].(string)); resp.StatusCode != http.StatusOK {
		t.Fatalf("signed-in client evicted by a registration flood: %d", resp.StatusCode)
	}
	if h.oauth.store.client(clientID) == nil {
		t.Fatal("signed-in client removed")
	}
	status, m := h.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {clientID}, "refresh_token": {tok["refresh_token"].(string)}}, "", "")
	if status != http.StatusOK {
		t.Errorf("refresh after flood: %d %v", status, m)
	}
}

func TestStoreFullOfLiveClients(t *testing.T) {
	st, _ := openStore("", time.Now, func(string, ...any) {})
	now := time.Now()
	for i := 0; i < 3; i++ {
		c := &Client{ID: fmt.Sprint("c", i), CreatedAt: now}
		if err := st.addClient(c, 3); err != nil {
			t.Fatal(err)
		}
		if _, err := st.issue(Token{Kind: kindAccess, ClientID: c.ID, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.addClient(&Client{ID: "new", CreatedAt: now}, 3); !errors.Is(err, errStoreFull) {
		t.Errorf("expected errStoreFull, got %v", err)
	}
}

func TestUnusedRegistrationsExpire(t *testing.T) {
	h := newHarness(t, "", "")
	unused := h.publicClient()
	used := h.publicClient()
	h.fullFlow(used)
	h.advance(unusedClientTTL + time.Hour)
	h.publicClient() // triggers pruning
	if h.oauth.store.client(unused) != nil {
		t.Error("unused registration kept")
	}
	if h.oauth.store.client(used) == nil {
		t.Error("used client removed")
	}
}

func TestRegistrationLimitsAndNameCleaning(t *testing.T) {
	h := newHarness(t, "", "")
	for _, uri := range []string{
		"https://" + strings.Repeat("a", maxRedirectURILength) + ".example/cb",
		"https://\u0441laude.ai/cb", // Cyrillic es
		"https://a.example/c b",
	} {
		if status, _ := h.register(`{"redirect_uris":["` + uri + `"],"token_endpoint_auth_method":"none"}`); status != http.StatusBadRequest {
			t.Errorf("redirect %q accepted: %d", uri, status)
		}
	}
	six := `["https://a.example/1","https://a.example/2","https://a.example/3","https://a.example/4","https://a.example/5","https://a.example/6"]`
	if status, _ := h.register(`{"redirect_uris":` + six + `,"token_endpoint_auth_method":"none"}`); status != http.StatusBadRequest {
		t.Errorf("six redirect URIs accepted: %d", status)
	}
	status, m := h.register(`{"redirect_uris":["https://a.example/cb"],"token_endpoint_auth_method":"none","client_name":"Cla\u202eude\u0000 ` + strings.Repeat("x", 100) + `"}`)
	if status != http.StatusCreated {
		t.Fatal(status, m)
	}
	name := m["client_name"].(string)
	if strings.ContainsAny(name, "\u202e\x00") || !strings.HasPrefix(name, "Claude ") || len([]rune(name)) > maxClientNameRunes {
		t.Errorf("client name not cleaned: %q", name)
	}
}

func TestCorruptDataFileRecovers(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "oauth.json")
	if err := os.WriteFile(file, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := openStore(file, time.Now, func(string, ...any) {})
	if err != nil {
		t.Fatalf("damaged file must not stop the server: %v", err)
	}
	if len(st.data.Clients) != 0 {
		t.Error("expected an empty store")
	}
	if _, err := os.Stat(file + ".corrupt"); err != nil {
		t.Error("damaged file not kept aside")
	}
}
