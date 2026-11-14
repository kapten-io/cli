// Package mockauth is a fake auth service for the CLI's tests and the
// kapten-mock server. It follows the token endpoint of auth
// (auth/internal/oauth): every request carries a DPoP proof it verifies,
// device codes and refresh tokens are bound to the proof key, and refresh
// tokens rotate, a reused one revoking its family. Access tokens it issued
// can be checked with Verify, which the mock Engine does.
package mockauth

import (
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
)

// Server is a fake auth service. It is an http.Handler served at URL, its
// issuer.
type Server struct {
	// URL is the issuer: the base URL the server is reachable at.
	URL string
	// Errorf reports a request the real auth would also refuse but that a
	// correct CLI never sends, such as an invalid DPoP proof.
	Errorf func(format string, args ...any)

	mu sync.Mutex
	// Subject, Email and Organization are put in the next issued tokens.
	Subject, Email, Organization string
	// OrganizationName answers GET /organizations/{Organization}.
	OrganizationName string
	// PendingPolls is how many polls answer authorization_pending before
	// the device code is approved.
	PendingPolls int
	// SlowDowns is how many of those answer slow_down instead.
	SlowDowns int
	// Deny makes the user deny the device request.
	Deny bool
	// Unavailable is how many token requests answer 503, as a gateway does
	// while auth restarts.
	Unavailable int
	// Expire makes the device code expired.
	Expire bool
	// ManualApproval keeps device requests pending until their code is
	// approved, through Approve or the /device page.
	ManualApproval bool
	// Interval is the poll interval, in seconds, given with device codes.
	Interval int
	// TTL is the lifetime of access tokens.
	TTL time.Duration
	// Applications maps OAuth application client ids to their secrets.
	Applications map[string]string
	// ApplicationKeys maps OAuth application client ids to their public keys,
	// for private_key_jwt.
	ApplicationKeys map[string]jwk.Key
	// OnToken runs on each token request, before it is answered.
	OnToken func()

	device   map[string]string // device code -> dpop_jkt
	userCode map[string]string // device code -> user code
	approved map[string]bool   // user codes approved by hand
	access   map[string]*accessToken
	polls    int
	refresh  map[string]*refreshToken
	jtis     map[string]bool
	issued   int
	// Calls counts token requests by grant type.
	Calls map[string]int
	// Proofs are the jkt of every verified token request proof.
	Proofs []string
}

type accessToken struct {
	expiry  time.Time
	revoked *bool
}

type refreshToken struct {
	jkt, subject, organization string
	used, revoked              *bool
}

// New returns a fake auth service. Set URL to where it is served before the
// first request.
func New() *Server {
	s := &Server{
		Errorf:          func(string, ...any) {},
		Interval:        5,
		Subject:         "usr_0b7c1d2e-0000-4000-8000-000000000001",
		Email:           "dev@kapten.local",
		Organization:    "6f1d1e1a-0000-4000-8000-000000000002",
		TTL:             time.Hour,
		Applications:    map[string]string{},
		ApplicationKeys: map[string]jwk.Key{},
		device:          map[string]string{},
		userCode:        map[string]string{},
		approved:        map[string]bool{},
		access:          map[string]*accessToken{},
		refresh:         map[string]*refreshToken{},
		jtis:            map[string]bool{},
		Calls:           map[string]int{},
	}

	return s
}

// ErrInvalidToken is an access token the server did not issue, or that
// expired or was revoked.
var ErrInvalidToken = errors.New("the access token is invalid, expired or revoked")

// Verify checks an access token the way a resource server would.
func (s *Server) Verify(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.access[token]
	if !ok || *row.revoked || !time.Now().Before(row.expiry) {
		return ErrInvalidToken
	}
	return nil
}

// ExpireAccessTokens makes every issued access token expired, as if their
// lifetime had passed; refresh tokens stay valid.
func (s *Server) ExpireAccessTokens() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.access {
		row.expiry = time.Now().Add(-time.Second)
	}
}

// RevokeSessions revokes every grant, as when the browser session that
// approved them signs out: access and refresh tokens stop working.
func (s *Server) RevokeSessions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, row := range s.refresh {
		*row.revoked = true
	}
	for _, row := range s.access {
		*row.revoked = true
	}
}

