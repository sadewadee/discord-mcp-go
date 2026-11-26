package oauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
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

// flow is the state of one sign-in, carried between /oauth/authorize, the
// Discord callback and the consent form in a signed cookie rather than on
// the server, so unauthenticated requests cannot exhaust server memory.
type flow struct {
	Nonce         string `json:"n"` // also the Discord state parameter
	ClientID      string `json:"c"`
	RedirectURI   string `json:"r"`
	RedirectGiven bool   `json:"rg,omitempty"`
	State         string `json:"s,omitempty"`
	Challenge     string `json:"p"`
	Expires       int64  `json:"e"`
	UserID        string `json:"u,omitempty"` // set after Discord sign-in
	Username      string `json:"un,omitempty"`
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

// maxStateLength bounds the client's state parameter, which is stored in
// the flow cookie.
const maxStateLength = 512

func (s *Server) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, http.StatusBadRequest, "Malformed authorization request.")
		return
	}
	q := r.Form
	c := s.store.client(q.Get("client_id"))
	if c == nil {
		s.errorPage(w, http.StatusBadRequest, "Unknown client. Remove the server from your MCP client, add it again and retry.")
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
	// Invalid requests are answered here rather than by redirecting to the
	// client: since anyone can register a client, redirecting before the
	// user has signed in would make this server an open redirector.
	if q.Get("response_type") != "code" {
		s.errorPage(w, http.StatusBadRequest, "Unsupported response_type; only \"code\" is supported.")
		return
	}
	challenge := q.Get("code_challenge")
	if len(challenge) != 43 || q.Get("code_challenge_method") != "S256" {
		s.errorPage(w, http.StatusBadRequest, "PKCE with code_challenge_method=S256 is required.")
		return
	}
	if res := q.Get("resource"); res != "" && !s.isResource(res) {
		s.errorPage(w, http.StatusBadRequest, "The requested resource is not served by this server.")
		return
	}
	state := q.Get("state")
	if len(state) > maxStateLength {
		s.errorPage(w, http.StatusBadRequest, "The state parameter is too long.")
		return
	}
	// The requested scope is ignored: clients ask for different things (for
	// example offline_access), and the grant is always Scope, which the
	// token response reports.

	f := flow{
		Nonce:         randomToken(),
		ClientID:      c.ID,
		RedirectURI:   redirect,
		RedirectGiven: redirectGiven,
		State:         state,
		Challenge:     challenge,
		Expires:       s.cfg.Now().Add(pendingTTL).Unix(),
	}
	s.setFlowCookie(w, &f)
	v := url.Values{
		"client_id":     {s.cfg.DiscordClientID},
		"response_type": {"code"},
		"redirect_uri":  {s.discordRedirectURI()},
		"scope":         {"identify"},
		"state":         {f.Nonce},
	}
	http.Redirect(w, r, s.cfg.DiscordAuthorizeURL+"?"+v.Encode(), http.StatusFound)
}

func (s *Server) discordRedirectURI() string { return s.issuer + "/oauth/discord/callback" }

const flowExpiredMsg = "This sign-in link has expired or was opened in a different browser. Start again from your MCP client."

