package oauth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

const maxBodyBytes = 64 << 10

// Client authentication methods.
const (
	authNone  = "none"
	authBasic = "client_secret_basic"
	authPost  = "client_secret_post"
)

type registrationRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	ClientName              string   `json:"client_name"`
}

// handleRegister implements dynamic client registration (RFC 7591).
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if cors(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req registrationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "request body must be a JSON object")
		return
	}
	if len(req.RedirectURIs) == 0 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uris is required")
		return
	}
	if len(req.RedirectURIs) > 10 {
		oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", "at most 10 redirect_uris are allowed")
		return
	}
	for _, u := range req.RedirectURIs {
		if err := validateRedirectURI(u); err != nil {
			oauthError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
			return
		}
	}
	method := req.TokenEndpointAuthMethod
	if method == "" {
		method = authBasic // the RFC 7591 default
	}
	if method != authNone && method != authBasic && method != authPost {
		oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
		return
	}
	grants := req.GrantTypes
	if len(grants) == 0 {
		grants = []string{"authorization_code"}
	}
	for _, g := range grants {
		if g != "authorization_code" && g != "refresh_token" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported grant_type "+g)
			return
		}
	}
	for _, rt := range req.ResponseTypes {
		if rt != "code" {
			oauthError(w, http.StatusBadRequest, "invalid_client_metadata", "unsupported response_type "+rt)
			return
		}
	}
	name := strings.TrimSpace(req.ClientName)
	if len(name) > 100 {
		name = name[:100]
	}

	now := s.cfg.Now()
	c := &Client{
		ID:           randomToken(),
		Name:         name,
		RedirectURIs: req.RedirectURIs,
		AuthMethod:   method,
		CreatedAt:    now,
		LastUsedAt:   now,
	}
	secret := ""
	if method != authNone {
		secret = randomToken()
		c.SecretHash = hashToken(secret)
	}
	if err := s.store.addClient(c); err != nil {
		s.log.Error("saving OAuth client failed", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if s.store.clientCount() > maxClients {
		if err := s.store.pruneClients(maxClients); err != nil {
			s.log.Error("pruning OAuth clients failed", "error", err)
		}
	}
	s.log.Info("OAuth client registered", "client_id", c.ID, "name", name, "auth_method", method)

	resp := map[string]any{
		"client_id":                  c.ID,
		"client_id_issued_at":        now.Unix(),
		"redirect_uris":              c.RedirectURIs,
		"token_endpoint_auth_method": method,
		"grant_types":                grants,
		"response_types":             []string{"code"},
	}
	if name != "" {
		resp["client_name"] = name
	}
	if secret != "" {
		resp["client_secret"] = secret
		resp["client_secret_expires_at"] = 0
	}
	writeJSON(w, http.StatusCreated, resp)
}

// validateRedirectURI accepts https URLs, http URLs on a loopback host (for
// native apps, RFC 8252) and private-use schemes such as cursor://.
func validateRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return errors.New("redirect_uri must be an absolute URI")
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return errors.New("redirect_uri must not contain a fragment")
	}
	switch scheme := strings.ToLower(u.Scheme); scheme {
	case "https":
		if u.Host == "" {
			return errors.New("redirect_uri must have a host")
		}
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return errors.New("http redirect_uri is only allowed for localhost")
		}
	case "javascript", "data", "file", "vbscript", "blob", "about", "ftp", "ws", "wss":
		return errors.New("redirect_uri scheme is not allowed")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// redirectMatches reports whether requested matches a registered redirect
// URI. Matching is exact, except that for loopback http URIs the port may
// differ (RFC 8252 section 7.3).
func redirectMatches(registered, requested string) bool {
	if registered == requested {
		return true
	}
	a, err1 := url.Parse(registered)
	b, err2 := url.Parse(requested)
	if err1 != nil || err2 != nil || a.Scheme != "http" || b.Scheme != "http" ||
		!isLoopbackHost(a.Hostname()) || a.Hostname() != b.Hostname() {
		return false
	}
	return a.Path == b.Path && a.RawQuery == b.RawQuery && a.User == nil && b.User == nil
}