// Approve approves the pending device request showing userCode.
func (s *Server) Approve(userCode string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, code := range s.userCode {
		if code == userCode {
			s.approved[userCode] = true
			return true
		}
	}
	return false
}

// Close is a no-op: the caller owns the listener.
func (s *Server) Close() {}

// Set changes fields under the server lock.
func (s *Server) Set(f func(*Server)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

// Count is the number of token requests of a grant type.
func (s *Server) Count(grant string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.Calls[grant]
}

// ServeHTTP answers auth's discovery, device, token and organization
// endpoints.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/device" {
		s.devicePage(w, r)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                        s.URL,
			"token_endpoint":                s.URL + "/token",
			"device_authorization_endpoint": s.URL + "/device/authorize",
			"authorization_endpoint":        s.URL + "/authorize",
		})
	case r.Method == http.MethodPost && r.URL.Path == "/device/authorize":
		_ = r.ParseForm()
		jkt := r.Form.Get("dpop_jkt")
		if r.Form.Get("client_id") == "" || len(jkt) != 43 {
			s.fail(w, http.StatusBadRequest, "invalid_request", "client_id and dpop_jkt are required")
			return
		}
		n := len(s.userCode) + 1
		code := fmt.Sprintf("device-%d", n)
		userCode := "WDJB-MJHT"
		if n > 1 {
			userCode = fmt.Sprintf("WDJB-%04d", n)
		}
		s.device[code] = jkt
		s.userCode[code] = userCode
		s.polls = 0
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code":               code,
			"user_code":                 userCode,
			"verification_uri":          s.URL + "/device",
			"verification_uri_complete": s.URL + "/device?user_code=" + userCode,
			"expires_in":                600,
			"interval":                  s.Interval,
		})
	case r.Method == http.MethodPost && r.URL.Path == "/token":
		s.token(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/organizations/"+s.Organization && s.OrganizationName != "":
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); !ok || !s.valid(token) {
			s.fail(w, http.StatusUnauthorized, "unauthorized", "a bearer token is required")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": s.Organization, "name": s.OrganizationName, "slug": "acme"})
	default:
		http.NotFound(w, r)
	}
}

// valid is Verify under the lock.
func (s *Server) valid(token string) bool {
	row, ok := s.access[token]
	return ok && !*row.revoked && time.Now().Before(row.expiry)
}

// devicePage is the page the CLI sends the user to: it shows the code and
// approves it, standing in for auth's sign-in and organization choice.
func (s *Server) devicePage(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("user_code")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodPost {
		if !s.Approve(code) {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprintf(w, "<p>No pending request shows the code %s.</p>", html.EscapeString(code))
			return
		}
		fmt.Fprintf(w, "<p>Approved %s. You can go back to the terminal.</p>", html.EscapeString(code))
		return
	}
	fmt.Fprintf(w, `<form method="post"><p>Kapten mock: approve the CLI showing the code</p>
<input name="user_code" value="%s"> <button>Approve</button></form>`, html.EscapeString(code))
}

