package oauth

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// pendingAuth is an authorization request waiting for the user to sign in
// with Discord and approve it.
type pendingAuth struct {
	ID            string
	Session       string // value of the browser cookie that started the flow
	ConsentToken  string // CSRF token for the consent form
	ClientID      string
	ClientName    string
	RedirectURI   string
	RedirectGiven bool
	State         string
	Challenge     string
	Scope         string
	User          *User // set after Discord sign-in
	ExpiresAt     time.Time
}

// authCode is an issued authorization code, stored under its hash. Used
// codes are kept until they expire to detect replays.
type authCode struct {
	ClientID      string
	RedirectURI   string
	RedirectGiven bool
	Challenge     string
	Scope         string
	User          User
	Family        string
	ExpiresAt     time.Time
	Used          bool
}

var errCodeReused = errors.New("authorization code reused")

const cookiePrefix = "dmcp_auth_"

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, http.StatusBadRequest, "Malformed authorization request.")
		return
	}
	q := r.Form
	c := s.store.client(q.Get("client_id"))
	if c == nil {
		s.errorPage(w, http.StatusBadRequest, "Unknown client. Register the client again and retry.")
		return
	}
	redirect := q.Get("redirect_uri")
	redirectGiven := redirect != ""
	if !redirectGiven {
		if len(c.RedirectURIs) != 1 {
			s.errorPage(w, http.StatusBadRequest, "redirect_uri is required.")
			return
		}
		redirect = c.RedirectURIs[0]
	}
	matched := false
	for _, reg := range c.RedirectURIs {
		if redirectMatches(reg, redirect) {
			matched = true
			break
		}
	}
	if !matched {
		s.errorPage(w, http.StatusBadRequest, "redirect_uri is not registered for this client.")
		return
	}

	// From here on errors are reported to the client's redirect URI.
	state := q.Get("state")
	fail := func(code, desc string) { s.redirectError(w, r, redirect, state, code, desc) }
	if q.Get("response_type") != "code" {
		fail("unsupported_response_type", "only response_type=code is supported")
		return
	}
	challenge := q.Get("code_challenge")
	if challenge == "" || q.Get("code_challenge_method") != "S256" {
		fail("invalid_request", "PKCE with code_challenge_method=S256 is required")
		return
	}
	if res := q.Get("resource"); res != "" && !s.isResource(res) {
		fail("invalid_target", "unknown resource")
		return
	}
	// The requested scope is ignored: clients ask for different things (for
	// example offline_access), and the grant is always Scope, which the
	// token response reports.

	p := &pendingAuth{
		ID:            randomToken(),
		Session:       randomToken(),
		ConsentToken:  randomToken(),
		ClientID:      c.ID,
		ClientName:    c.Name,
		RedirectURI:   redirect,
		RedirectGiven: redirectGiven,
		State:         state,
		Challenge:     challenge,
		Scope:         Scope,
		ExpiresAt:     s.cfg.Now().Add(pendingTTL),
	}
	if !s.addPending(p) {
		fail("temporarily_unavailable", "too many sign-ins in progress")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookiePrefix + p.ID[:16],
		Value:    p.Session,
		Path:     "/oauth/",
		MaxAge:   int(pendingTTL.Seconds()),
		HttpOnly: true,
		Secure:   strings.HasPrefix(s.issuer, "https://"),
		SameSite: http.SameSiteLaxMode, // sent on the top-level redirect back from Discord
	})
	v := url.Values{
		"client_id":     {s.cfg.DiscordClientID},
		"response_type": {"code"},
		"redirect_uri":  {s.discordRedirectURI()},
		"scope":         {"identify"},
		"state":         {p.ID},
	}
	http.Redirect(w, r, s.cfg.DiscordAuthorizeURL+"?"+v.Encode(), http.StatusFound)
}

func (s *Server) discordRedirectURI() string { return s.issuer + "/oauth/discord/callback" }

