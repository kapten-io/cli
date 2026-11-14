package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kapten-io/cli/internal/contextstore"
	"github.com/kapten-io/cli/internal/credentialstore"
	"github.com/kapten-io/cli/internal/dpop"
	"github.com/kapten-io/cli/internal/fileutil"
	"github.com/kapten-io/cli/internal/oauth"
	"github.com/rest-sh/restish/v2/auth"
)

// apiAuthScheme is how API requests carry the access token. The Engine only
// reads `Authorization: Bearer` and does not verify DPoP proofs yet, like
// for the UI.
// Once it does, switching this to "DPoP" sends the scheme and a proof per
// request.
var apiAuthScheme = "Bearer"

// expiryMargin renews access tokens that expire within it, so a request
// never leaves with a token about to lapse.
const expiryMargin = 30 * time.Second

// refreshMu serializes refreshes within the process; the lock file does
// between processes. A refresh token is single-use: two concurrent
// refreshes would make auth revoke the whole grant.
var refreshMu sync.Mutex

// contextAuth is the Restish auth handler of resource commands and MCP: it
// keeps credentials in Kapten's keychain/file store, never Restish's token
// cache, and signs every request itself.
type contextAuth struct {
	app      *App
	selected contextstore.Context
	store    *credentialstore.Store
	runCtx   context.Context
}

var _ auth.Handler = (*contextAuth)(nil)
var _ auth.ForceCapable = (*contextAuth)(nil)

func (*contextAuth) Parameters() []auth.Param { return nil }
func (*contextAuth) SupportsForce()           {}