// handleDiscordCallback finishes the Discord sign-in and shows the consent
// page.
func (s *Server) handleDiscordCallback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	f := s.readFlow(r, q.Get("state"))
	if f == nil || f.UserID != "" {
		s.errorPage(w, http.StatusBadRequest, flowExpiredMsg)
		return
	}
	if q.Get("error") != "" || q.Get("code") == "" {
		s.clearFlowCookie(w, f.Nonce)
		s.redirectError(w, r, f.RedirectURI, f.State, "access_denied", "Discord sign-in was cancelled")
		return
	}
	user, err := s.discordUser(r, q.Get("code"))
	if err != nil {
		s.log.Warn("Discord sign-in failed", "error", err)
		s.clearFlowCookie(w, f.Nonce)
		s.errorPage(w, http.StatusBadGateway, "Signing in with Discord failed. Try again.")
		return
	}
	if !s.cfg.Allowed(user.ID) {
		s.log.Warn("Discord user not allowed", "user_id", user.ID, "username", user.Username)
		s.clearFlowCookie(w, f.Nonce)
		s.redirectError(w, r, f.RedirectURI, f.State, "access_denied",
			"this Discord account is not allowed to use this server")
		return
	}
	f.UserID, f.Username = user.ID, user.Username
	s.setFlowCookie(w, f)
	c := s.store.client(f.ClientID)
	if c == nil {
		s.errorPage(w, http.StatusBadRequest, flowExpiredMsg)
		return
	}
	s.consentPage(w, f, c.Name)
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
	f := s.readFlow(r, r.PostForm.Get("request"))
	if f == nil || f.UserID == "" ||
		subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(s.csrfToken(f.Nonce))) != 1 {
		s.errorPage(w, http.StatusBadRequest, flowExpiredMsg)
		return
	}
	// Each sign-in may be answered once.
	if !s.markFlowDone(f.Nonce, time.Unix(f.Expires, 0)) {
		s.errorPage(w, http.StatusBadRequest, flowExpiredMsg)
		return
	}
	s.clearFlowCookie(w, f.Nonce)
	if r.PostForm.Get("action") != "allow" {
		s.redirectError(w, r, f.RedirectURI, f.State, "access_denied", "the user denied access")
		return
	}
	user := User{ID: f.UserID, Username: f.Username}
	if !s.cfg.Allowed(user.ID) {
		s.redirectError(w, r, f.RedirectURI, f.State, "access_denied", "this Discord account is not allowed to use this server")
		return
	}
	code := randomToken()
	s.mu.Lock()
	s.pruneLocked()
	s.codes[hashToken(code)] = &authCode{
		ClientID:      f.ClientID,
		RedirectURI:   f.RedirectURI,
		RedirectGiven: f.RedirectGiven,
		Challenge:     f.Challenge,
		Scope:         Scope,
		User:          user,
		Family:        randomToken(),
		ExpiresAt:     s.cfg.Now().Add(codeTTL),
	}
	s.mu.Unlock()
	s.log.Info("authorization granted", "client_id", f.ClientID, "user_id", user.ID, "username", user.Username)
	v := url.Values{"code": {code}, "iss": {s.issuer}}
	if f.State != "" {
		v.Set("state", f.State)
	}
	http.Redirect(w, r, appendQuery(f.RedirectURI, v), http.StatusFound)
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

// markFlowDone records that a sign-in was answered and reports whether it
// was the first answer. Only allowed users reach the consent form, so this
// map cannot be grown by anonymous requests.
func (s *Server) markFlowDone(nonce string, expires time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if _, done := s.doneFlows[nonce]; done {
		return false
	}
	s.doneFlows[nonce] = expires
	return true
}

// pruneLocked drops expired codes and answered sign-ins. The caller holds s.mu.
func (s *Server) pruneLocked() {
	now := s.cfg.Now()
	for k, c := range s.codes {
		if !now.Before(c.ExpiresAt) {
			delete(s.codes, k)
		}
	}
	for k, exp := range s.doneFlows {
		if !now.Before(exp) {
			delete(s.doneFlows, k)
		}
	}
}

// Flow cookies. Each sign-in has its own cookie, named after its nonce, so
// several sign-ins can run in one browser. On https the __Host- prefix keeps
// sibling subdomains from setting or overwriting it.
func (s *Server) flowCookieName(nonce string) string {
	prefix := "dmcp_flow_"
	if s.secureCookies() {
		prefix = "__Host-dmcp_flow_"
	}
	return prefix + nonce[:16]
}

func (s *Server) secureCookies() bool { return strings.HasPrefix(s.issuer, "https://") }

