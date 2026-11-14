// Package oauth is the CLI's client for the auth service: discovery, the
// device authorization grant (RFC 8628) for `kapten login`, refresh tokens
// that rotate on every use, and the client credentials grant for OAuth
// applications. Every token request carries a DPoP proof (RFC 9449).
package oauth

import (
	"context"
	"crypto"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kapten-io/cli/internal/dpop"
	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jws"
)

// Grant types.
const (
	GrantDeviceCode        = "urn:ietf:params:oauth:grant-type:device_code"
	GrantRefreshToken      = "refresh_token"
	GrantClientCredentials = "client_credentials"
)

// Device token errors (RFC 8628 section 3.5).
const (
	ErrorAuthorizationPending = "authorization_pending"
	ErrorSlowDown             = "slow_down"
	ErrorAccessDenied         = "access_denied"
	ErrorExpiredToken         = "expired_token"
)

// slowDownStep is added to the polling interval on each slow_down.
const slowDownStep = 5 * time.Second

const maxResponseBytes = 1 << 20

// RequestTimeout bounds each request to auth.
const RequestTimeout = 30 * time.Second

// Metadata is the part of the authorization server metadata the CLI uses.
type Metadata struct {
	Issuer                      string `json:"issuer"`
	TokenEndpoint               string `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
}

// Token is a token endpoint response.
type Token struct {
	AccessToken  string
	TokenType    string
	RefreshToken string
	Scope        string
	// OrganizationID is the organization the access token is scoped to,
	// empty while the user belongs to none.
	OrganizationID string
	// Expiry is zero when the server sent no expires_in.
	Expiry time.Time
}

// Error is an OAuth error response.
type Error struct {
	Code        string `json:"error"`
	Description string `json:"error_description"`
	Status      int    `json:"-"`
}

func (e *Error) Error() string {
	if e.Description == "" {
		return e.Code
	}

	return e.Code + ": " + e.Description
}

// IsCode reports whether err is an OAuth error with this code.
func IsCode(err error, code string) bool {
	var oauthErr *Error

	return errors.As(err, &oauthErr) && oauthErr.Code == code
}

// Client talks to one auth service.
type Client struct {
	HTTP *http.Client
	// Now overrides the clock of DPoP proofs and expiries in tests.
	Now func() time.Time
	// Sleep waits between device polls; tests replace it.
	Sleep func(context.Context, time.Duration) error
}

func (c *Client) http() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}

	return &http.Client{Timeout: RequestTimeout}
}

func (c *Client) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}

	return time.Now()
}

func (c *Client) sleep(ctx context.Context, d time.Duration) error {
	if c != nil && c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Discover reads the issuer's metadata at /.well-known/openid-configuration.
func (c *Client) Discover(ctx context.Context, issuer string) (Metadata, error) {
	issuer = strings.TrimRight(issuer, "/")
	u, err := url.Parse(issuer)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return Metadata{}, fmt.Errorf("invalid issuer URL %q", issuer)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return Metadata{}, fmt.Errorf("oauth discovery: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	res, err := c.http().Do(req)
	if err != nil {
		return Metadata{}, requestError(ctx, "oauth discovery", issuer)
	}
	defer res.Body.Close()
	body, err := readBounded(res.Body)
	if err != nil {
		return Metadata{}, err
	}
	if res.StatusCode != http.StatusOK {
		return Metadata{}, fmt.Errorf("oauth discovery returned HTTP %d", res.StatusCode)
	}
	var metadata Metadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return Metadata{}, errors.New("oauth discovery: invalid metadata document")
	}
	// RFC 8414 section 3.3: the metadata must name the issuer it was fetched for.
	if strings.TrimRight(metadata.Issuer, "/") != issuer {
		return Metadata{}, fmt.Errorf("oauth discovery: metadata is for issuer %q, not %q", metadata.Issuer, issuer)
	}
	if metadata.TokenEndpoint == "" {
		return Metadata{}, errors.New("oauth discovery: metadata has no token_endpoint")
	}

	return metadata, nil
}

// DeviceAuthorization is a started device authorization (RFC 8628 section 3.2).
type DeviceAuthorization struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// StartDevice asks for a device code bound to key (dpop_jkt).
func (c *Client) StartDevice(ctx context.Context, metadata Metadata, clientID, scope string, key *dpop.Key) (DeviceAuthorization, error) {
	if metadata.DeviceAuthorizationEndpoint == "" {
		return DeviceAuthorization{}, errors.New("the auth service does not offer the device authorization grant")
	}
	form := url.Values{"client_id": {clientID}, "dpop_jkt": {key.Thumbprint()}}
	if scope != "" {
		form.Set("scope", scope)
	}
	var device DeviceAuthorization
	if err := c.post(ctx, metadata.DeviceAuthorizationEndpoint, form, nil, "device authorization", &device); err != nil {
		return DeviceAuthorization{}, err
	}
	if device.DeviceCode == "" || device.UserCode == "" || device.VerificationURI == "" {
		return DeviceAuthorization{}, errors.New("device authorization: incomplete response")
	}

	return device, nil
}

// PollDevice polls the token endpoint until the user approves, denies, or
// the device code expires. It waits interval seconds between polls and five
// more after each slow_down.
func (c *Client) PollDevice(ctx context.Context, metadata Metadata, clientID string, device DeviceAuthorization, key *dpop.Key) (Token, error) {
	interval := time.Duration(device.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if device.ExpiresIn > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(device.ExpiresIn)*time.Second)
		defer cancel()
	}
	form := url.Values{"grant_type": {GrantDeviceCode}, "device_code": {device.DeviceCode}, "client_id": {clientID}}
	for {
		if err := c.sleep(ctx, interval); err != nil {
			if errors.Is(err, context.DeadlineExceeded) && device.ExpiresIn > 0 {
				return Token{}, &Error{Code: ErrorExpiredToken, Description: "the code expired before it was approved"}
			}
			return Token{}, err
		}
		token, err := c.token(ctx, metadata, form, key)
		switch {
		case IsCode(err, ErrorAuthorizationPending):
			continue
		case IsCode(err, ErrorSlowDown):
			interval += slowDownStep
			continue
		case ctx.Err() == nil && IsTransient(err):
			// RFC 8628 §3.5: a connection timeout is not a refusal. Back
			// off and keep polling until the code expires.
			interval = min(2*interval, maxPollInterval)
			continue
		}

		return token, err
	}
}

// Refresh redeems a refresh token. auth rotates it: the response carries
// the next one and the old one is spent.
func (c *Client) Refresh(ctx context.Context, metadata Metadata, clientID, refreshToken string, key *dpop.Key) (Token, error) {
	if refreshToken == "" {
		return Token{}, errors.New("refresh token is empty")
	}
	form := url.Values{"grant_type": {GrantRefreshToken}, "refresh_token": {refreshToken}, "client_id": {clientID}}

	return c.token(ctx, metadata, form, key)
}

// ClientCredentials authenticates an OAuth application with its secret
// (client_secret_post). With a key the token is DPoP-bound.
func (c *Client) ClientCredentials(ctx context.Context, metadata Metadata, clientID, clientSecret, scope string, key *dpop.Key) (Token, error) {
	form := url.Values{"grant_type": {GrantClientCredentials}, "client_id": {clientID}, "client_secret": {clientSecret}}
	if scope != "" {
		form.Set("scope", scope)
	}

	return c.token(ctx, metadata, form, key)
}

func (c *Client) token(ctx context.Context, metadata Metadata, form url.Values, key *dpop.Key) (Token, error) {
	headers := http.Header{}
	if key != nil {
		proof, err := key.Proof(http.MethodPost, metadata.TokenEndpoint, "", c.now())
		if err != nil {
			return Token{}, err
		}
		headers.Set(dpop.Header, proof)
	}
	var payload struct {
		AccessToken    string `json:"access_token"`
		TokenType      string `json:"token_type"`
		ExpiresIn      int64  `json:"expires_in"`
		RefreshToken   string `json:"refresh_token"`
		Scope          string `json:"scope"`
		OrganizationID string `json:"organization_id"`
	}
	if err := c.post(ctx, metadata.TokenEndpoint, form, headers, "token request", &payload); err != nil {
		return Token{}, err
	}
	if payload.AccessToken == "" {
		return Token{}, errors.New("token request: response has no access token")
	}
	token := Token{
		AccessToken:    payload.AccessToken,
		TokenType:      payload.TokenType,
		RefreshToken:   payload.RefreshToken,
		Scope:          payload.Scope,
		OrganizationID: payload.OrganizationID,
	}
	if payload.ExpiresIn > 0 {
		token.Expiry = c.now().Add(time.Duration(payload.ExpiresIn) * time.Second)
	}

	return token, nil
}

// post sends a form and decodes a JSON answer, or returns the OAuth error.
// Transport errors are reported without their text, which can carry the
// request URL and form.
func (c *Client) post(ctx context.Context, endpoint string, form url.Values, headers http.Header, operation string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("%s: invalid endpoint", operation)
	}
	for name, values := range headers {
		req.Header[name] = values
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	res, err := c.http().Do(req)
	if err != nil {
		return requestError(ctx, operation, endpoint)
	}
	defer res.Body.Close()
	body, err := readBounded(res.Body)
	if err != nil {
		return err
	}
	switch res.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return fmt.Errorf("%s returned HTTP %d: %w", operation, res.StatusCode, ErrUnavailable)
	}
	if res.StatusCode/100 != 2 {
		oauthErr := &Error{Status: res.StatusCode}
		if json.Unmarshal(body, oauthErr) != nil || oauthErr.Code == "" {
			return fmt.Errorf("%s returned HTTP %d", operation, res.StatusCode)
		}
		return oauthErr
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s: invalid JSON response", operation)
	}

	return nil
}

// ErrUnavailable marks a request that did not reach the auth service, or
// that it answered with a gateway or availability error: retrying later may
// succeed.
var ErrUnavailable = errors.New("the auth service is unavailable")

// maxPollInterval bounds the device poll backoff after transient errors.
const maxPollInterval = 60 * time.Second

// IsTransient reports whether err is worth retrying later.
func IsTransient(err error) bool {
	return errors.Is(err, ErrUnavailable)
}

func requestError(ctx context.Context, operation, endpoint string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	host := "the auth service"
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		host = u.Host
	}

	return fmt.Errorf("%s: could not reach %s: %w", operation, host, ErrUnavailable)
}

func readBounded(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		// Interrupted or timed out mid-body: as transient as a refused
		// connection. The caller's cancellation is checked first by callers.
		return nil, fmt.Errorf("read auth response failed: %w", ErrUnavailable)
	}
	if len(b) > maxResponseBytes {
		return nil, errors.New("auth response exceeds size limit")
	}

	return b, nil
}

// Claims are the access token claims the CLI shows and keys contexts by.
type Claims struct {
	Subject        string `json:"sub"`
	ClientID       string `json:"client_id"`
	OrganizationID string `json:"org_id"`
	Email          string `json:"email"`
	ClientName     string `json:"client_name"`
	Scope          string `json:"scope"`
	ExpiresAt      int64  `json:"exp"`
}

// ReadClaims decodes an access token without verifying it. The CLI got the
// token straight from the token endpoint; the Engine verifies it.
func ReadClaims(accessToken string) (Claims, error) {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("access token is not a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, errors.New("access token payload is not base64url")
	}
	var claims Claims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Claims{}, errors.New("access token payload is not JSON")
	}

	return claims, nil
}

// ClientAssertionType is the client_assertion_type of private_key_jwt (RFC 7523).
const ClientAssertionType = "urn:ietf:params:oauth:client-assertion-type:jwt-bearer"

// assertionLifetime is how long a client assertion is valid; auth accepts
// up to ten minutes.
const assertionLifetime = 5 * time.Minute

// ParsePrivateKey reads an OAuth application's private key, a JWK or PEM.
func ParsePrivateKey(raw []byte) (jwk.Key, error) {
	key, err := jwk.ParseKey(raw)
	if err != nil {
		key, err = jwk.ParseKey(raw, jwk.WithPEM(true))
	}
	if err != nil {
		return nil, errors.New("the client private key is neither a JWK nor a PEM key")
	}
	if private, err := jwk.IsPrivateKey(key); err != nil || !private {
		return nil, errors.New("the client private key is a public key")
	}
	if _, err := assertionAlgorithm(key); err != nil {
		return nil, err
	}

	return key, nil
}

// assertionAlgorithm picks the signature algorithm of a key, as auth's
// clientkey package accepts it.
func assertionAlgorithm(key jwk.Key) (jwa.SignatureAlgorithm, error) {
	if algorithm, ok := key.Algorithm(); ok {
		if signature, ok := jwa.LookupSignatureAlgorithm(algorithm.String()); ok {
			return signature, nil
		}
	}
	switch key.KeyType() {
	case jwa.RSA():
		return jwa.RS256(), nil
	case jwa.EC():
		if crv, ok := key.(jwk.ECDSAPrivateKey); ok {
			if curve, ok := crv.Crv(); ok {
				switch curve {
				case jwa.P256():
					return jwa.ES256(), nil
				case jwa.P384():
					return jwa.ES384(), nil
				case jwa.P521():
					return jwa.ES512(), nil
				}
			}
		}
	case jwa.OKP():
		return jwa.EdDSA(), nil
	}

	return jwa.SignatureAlgorithm{}, errors.New("the client private key must be an EC, Ed25519 or RSA key")
}

// ClientAssertionToken authenticates an OAuth application with a client
// assertion signed by its private key (private_key_jwt, RFC 7523). With a
// DPoP key the token is DPoP-bound.
func (c *Client) ClientAssertionToken(ctx context.Context, metadata Metadata, clientID string, private jwk.Key, scope string, key *dpop.Key) (Token, error) {
	algorithm, err := assertionAlgorithm(private)
	if err != nil {
		return Token{}, err
	}
	kid, ok := private.KeyID()
	if !ok {
		// auth also matches a key by its thumbprint.
		thumbprint, err := private.Thumbprint(crypto.SHA256)
		if err != nil {
			return Token{}, fmt.Errorf("compute client key thumbprint: %w", err)
		}
		kid = base64.RawURLEncoding.EncodeToString(thumbprint)
	}
	now := c.now()
	jti := make([]byte, 16)
	_, _ = rand.Read(jti)
	payload, err := json.Marshal(map[string]any{
		"iss": clientID,
		"sub": clientID,
		"aud": metadata.TokenEndpoint,
		"jti": base64.RawURLEncoding.EncodeToString(jti),
		"iat": now.Unix(),
		"exp": now.Add(assertionLifetime).Unix(),
	})
	if err != nil {
		return Token{}, err
	}
	headers := jws.NewHeaders()
	if err := headers.Set(jws.KeyIDKey, kid); err != nil {
		return Token{}, err
	}
	if err := headers.Set(jws.TypeKey, "JWT"); err != nil {
		return Token{}, err
	}
	assertion, err := jws.Sign(payload, jws.WithKey(algorithm, private, jws.WithProtectedHeaders(headers)))
	if err != nil {
		return Token{}, fmt.Errorf("sign client assertion: %w", err)
	}
	form := url.Values{
		"grant_type":            {GrantClientCredentials},
		"client_id":             {clientID},
		"client_assertion_type": {ClientAssertionType},
		"client_assertion":      {string(assertion)},
	}
	if scope != "" {
		form.Set("scope", scope)
	}

	return c.token(ctx, metadata, form, key)
}