// authenticateClient identifies the client of a token or revocation
// request. Confidential clients must present their secret (HTTP Basic or
// form fields); public clients send only client_id.
func (s *Server) authenticateClient(w http.ResponseWriter, r *http.Request) *Client {
	id, secret, basic := r.BasicAuth()
	if basic {
		var err1, err2 error
		id, err1 = url.QueryUnescape(id)
		secret, err2 = url.QueryUnescape(secret)
		if err1 != nil || err2 != nil {
			basic = false
			id, secret = "", ""
		}
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	fail := func() *Client {
		if basic {
			w.Header().Set("WWW-Authenticate", `Basic realm="discord-mcp"`)
		}
		oauthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return nil
	}
	c := s.store.client(id)
	if id == "" || c == nil {
		return fail()
	}
	if c.AuthMethod == authNone {
		if secret != "" {
			return fail()
		}
		return c
	}
	if secret == "" || subtle.ConstantTimeCompare([]byte(hashToken(secret)), []byte(c.SecretHash)) != 1 {
		return fail()
	}
	return c
}

func (s *Server) handleToken(w http.ResponseWriter, r *http.Request) {
	if cors(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}
	c := s.authenticateClient(w, r)
	if c == nil {
		return
	}
	if res := r.PostForm.Get("resource"); res != "" && !s.isResource(res) {
		oauthError(w, http.StatusBadRequest, "invalid_target", "unknown resource")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, r, c)
	case "refresh_token":
		s.refresh(w, r, c)
	case "":
		oauthError(w, http.StatusBadRequest, "invalid_request", "grant_type is required")
	default:
		oauthError(w, http.StatusBadRequest, "unsupported_grant_type", "")
	}
}

func (s *Server) exchangeCode(w http.ResponseWriter, r *http.Request, c *Client) {
	code, err := s.takeCode(r.PostForm.Get("code"))
	if errors.Is(err, errCodeReused) {
		s.log.Warn("authorization code reused; revoking its tokens", "client_id", c.ID)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "authorization code already used")
		return
	}
	if code == nil || code.ClientID != c.ID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired authorization code")
		return
	}
	redirect := r.PostForm.Get("redirect_uri")
	if (code.RedirectGiven || redirect != "") && redirect != code.RedirectURI {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
		return
	}
	if !verifyPKCE(r.PostForm.Get("code_verifier"), code.Challenge) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
		return
	}
	if !s.cfg.Allowed(code.User.ID) {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "Discord user is not allowed")
		return
	}
	s.issueTokens(w, c.ID, code.User, code.Scope, code.Family)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request, c *Client) {
	value := r.PostForm.Get("refresh_token")
	if value == "" {
		oauthError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	t, err := s.store.useRefresh(value)
	if errors.Is(err, errRefreshReused) {
		s.log.Warn("refresh token reused; revoked the token family", "client_id", c.ID)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "refresh token already used")
		return
	}
	if err != nil {
		s.log.Error("saving OAuth tokens failed", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	if t == nil || t.ClientID != c.ID {
		oauthError(w, http.StatusBadRequest, "invalid_grant", "invalid or expired refresh token")
		return
	}
	if !s.cfg.Allowed(t.UserID) {
		_ = s.store.revokeFamily(t.Family)
		oauthError(w, http.StatusBadRequest, "invalid_grant", "Discord user is no longer allowed")
		return
	}
	s.issueTokens(w, c.ID, User{ID: t.UserID, Username: t.UserName}, t.Scope, t.Family)
}

// issueTokens creates an access and a refresh token and writes the token
// response.
func (s *Server) issueTokens(w http.ResponseWriter, clientID string, u User, scope, family string) {
	now := s.cfg.Now()
	base := Token{ClientID: clientID, UserID: u.ID, UserName: u.Username, Resource: s.resource, Scope: scope, Family: family}

	access := base
	access.Kind, access.ExpiresAt = kindAccess, now.Add(AccessTokenTTL)
	accessValue, err := s.store.issue(access)
	if err != nil {
		s.log.Error("saving OAuth tokens failed", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	refresh := base
	refresh.Kind, refresh.ExpiresAt = kindRefresh, now.Add(RefreshTokenTTL)
	refreshValue, err := s.store.issue(refresh)
	if err != nil {
		s.log.Error("saving OAuth tokens failed", "error", err)
		oauthError(w, http.StatusInternalServerError, "server_error", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token":  accessValue,
		"token_type":    "Bearer",
		"expires_in":    int(AccessTokenTTL.Seconds()),
		"refresh_token": refreshValue,
		"scope":         scope,
	})
}

// handleRevoke implements token revocation (RFC 7009). Unknown tokens are
// not an error.
func (s *Server) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if cors(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		oauthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}
	c := s.authenticateClient(w, r)
	if c == nil {
		return
	}
	if err := s.store.revoke(r.PostForm.Get("token"), c.ID); err != nil {
		s.log.Error("saving OAuth tokens failed", "error", err)
	}
	w.WriteHeader(http.StatusOK)
}

var verifierRe = regexp.MustCompile(`^[A-Za-z0-9\-._~]{43,128}$`)

// verifyPKCE checks an S256 code verifier against the stored challenge.
func verifyPKCE(verifier, challenge string) bool {
	if !verifierRe.MatchString(verifier) {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	got := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(challenge)) == 1
}

// isResource reports whether res names this server's MCP endpoint. A
// trailing slash and the bare base URL are accepted too.
func (s *Server) isResource(res string) bool {
	res = strings.TrimRight(res, "/")
	return slices.Contains([]string{s.resource, s.issuer}, res)
}