// Authenticate is called by Restish for each request, and again with Force
// for the retry after a 401, so each attempt gets its own proof.
func (h *contextAuth) Authenticate(ctx context.Context, req *http.Request, ac auth.AuthContext) error {
	if h.runCtx != nil {
		if err := h.runCtx.Err(); err != nil {
			return err
		}
		// Restish has no public RunContext hook. Merge caller cancellation at its
		// supported request-auth boundary without losing Restish's own context.
		merged, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(h.runCtx, cancel)
		context.AfterFunc(merged, func() { stop() })
		ctx = merged
		*req = *req.WithContext(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	client := h.app.oauthClient(ac.HTTPClient)
	var (
		access string
		key    *dpop.Key
		err    error
	)
	// Only the environment's context has no saved credentials.
	if h.selected.CredentialKey == "" {
		creds, envErr := envCredentials()
		switch {
		case envErr != nil:
			return envErr
		case creds == nil:
			return errors.New("no credentials; run `kapten login` first")
		}
		access, key, err = creds.accessToken(ctx, client, ac.Force)
	} else {
		access, key, err = h.app.storedAccessToken(ctx, client, h.store, &h.selected, ac.Force)
	}
	if err != nil {
		return err
	}

	return authorize(req, access, key)
}

// authorize puts the access token, and a proof for the DPoP scheme, on req.
func authorize(req *http.Request, access string, key *dpop.Key) error {
	if apiAuthScheme != "DPoP" || key == nil {
		req.Header.Set("Authorization", "Bearer "+access)
		return nil
	}
	proof, err := key.Proof(req.Method, req.URL.String(), access, time.Now())
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "DPoP "+access)
	req.Header.Set(dpop.Header, proof)

	return nil
}

func (a *App) oauthClient(supplied *http.Client) *oauth.Client {
	// Preserve Restish's transport/TLS settings without mutating its client.
	client := &http.Client{Timeout: oauth.RequestTimeout}
	if supplied != nil {
		copy := *supplied
		if copy.Timeout == 0 || copy.Timeout > oauth.RequestTimeout {
			copy.Timeout = oauth.RequestTimeout
		}
		client = &copy
	}

	return &oauth.Client{HTTP: client, Sleep: a.Sleep}
}

func fresh(tok credentialstore.Token, now time.Time) bool {
	return tok.AccessToken != "" && (tok.Expiry == 0 || now.Add(expiryMargin).Before(time.Unix(tok.Expiry, 0)))
}

// storedAccessToken returns the access token of a saved context, renewed
// with its refresh token when it expires soon or force is set (the API
// refused it).
func (a *App) storedAccessToken(ctx context.Context, client *oauth.Client, store *credentialstore.Store, selected *contextstore.Context, force bool) (string, *dpop.Key, error) {
	tok, backend, err := store.Get(selected.CredentialKey)
	if err != nil {
		return "", nil, fmt.Errorf("load credentials for context %q: %w; run `kapten login` again", selected.Name, err)
	}
	// The login warned already; warn again only when the store changed.
	if backend != selected.Storage {
		a.warnStorage(backend)
	}
	key, err := dpop.Parse(tok.DPoPKey)
	if err != nil {
		return "", nil, fmt.Errorf("credentials of context %q predate DPoP; run `kapten login` again", selected.Name)
	}
	if !force && fresh(tok, time.Now()) {
		return tok.AccessToken, key, a.recordBackend(selected, backend)
	}
	used := tok.AccessToken

	refreshMu.Lock()
	defer refreshMu.Unlock()
	unlock, err := fileutil.Lock(ctx, filepath.Join(contextstore.Dir(), "refresh.lock"))
	if err != nil {
		return "", nil, err
	}
	defer unlock()
	// Another kapten may have refreshed while this one waited: its token is
	// the one to use, and the refresh token this one read is spent.
	if current, currentBackend, err := store.Get(selected.CredentialKey); err == nil && current.AccessToken != used && fresh(current, time.Now()) {
		return current.AccessToken, key, a.recordBackend(selected, currentBackend)
	} else if err == nil {
		tok = current
	}
	if tok.RefreshToken == "" {
		return "", nil, fmt.Errorf("the session of context %q ended; run `kapten login` again", selected.Name)
	}
	metadata, err := client.Discover(ctx, selected.Issuer)
	if err != nil {
		return "", nil, err
	}
	renewed, err := client.Refresh(ctx, metadata, oauthClientID(), tok.RefreshToken, key)
	if oauth.IsCode(err, "invalid_grant") {
		return "", nil, fmt.Errorf("the session of context %q ended (%v); run `kapten login` again", selected.Name, err)
	}
	if err != nil {
		return "", nil, fmt.Errorf("refresh credentials of context %q: %w", selected.Name, err)
	}
	tok.AccessToken, tok.TokenType, tok.Expiry = renewed.AccessToken, renewed.TokenType, 0
	if !renewed.Expiry.IsZero() {
		tok.Expiry = renewed.Expiry.Unix()
	}
	if renewed.RefreshToken != "" {
		tok.RefreshToken = renewed.RefreshToken
	}
	backend, err = store.Set(selected.CredentialKey, tok)
	if err != nil {
		return "", nil, fmt.Errorf("save refreshed credentials: %w", err)
	}
	if backend != selected.Storage {
		a.warnStorage(backend)
	}

	return tok.AccessToken, key, a.recordBackend(selected, backend)
}

// recordBackend notes in the context where its credentials now live, after
// the keychain became unavailable or available again.
func (a *App) recordBackend(selected *contextstore.Context, backend string) error {
	if backend == selected.Storage || selected.CredentialKey == "" {
		return nil
	}
	c, err := contextstore.Load()
	if err != nil {
		return err
	}
	for i := range c.Contexts {
		if c.Contexts[i].Name == selected.Name && c.Contexts[i].CredentialKey == selected.CredentialKey {
			c.Contexts[i].Storage = backend
			if err := contextstore.Save(c); err != nil {
				return err
			}
			selected.Storage = backend
			break
		}
	}

	return nil
}

// Server mode: an OAuth application authenticates with KAPTEN_CLIENT_ID and
// KAPTEN_CLIENT_SECRET (client secret) or KAPTEN_CLIENT_PRIVATE_KEY /
// KAPTEN_CLIENT_PRIVATE_KEY_FILE (private_key_jwt) through the client
// credentials grant. The token stays in memory, never in the store. An
// explicit --context wins over the environment, which wins over the active
// saved login.
type serverCredentials struct {
	issuer, clientID, secret, privateKey string

	mu     sync.Mutex
	key    *dpop.Key
	token  oauth.Token
	claims oauth.Claims
}

var (
	serverMu    sync.Mutex
	serverCache *serverCredentials
)

// errIncompleteEnvironment is KAPTEN_CLIENT_ID without a credential.
var errIncompleteEnvironment = errors.New("KAPTEN_CLIENT_ID is set without KAPTEN_CLIENT_SECRET, KAPTEN_CLIENT_PRIVATE_KEY or KAPTEN_CLIENT_PRIVATE_KEY_FILE")

// environmentSet reports whether the environment names an OAuth application.
func environmentSet() bool {
	return os.Getenv("KAPTEN_CLIENT_ID") != ""
}

// envCredentials reads server mode from the environment, reusing the
// process's token while the variables stay the same. It returns nil without
// KAPTEN_CLIENT_ID.
func envCredentials() (*serverCredentials, error) {
	id := os.Getenv("KAPTEN_CLIENT_ID")
	if id == "" {
		return nil, nil
	}
	secret, privateKey := os.Getenv("KAPTEN_CLIENT_SECRET"), os.Getenv("KAPTEN_CLIENT_PRIVATE_KEY")
	if path := os.Getenv("KAPTEN_CLIENT_PRIVATE_KEY_FILE"); path != "" && privateKey == "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read KAPTEN_CLIENT_PRIVATE_KEY_FILE: %w", err)
		}
		privateKey = string(data)
	}
	switch {
	case secret == "" && privateKey == "":
		return nil, errIncompleteEnvironment
	case secret != "" && privateKey != "":
		return nil, errors.New("set KAPTEN_CLIENT_SECRET or a private key, not both")
	}
	issuer := env("KAPTEN_ISSUER_URL", defaultIssuerURL)
	serverMu.Lock()
	defer serverMu.Unlock()
	if serverCache == nil || serverCache.issuer != issuer || serverCache.clientID != id || serverCache.secret != secret || serverCache.privateKey != privateKey {
		serverCache = &serverCredentials{issuer: issuer, clientID: id, secret: secret, privateKey: privateKey}
	}

	return serverCache, nil
}