func (s *Server) fail(w http.ResponseWriter, status int, code, description string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": description})
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	grant := r.Form.Get("grant_type")
	s.Calls[grant]++
	if s.OnToken != nil {
		s.OnToken()
	}
	if s.Unavailable > 0 {
		s.Unavailable--
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	jkt, err := s.verifyProof(r)
	if err != nil {
		if grant == "client_credentials" && r.Header.Get("DPoP") == "" {
			jkt = ""
		} else {
			s.Errorf("token request %s: %v", grant, err)
			s.fail(w, http.StatusBadRequest, "invalid_dpop_proof", err.Error())
			return
		}
	}
	switch grant {
	case "urn:ietf:params:oauth:grant-type:device_code":
		bound, ok := s.device[r.Form.Get("device_code")]
		switch {
		case !ok:
			s.fail(w, http.StatusBadRequest, "invalid_grant", "device_code is invalid")
		case bound != jkt:
			s.fail(w, http.StatusBadRequest, "invalid_dpop_proof", "proof key does not match dpop_jkt")
		case s.Expire:
			s.fail(w, http.StatusBadRequest, "expired_token", "device_code expired")
		case s.SlowDowns > 0:
			s.SlowDowns--
			s.fail(w, http.StatusBadRequest, "slow_down", "poll at most every 10 seconds")
		case s.ManualApproval && !s.approved[s.userCode[r.Form.Get("device_code")]]:
			s.fail(w, http.StatusBadRequest, "authorization_pending", "the code has not been approved yet")
		case s.polls < s.PendingPolls:
			s.polls++
			s.fail(w, http.StatusBadRequest, "authorization_pending", "the user has not answered yet")
		case s.Deny:
			s.fail(w, http.StatusBadRequest, "access_denied", "the user denied the request")
		default:
			delete(s.device, r.Form.Get("device_code"))
			delete(s.userCode, r.Form.Get("device_code"))
			used, revoked := false, false
			s.issue(w, &refreshToken{jkt: jkt, subject: s.Subject, organization: s.Organization, used: &used, revoked: &revoked})
		}
	case "refresh_token":
		row, ok := s.refresh[r.Form.Get("refresh_token")]
		switch {
		case !ok || *row.revoked:
			s.fail(w, http.StatusBadRequest, "invalid_grant", "refresh token is revoked or expired")
		case row.jkt != jkt:
			s.fail(w, http.StatusBadRequest, "invalid_dpop_proof", "proof key does not match the refresh token binding")
		case *row.used:
			*row.revoked = true
			s.fail(w, http.StatusBadRequest, "invalid_grant", "refresh token was already used; the grant is revoked")
		default:
			*row.used = true
			used := false
			s.issue(w, &refreshToken{jkt: jkt, subject: row.subject, organization: row.organization, used: &used, revoked: row.revoked})
		}
	case "client_credentials":
		if assertion := r.Form.Get("client_assertion"); assertion != "" {
			if err := s.verifyAssertion(r.Form.Get("client_id"), r.Form.Get("client_assertion_type"), assertion); err != nil {
				s.fail(w, http.StatusUnauthorized, "invalid_client", err.Error())
				return
			}
		} else if secret, ok := s.Applications[r.Form.Get("client_id")]; !ok || secret != r.Form.Get("client_secret") {
			s.fail(w, http.StatusUnauthorized, "invalid_client", "the client_id or client_secret is wrong")
			return
		}
		s.issued++
		tokenType := "Bearer"
		if jkt != "" {
			tokenType = "DPoP"
		}
		revoked := false
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":    s.accessToken(map[string]any{"sub": r.Form.Get("client_id"), "client_id": r.Form.Get("client_id"), "client_name": "Deploy bot", "org_id": s.Organization}, &revoked),
			"token_type":      tokenType,
			"expires_in":      int(s.TTL.Seconds()),
			"scope":           "",
			"organization_id": s.Organization,
		})
	default:
		s.fail(w, http.StatusBadRequest, "unsupported_grant_type", grant)
	}
}

func (s *Server) issue(w http.ResponseWriter, row *refreshToken) {
	s.issued++
	refresh := fmt.Sprintf("refresh-%d", s.issued)
	s.refresh[refresh] = row
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":    s.accessToken(map[string]any{"sub": row.subject, "client_id": "cli_9811a064-0835-43f4-a478-9ff95d92bc28", "org_id": row.organization, "email": s.Email, "cnf": map[string]string{"jkt": row.jkt}}, row.revoked),
		"token_type":      "DPoP",
		"expires_in":      int(s.TTL.Seconds()),
		"refresh_token":   refresh,
		"scope":           "",
		"organization_id": row.organization,
	})
}

// accessToken is an unsigned JWT: the CLI only reads its claims, and the
// mock Engine checks it with Verify. It dies with revoked, its grant.
func (s *Server) accessToken(claims map[string]any, revoked *bool) string {
	expiry := time.Now().Add(s.TTL)
	claims["iss"] = s.URL
	claims["exp"] = expiry.Unix()
	claims["jti"] = fmt.Sprintf("at-%d", s.issued)
	header, _ := json.Marshal(map[string]string{"typ": "at+jwt", "alg": "ES256"})
	payload, _ := json.Marshal(claims)
	token := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".c2ln"
	s.access[token] = &accessToken{expiry: expiry, revoked: revoked}

	return token
}