// handleDiscordCallback finishes the Discord sign-in and shows the consent
// page.
func (s *Server) handleDiscordCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	p := s.pendingFor(r, q.Get("state"))
	if p == nil {
		s.errorPage(w, http.StatusBadRequest, "This sign-in link has expired or was opened in a different browser. Start again from your MCP client.")
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		s.dropPending(p.ID)
		s.redirectError(w, r, p.RedirectURI, p.State, "access_denied", "Discord sign-in was cancelled")
		return
	}
	user, err := s.discordUser(r, q.Get("code"))
	if err != nil {
		s.log.Warn("Discord sign-in failed", "error", err)
		s.dropPending(p.ID)
		s.errorPage(w, http.StatusBadGateway, "Signing in with Discord failed. Try again.")
		return
	}
	if !s.cfg.Allowed(user.ID) {
		s.log.Warn("Discord user not allowed", "user_id", user.ID, "username", user.Username)
		s.dropPending(p.ID)
		s.redirectError(w, r, p.RedirectURI, p.State, "access_denied",
			"this Discord account is not allowed to use this server")
		return
	}
	s.mu.Lock()
	p.User = user
	s.mu.Unlock()
	s.consentPage(w, p)
}

// handleConsent processes the Allow/Deny form.
func (s *Server) handleConsent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, http.StatusBadRequest, "Malformed request.")
		return
	}
	p := s.pendingFor(r, r.PostForm.Get("request"))
	if p == nil || p.User == nil ||
		subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(p.ConsentToken)) != 1 {
		s.errorPage(w, http.StatusBadRequest, "This sign-in link has expired or was opened in a different browser. Start again from your MCP client.")
		return
	}
	s.dropPending(p.ID)
	http.SetCookie(w, &http.Cookie{Name: cookiePrefix + p.ID[:16], Path: "/oauth/", MaxAge: -1})
	if r.PostForm.Get("action") != "allow" {
		s.redirectError(w, r, p.RedirectURI, p.State, "access_denied", "the user denied access")
		return
	}
	code := randomToken()
	s.mu.Lock()
	s.pruneLocked()
	s.codes[hashToken(code)] = &authCode{
		ClientID:      p.ClientID,
		RedirectURI:   p.RedirectURI,
		RedirectGiven: p.RedirectGiven,
		Challenge:     p.Challenge,
		Scope:         p.Scope,
		User:          *p.User,
		Family:        randomToken(),
		ExpiresAt:     s.cfg.Now().Add(codeTTL),
	}
	s.mu.Unlock()
	s.log.Info("authorization granted", "client_id", p.ClientID, "user_id", p.User.ID, "username", p.User.Username)
	v := url.Values{"code": {code}, "iss": {s.issuer}}
	if p.State != "" {
		v.Set("state", p.State)
	}
	http.Redirect(w, r, appendQuery(p.RedirectURI, v), http.StatusFound)
}

// takeCode consumes an authorization code. A code presented twice revokes
// the tokens issued from it and returns errCodeReused.
func (s *Server) takeCode(code string) (*authCode, error) {
	if code == "" {
		return nil, nil
	}
	s.mu.Lock()
	c, ok := s.codes[hashToken(code)]
	if !ok || !s.cfg.Now().Before(c.ExpiresAt) {
		s.mu.Unlock()
		return nil, nil
	}
	if c.Used {
		family := c.Family
		s.mu.Unlock()
		// The store has its own lock; s.mu is released first.
		_ = s.store.revokeFamily(family)
		return nil, errCodeReused
	}
	c.Used = true
	cp := *c
	s.mu.Unlock()
	return &cp, nil
}

func (s *Server) addPending(p *pendingAuth) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if len(s.pending) >= maxPending {
		return false
	}
	s.pending[p.ID] = p
	return true
}

// pendingFor returns the pending request id when it has not expired and the
// browser presents the cookie that started it.
func (s *Server) pendingFor(r *http.Request, id string) *pendingAuth {
	if len(id) < 16 {
		return nil
	}
	cookie, err := r.Cookie(cookiePrefix + id[:16])
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[id]
	if !ok || !s.cfg.Now().Before(p.ExpiresAt) ||
		subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(p.Session)) != 1 {
		return nil
	}
	return p
}

func (s *Server) dropPending(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, id)
}

// pruneLocked drops expired pending requests and codes. The caller holds s.mu.
func (s *Server) pruneLocked() {
	now := s.cfg.Now()
	for k, p := range s.pending {
		if !now.Before(p.ExpiresAt) {
			delete(s.pending, k)
		}
	}
	for k, c := range s.codes {
		if !now.Before(c.ExpiresAt) {
			delete(s.codes, k)
		}
	}
}