func (s *serverCredentials) accessToken(ctx context.Context, client *oauth.Client, force bool) (string, *dpop.Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && s.token.AccessToken != "" && (s.token.Expiry.IsZero() || time.Now().Add(expiryMargin).Before(s.token.Expiry)) {
		return s.token.AccessToken, s.key, nil
	}
	if s.key == nil {
		key, err := dpop.Generate()
		if err != nil {
			return "", nil, err
		}
		s.key = key
	}
	metadata, err := client.Discover(ctx, s.issuer)
	if err != nil {
		return "", nil, err
	}
	scope := os.Getenv("KAPTEN_OAUTH_SCOPE")
	var token oauth.Token
	if s.secret != "" {
		token, err = client.ClientCredentials(ctx, metadata, s.clientID, s.secret, scope, s.key)
		if err != nil {
			return "", nil, fmt.Errorf("authenticate with KAPTEN_CLIENT_ID and KAPTEN_CLIENT_SECRET: %w", err)
		}
	} else {
		private, parseErr := oauth.ParsePrivateKey([]byte(s.privateKey))
		if parseErr != nil {
			return "", nil, parseErr
		}
		token, err = client.ClientAssertionToken(ctx, metadata, s.clientID, private, scope, s.key)
		if err != nil {
			return "", nil, fmt.Errorf("authenticate with KAPTEN_CLIENT_ID and its private key: %w", err)
		}
	}
	s.token = token
	s.claims, _ = oauth.ReadClaims(token.AccessToken)

	return token.AccessToken, s.key, nil
}

// source names the server-mode credential for whoami.
func (s *serverCredentials) source() string {
	if s.secret != "" {
		return "KAPTEN_CLIENT_ID and KAPTEN_CLIENT_SECRET"
	}

	return "KAPTEN_CLIENT_ID and its private key"
}

func (a *App) warnStorage(backend string) {
	if credentialstore.FileOnly() {
		return
	}
	if backend == "file" && !a.warnedFile {
		a.warnedFile = true
		if a.Err != nil {
			fmt.Fprintln(a.Err, "kapten: warning: the OS keychain is unavailable, so credentials are saved in an owner-only file (set KAPTEN_CREDENTIALS_STORE=file to choose it and silence this warning)")
		}
	}
}

// logoutContext forgets the credentials of one context, or of all. auth
// has no revocation endpoint: the refresh tokens lapse with the browser
// session that approved them, or at the end of their lifetime.
func (a *App) logoutContext(name string, all bool) error {
	c, err := contextstore.Load()
	if err != nil {
		return err
	}
	if !all && name == "" {
		name = c.Active
	}
	if !all && name == "" {
		fmt.Fprintln(a.Out, "Not logged in.")
		return nil
	}
	if !all {
		if _, ok := c.Find(name); !ok {
			return fmt.Errorf("context %q was not found", name)
		}
	}
	store := credentialstore.New("kapten", contextstore.CredentialsPath())
	remaining := make([]contextstore.Context, 0, len(c.Contexts))
	var cleanupErrors []error
	var removed []string
	for _, selected := range c.Contexts {
		if !all && selected.Name != name {
			remaining = append(remaining, selected)
			continue
		}
		if err := store.Delete(selected.CredentialKey); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove credentials for context %q: %w", selected.Name, err))
			remaining = append(remaining, selected)
			continue
		}
		removed = append(removed, selected.Name)
	}
	c.Contexts = remaining
	if _, ok := c.Find(c.Active); !ok {
		c.Active = ""
		if len(remaining) > 0 {
			c.Active = remaining[0].Name
		}
	}
	if err := errors.Join(append(cleanupErrors, contextstore.Save(c))...); err != nil {
		return err
	}
	for _, name := range removed {
		fmt.Fprintf(a.Out, "Logged out of context %s.\n", name)
	}
	if environmentSet() {
		fmt.Fprintln(a.Out, "KAPTEN_CLIENT_ID is still set and keeps authenticating commands.")
	}

	return nil
}