// verifyProof checks the DPoP proof of a token request like auth does.
func (s *Server) verifyProof(r *http.Request) (string, error) {
	values := r.Header.Values("DPoP")
	if len(values) != 1 {
		return "", fmt.Errorf("exactly one DPoP header is required, got %d", len(values))
	}
	message, err := jws.Parse([]byte(values[0]), jws.WithCompact())
	if err != nil || len(message.Signatures()) != 1 {
		return "", fmt.Errorf("parse proof: %v", err)
	}
	headers := message.Signatures()[0].ProtectedHeaders()
	if typ, _ := headers.Type(); typ != "dpop+jwt" {
		return "", fmt.Errorf("proof typ %q", typ)
	}
	alg, ok := headers.Algorithm()
	if !ok || alg.String() != "ES256" {
		return "", fmt.Errorf("proof alg %v", alg)
	}
	key, ok := headers.JWK()
	if !ok {
		return "", fmt.Errorf("proof has no jwk")
	}
	if private, err := jwk.IsPrivateKey(key); err != nil || private {
		return "", fmt.Errorf("proof jwk is private")
	}
	payload, err := jws.Verify([]byte(values[0]), jws.WithKey(alg, key))
	if err != nil {
		return "", fmt.Errorf("proof signature: %v", err)
	}
	var claims struct {
		JTI string  `json:"jti"`
		HTM string  `json:"htm"`
		HTU string  `json:"htu"`
		IAT float64 `json:"iat"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	if claims.HTM != http.MethodPost || claims.HTU != s.URL+"/token" {
		return "", fmt.Errorf("proof htm/htu %s %s", claims.HTM, claims.HTU)
	}
	if iat := time.Unix(int64(claims.IAT), 0); time.Since(iat) > 5*time.Minute || time.Until(iat) > time.Minute {
		return "", fmt.Errorf("proof iat outside the window")
	}
	if claims.JTI == "" || s.jtis[claims.JTI] {
		return "", fmt.Errorf("proof jti missing or replayed")
	}
	s.jtis[claims.JTI] = true
	thumbprint, err := key.Thumbprint(crypto.SHA256)
	if err != nil {
		return "", err
	}
	jkt := base64.RawURLEncoding.EncodeToString(thumbprint)
	s.Proofs = append(s.Proofs, jkt)

	return jkt, nil
}

// verifyAssertion checks a private_key_jwt client assertion like auth does.
func (s *Server) verifyAssertion(clientID, assertionType, assertion string) error {
	if assertionType != "urn:ietf:params:oauth:client-assertion-type:jwt-bearer" {
		return fmt.Errorf("client_assertion_type is %q", assertionType)
	}
	key, ok := s.ApplicationKeys[clientID]
	if !ok {
		return fmt.Errorf("the client is unknown")
	}
	message, err := jws.Parse([]byte(assertion), jws.WithCompact())
	if err != nil || len(message.Signatures()) != 1 {
		return fmt.Errorf("client_assertion is not a compact JWS")
	}
	alg, ok := message.Signatures()[0].ProtectedHeaders().Algorithm()
	if !ok {
		return fmt.Errorf("client_assertion has no alg")
	}
	payload, err := jws.Verify([]byte(assertion), jws.WithKey(alg, key))
	if err != nil {
		return fmt.Errorf("client_assertion signature does not verify")
	}
	var claims struct {
		Iss, Sub, Aud, JTI string
		Exp                int64
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return err
	}
	switch {
	case claims.Iss != clientID || claims.Sub != clientID:
		return fmt.Errorf("iss and sub must be the client_id")
	case claims.Aud != s.URL+"/token" && claims.Aud != s.URL:
		return fmt.Errorf("aud must be the issuer or the token endpoint")
	case claims.JTI == "" || s.jtis[claims.JTI]:
		return fmt.Errorf("jti missing or replayed")
	case claims.Exp < time.Now().Unix() || claims.Exp > time.Now().Add(11*time.Minute).Unix():
		return fmt.Errorf("exp outside the window")
	}
	s.jtis[claims.JTI] = true

	return nil
}