// discordUser exchanges a Discord authorization code and returns the
// signed-in user.
func (s *Server) discordUser(r *http.Request, code string) (*User, error) {
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {s.discordRedirectURI()},
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.cfg.DiscordAPIBase+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(s.cfg.DiscordClientID), url.QueryEscape(s.cfg.DiscordClientSecret))
	var tok struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := s.doJSON(req, &tok); err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, errors.New("token exchange: no access token")
	}

	req, err = http.NewRequestWithContext(r.Context(), http.MethodGet, s.cfg.DiscordAPIBase+"/users/@me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	var me struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := s.doJSON(req, &me); err != nil {
		return nil, fmt.Errorf("fetch user: %w", err)
	}
	if me.ID == "" {
		return nil, errors.New("fetch user: no user ID")
	}
	return &User{ID: me.ID, Username: me.Username}, nil
}

func (s *Server) doJSON(req *http.Request, v any) error {
	req.Header.Set("Accept", "application/json")
	resp, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, v)
}

// redirectError sends an OAuth error to the client's redirect URI.
func (s *Server) redirectError(w http.ResponseWriter, r *http.Request, redirect, state, code, desc string) {
	v := url.Values{"error": {code}, "iss": {s.issuer}}
	if desc != "" {
		v.Set("error_description", desc)
	}
	if state != "" {
		v.Set("state", state)
	}
	http.Redirect(w, r, appendQuery(redirect, v), http.StatusFound)
}

func appendQuery(raw string, v url.Values) string {
	sep := "?"
	if strings.Contains(raw, "?") {
		sep = "&"
	}
	return raw + sep + v.Encode()
}

// pageHeaders stops the sign-in pages from being framed or cached.
func pageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body{font-family:system-ui,sans-serif;background:#f4f5f7;color:#1f2328;margin:0;display:flex;min-height:100vh;align-items:center;justify-content:center}
main{background:#fff;border-radius:12px;box-shadow:0 2px 12px rgba(0,0,0,.08);padding:28px;max-width:420px;width:calc(100% - 32px)}
h1{font-size:1.25rem;margin:0 0 12px}p{line-height:1.5}.muted{color:#59636e;font-size:.9rem}code{word-break:break-all}
.row{display:flex;gap:12px;margin-top:20px}button{flex:1;padding:10px;border-radius:8px;border:1px solid #d0d7de;font-size:1rem;cursor:pointer;background:#fff}
button.primary{background:#5865f2;border-color:#5865f2;color:#fff}
@media (prefers-color-scheme:dark){body{background:#0d1117;color:#e6edf3}main{background:#161b22}.muted{color:#9198a1}button{background:#21262d;color:#e6edf3;border-color:#30363d}}
</style></head><body><main>
<h1>{{.Title}}</h1>
{{if .Consent}}
<p><strong>{{if .ClientName}}{{.ClientName}}{{else}}An unnamed MCP client{{end}}</strong> wants to manage Discord through this server as <strong>{{.User}}</strong>.</p>
<p class="muted">After you allow it, you will be sent to <code>{{.RedirectHost}}</code>. Only allow clients you started yourself.</p>
<form method="post" action="/oauth/consent">
<input type="hidden" name="request" value="{{.Request}}"><input type="hidden" name="csrf" value="{{.CSRF}}">
<div class="row"><button type="submit" name="action" value="deny">Deny</button><button class="primary" type="submit" name="action" value="allow">Allow</button></div>
</form>
{{else}}<p>{{.Message}}</p>{{end}}
</main></body></html>`))

func (s *Server) consentPage(w http.ResponseWriter, p *pendingAuth) {
	host := p.RedirectURI
	if u, err := url.Parse(p.RedirectURI); err == nil && u.Host != "" {
		host = u.Scheme + "://" + u.Host
	}
	pageHeaders(w)
	_ = pageTmpl.Execute(w, map[string]any{
		"Title":        "Allow access to Discord?",
		"Consent":      true,
		"ClientName":   p.ClientName,
		"User":         p.User.Username,
		"RedirectHost": host,
		"Request":      p.ID,
		"CSRF":         p.ConsentToken,
	})
}

func (s *Server) errorPage(w http.ResponseWriter, status int, msg string) {
	pageHeaders(w)
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, map[string]any{"Title": "Sign-in failed", "Message": msg})
}