func (s *Server) setFlowCookie(w http.ResponseWriter, f *flow) {
	payload, _ := json.Marshal(f)
	body := base64.RawURLEncoding.EncodeToString(payload)
	http.SetCookie(w, &http.Cookie{
		Name:     s.flowCookieName(f.Nonce),
		Value:    body + "." + s.sign("flow", body),
		Path:     "/",
		MaxAge:   int(pendingTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode, // sent on the top-level redirect back from Discord
	})
}

func (s *Server) clearFlowCookie(w http.ResponseWriter, nonce string) {
	http.SetCookie(w, &http.Cookie{
		Name: s.flowCookieName(nonce), Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode,
	})
}

// readFlow returns the sign-in whose nonce is given, from this browser's
// cookie, when the signature is valid and it has not expired.
func (s *Server) readFlow(r *http.Request, nonce string) *flow {
	if len(nonce) < 16 {
		return nil
	}
	cookie, err := r.Cookie(s.flowCookieName(nonce))
	if err != nil {
		return nil
	}
	body, sig, ok := strings.Cut(cookie.Value, ".")
	if !ok || !hmac.Equal([]byte(sig), []byte(s.sign("flow", body))) {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil
	}
	var f flow
	if json.Unmarshal(payload, &f) != nil ||
		subtle.ConstantTimeCompare([]byte(f.Nonce), []byte(nonce)) != 1 ||
		s.cfg.Now().Unix() >= f.Expires {
		return nil
	}
	return &f
}

// sign returns an HMAC-SHA256 of purpose and data under the server key.
func (s *Server) sign(purpose, data string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(purpose))
	m.Write([]byte{0})
	m.Write([]byte(data))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// csrfToken is the consent form token for a sign-in.
func (s *Server) csrfToken(nonce string) string { return s.sign("csrf", nonce) }

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

// redirectError sends an OAuth error to the client's redirect URI. It is
// used only after the user has signed in with Discord.
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

// pageHeaders stops the sign-in pages from being framed or cached. There is
// no form-action directive: browsers apply it to the redirect that follows
// the consent form, which goes to the client's own (other-origin or
// custom-scheme) redirect URI.
func pageHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
body{font-family:system-ui,sans-serif;background:#f4f5f7;color:#1f2328;margin:0;display:flex;min-height:100vh;align-items:center;justify-content:center}
main{background:#fff;border-radius:12px;box-shadow:0 2px 12px rgba(0,0,0,.08);padding:28px;max-width:460px;width:calc(100% - 32px)}
h1{font-size:1.25rem;margin:0 0 12px}p{line-height:1.5}.muted{color:#59636e;font-size:.9rem}code{word-break:break-all}
.row{display:flex;gap:12px;margin-top:20px}button{flex:1;padding:10px;border-radius:8px;border:1px solid #d0d7de;font-size:1rem;cursor:pointer;background:#fff}
button.primary{background:#5865f2;border-color:#5865f2;color:#fff}
@media (prefers-color-scheme:dark){body{background:#0d1117;color:#e6edf3}main{background:#161b22}.muted{color:#9198a1}button{background:#21262d;color:#e6edf3;border-color:#30363d}}
</style></head><body><main>
<h1>{{.Title}}</h1>
{{if .Consent}}
<p>An MCP client wants to manage Discord through this server as <strong>{{.User}}</strong>.</p>
<p>It calls itself <strong>{{if .ClientName}}{{.ClientName}}{{else}}(no name){{end}}</strong>. This name is chosen by the client and is not verified.</p>
<p class="muted">After you allow it, you will be sent to:<br><code>{{.Redirect}}</code><br>Only allow this if you just started the sign-in from your own MCP client and you recognize this address.</p>
<form method="post" action="/oauth/consent">
<input type="hidden" name="request" value="{{.Request}}"><input type="hidden" name="csrf" value="{{.CSRF}}">
<div class="row"><button type="submit" name="action" value="deny">Deny</button><button class="primary" type="submit" name="action" value="allow">Allow</button></div>
</form>
{{else}}<p>{{.Message}}</p>{{end}}
</main></body></html>`))

func (s *Server) consentPage(w http.ResponseWriter, f *flow, clientName string) {
	pageHeaders(w)
	_ = pageTmpl.Execute(w, map[string]any{
		"Title":      "Allow access to Discord?",
		"Consent":    true,
		"ClientName": clientName,
		"User":       f.Username,
		"Redirect":   f.RedirectURI,
		"Request":    f.Nonce,
		"CSRF":       s.csrfToken(f.Nonce),
	})
}

func (s *Server) errorPage(w http.ResponseWriter, status int, msg string) {
	pageHeaders(w)
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, map[string]any{"Title": "Sign-in failed", "Message": msg})
}
