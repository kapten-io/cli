package app

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kapten-io/cli/internal/contextstore"
	"github.com/kapten-io/cli/internal/credentialstore"
	"github.com/kapten-io/cli/internal/dpop"
	"github.com/kapten-io/cli/internal/oauth/oauthtest"
	"github.com/kapten-io/cli/internal/spec"
	"github.com/kapten-io/cli/internal/mockengine"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/zalando/go-keyring"
)

func noSleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// loginApp is an App that logs in against server without waiting between
// polls; opened collects the pages it opens in the browser.
func loginApp(t *testing.T, server *oauthtest.Server, api string) (a *App, out, stderr *bytes.Buffer, opened *[]string) {
	t.Helper()
	t.Setenv("KAPTEN_ISSUER_URL", server.URL)
	t.Setenv("KAPTEN_API_URL", api)
	out, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	a = New(out, stderr)
	a.Sleep = noSleep
	pages := []string{}
	a.OpenBrowser = func(_ context.Context, page string) error {
		pages = append(pages, page)
		return nil
	}

	return a, out, stderr, &pages
}

func TestAuthCommandsDeviceWorkflow(t *testing.T) {
	for _, backend := range []string{"keychain", "file"} {
		t.Run(backend, func(t *testing.T) { runDeviceAuthWorkflow(t, backend) })
	}
}

func runDeviceAuthWorkflow(t *testing.T, backend string) {
	keyring.MockInit()
	if backend == "file" {
		fallbackKeyring(t)
	}
	t.Setenv("KAPTEN_CONFIG_DIR", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	server := oauthtest.New(t)
	server.OrganizationName = "Acme"
	server.PendingPolls = 2
	a, out, stderr, opened := loginApp(t, server, server.URL+"/api")
	run := func(args ...string) {
		t.Helper()
		out.Reset()
		stderr.Reset()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.RunContext(ctx, append([]string{"kapten"}, args...)); err != nil {
			t.Fatalf("%v: %v; %s", args, err, stderr.String())
		}
	}
	for _, args := range [][]string{{"auth"}, {"auth", "--help"}, {"auth", "login", "--help"}, {"login", "--help"}, {"whoami", "--help"}, {"auth", "list", "--help"}, {"auth", "use", "--help"}, {"auth", "logout", "--help"}} {
		run(args...)
		if !strings.Contains(out.String(), "Usage:") {
			t.Fatalf("help absent: %s", out.String())
		}
	}

	run("login")
	if !strings.Contains(out.String(), server.URL+"/device?user_code=WDJB-MJHT") || !strings.Contains(out.String(), "WDJB-MJHT") || !strings.Contains(out.String(), "Logged in to organization Acme") {
		t.Fatalf("login output: %s", out.String())
	}
	if len(*opened) != 1 || (*opened)[0] != server.URL+"/device?user_code=WDJB-MJHT" {
		t.Fatalf("opened %v", *opened)
	}
	c, err := contextstore.Load()
	if err != nil || len(c.Contexts) != 1 || c.Active != "acme" {
		t.Fatalf("default context named after the organization slug: %+v %v", c, err)
	}
	saved := c.Contexts[0]
	if saved.Subject != server.Subject || saved.OrganizationID != server.Organization || saved.Email != server.Email || saved.OrganizationName != "Acme" || saved.Issuer != server.URL || saved.Storage != backend {
		t.Fatalf("context = %+v", saved)
	}
	store := credentialstore.New("kapten", contextstore.CredentialsPath())
	tok, _, err := store.Get(saved.CredentialKey)
	if err != nil || tok.RefreshToken == "" || tok.TokenType != "DPoP" || tok.Expiry == 0 {
		t.Fatalf("credentials = %+v %v", tok, err)
	}
	key, err := dpop.Parse(tok.DPoPKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, jkt := range server.Proofs {
		if jkt != key.Thumbprint() {
			t.Fatal("the saved key is not the one the grant is bound to")
		}
	}
	if backend == "file" && runtime.GOOS != "windows" {
		for _, path := range []string{contextstore.Path(), contextstore.CredentialsPath()} {
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("login did not persist private file %s: %v", path, err)
			}
		}
	}

	// The same identity updates its context; --no-browser opens nothing.
	run("auth", "login", "--name", "ignored", "--no-browser")
	if len(*opened) != 1 {
		t.Fatal("--no-browser opened a browser")
	}
	c, _ = contextstore.Load()
	if len(c.Contexts) != 1 || c.Active != "acme" {
		t.Fatalf("same identity duplicated: %+v", c)
	}
	server.Set(func(s *oauthtest.Server) {
		s.Subject = "usr_0b7c1d2e-0000-4000-8000-000000000009"
		s.Email = "second@kapten.local"
	})
	run("login", "--name", "work")
	c, _ = contextstore.Load()
	if len(c.Contexts) != 2 || c.Active != "work" {
		t.Fatalf("different identity not separated: %+v", c)
	}

	for _, args := range [][]string{{"auth", "list"}, {"auth", "list", "--json"}, {"auth", "list", "-ojson"}} {
		run(args...)
		if !strings.Contains(out.String(), "work") || !strings.Contains(out.String(), "acme") {
			t.Fatalf("list missing context: %s", out.String())
		}
		if len(args) > 2 {
			var value map[string]any
			if err := json.Unmarshal(out.Bytes(), &value); err != nil {
				t.Fatal(err)
			}
		}
		if strings.Contains(out.String(), "refresh-") || strings.Contains(out.String(), "access_token") || strings.Contains(out.String(), `"d"`) {
			t.Fatal("list leaked credentials")
		}
	}

	run("whoami")
	for _, want := range []string{"User second@kapten.local", "Organization Acme (" + server.Organization + ")", "From context work", "Issuer " + server.URL} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("whoami lacks %q: %s", want, out.String())
		}
	}
	run("--context", "acme", "whoami", "--json")
	var who identity
	if err := json.Unmarshal(out.Bytes(), &who); err != nil || who.Context != "acme" || who.Email != "dev@kapten.local" || who.Source != "login" || who.ExpiresAt == "" {
		t.Fatalf("whoami --json: %+v %v", who, err)
	}

	before := c.Active
	for _, args := range [][]string{{"auth", "use", "missing"}, {"logout", "missing"}} {
		if err := a.Run(append([]string{"kapten"}, args...)); err == nil {
			t.Fatalf("missing context accepted: %v", args)
		}
		unchanged, _ := contextstore.Load()
		if unchanged.Active != before || len(unchanged.Contexts) != 2 {
			t.Fatal("invalid command changed config")
		}
	}
	run("auth", "use", "acme")
	run("logout")
	c, _ = contextstore.Load()
	if len(c.Contexts) != 1 || c.Active != "work" || !strings.Contains(out.String(), "Logged out of context acme") {
		t.Fatalf("default logout did not select survivor: %+v %s", c, out.String())
	}
	if _, _, err := store.Get(saved.CredentialKey); err == nil {
		t.Fatal("logout retained credentials")
	}
	run("auth", "logout", "--all")
	c, _ = contextstore.Load()
	if len(c.Contexts) != 0 || c.Active != "" {
		t.Fatal("all-context logout retained state")
	}
	run("logout")
	if !strings.Contains(out.String(), "Not logged in.") {
		t.Fatalf("empty logout: %s", out.String())
	}
	run("auth", "list")
	if !strings.Contains(out.String(), "No Kapten contexts") {
		t.Fatal("empty list missing")
	}
	if err := a.Run([]string{"kapten", "whoami"}); err == nil || !strings.Contains(err.Error(), "kapten login") {
		t.Fatalf("whoami without login: %v", err)
	}
}

func TestLoginStopsOnDenialAndExpiry(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		set        func(*oauthtest.Server)
	}{
		{"denied", "login denied", func(s *oauthtest.Server) { s.Deny = true }},
		{"expired", "the code expired", func(s *oauthtest.Server) { s.Expire = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keyring.MockInit()
			t.Setenv("KAPTEN_CONFIG_DIR", t.TempDir())
			server := oauthtest.New(t)
			tc.set(server)
			a, _, _, _ := loginApp(t, server, server.URL)
			if err := a.Run([]string{"kapten", "login"}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v", err)
			}
			if c, _ := contextstore.Load(); len(c.Contexts) != 0 {
				t.Fatal("a failed login saved a context")
			}
		})
	}
}

func TestCanOpenBrowser(t *testing.T) {
	for _, tc := range []struct {
		goos string
		env  map[string]string
		want bool
	}{
		{"darwin", nil, true},
		{"windows", nil, true},
		{"darwin", map[string]string{"SSH_CONNECTION": "10.0.0.1 22 10.0.0.2 51000"}, false},
		{"linux", map[string]string{"SSH_TTY": "/dev/pts/0", "DISPLAY": ":0"}, false},
		{"linux", nil, false},
		{"linux", map[string]string{"DISPLAY": ":0"}, true},
		{"freebsd", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true},
	} {
		if got := canOpenBrowser(tc.goos, func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("canOpenBrowser(%s, %v) = %t", tc.goos, tc.env, got)
		}
	}
}

// Server mode: the environment's OAuth application authenticates every
// command with a token kept in memory, ahead of a saved login.
func TestServerModeFromEnvironment(t *testing.T) {
	keyring.MockInit()
	t.Setenv("KAPTEN_CONFIG_DIR", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	server := oauthtest.New(t)
	server.Applications["cli_4c1e"] = "kapten_secret_fixture"
	server.OrganizationName = "Acme"
	api := mockengine.New(t, spec.OpenAPI)
	// A saved login the environment must win over.
	saveAuthFixture(t, server.URL, api.URL, credentialstore.Token{AccessToken: "saved-login", Expiry: time.Now().Add(time.Hour).Unix()})
	t.Setenv("KAPTEN_ISSUER_URL", server.URL)
	t.Setenv("KAPTEN_API_URL", api.URL)
	t.Setenv("KAPTEN_CLIENT_ID", "cli_4c1e")
	t.Setenv("KAPTEN_CLIENT_SECRET", "kapten_secret_fixture")
	var out, stderr bytes.Buffer
	a := New(&out, &stderr)
	for range 2 {
		if err := a.Run([]string{"kapten", "projects", "list", "-ojson"}); err != nil {
			t.Fatalf("%v; %s", err, stderr.String())
		}
	}
	if server.Count("client_credentials") != 1 {
		t.Fatalf("token not reused in memory: %d requests", server.Count("client_credentials"))
	}
	if len(api.Requests()) != 2 || len(api.Errors()) != 0 {
		t.Fatalf("requests=%d errors=%v", len(api.Requests()), api.Errors())
	}
	for _, req := range api.Requests() {
		if !strings.HasPrefix(req.Authorization, "Bearer ") || req.Authorization == "Bearer saved-login" {
			t.Fatalf("authorization = %q", req.Authorization)
		}
	}
	out.Reset()
	if err := a.Run([]string{"kapten", "whoami"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"OAuth application Deploy bot (cli_4c1e)", "Organization Acme", "KAPTEN_CLIENT_ID and KAPTEN_CLIENT_SECRET, token kept in memory"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("whoami lacks %q: %s", want, out.String())
		}
	}
	if data, err := os.ReadFile(contextstore.CredentialsPath()); err == nil && strings.Contains(string(data), "Deploy") {
		t.Fatal("server mode token written to disk")
	}

	// An explicit --context wins over the environment.
	before := len(api.Requests())
	if err := a.Run([]string{"kapten", "--context", "test", "projects", "list", "-ojson"}); err != nil {
		t.Fatal(err)
	}
	if got := api.Requests()[before].Authorization; got != "Bearer saved-login" {
		t.Fatalf("--context authorization = %q", got)
	}
	// Logging in while the environment authenticates would not be used.
	if err := a.Run([]string{"kapten", "login"}); err == nil || !strings.Contains(err.Error(), "unset it to log in") {
		t.Fatalf("login with the environment set: %v", err)
	}

	t.Setenv("KAPTEN_CLIENT_SECRET", "")
	if err := a.Run([]string{"kapten", "projects", "list", "-ojson"}); err == nil || !strings.Contains(err.Error(), "KAPTEN_CLIENT_PRIVATE_KEY") {
		t.Fatalf("KAPTEN_CLIENT_ID alone: %v", err)
	}

	t.Setenv("KAPTEN_CLIENT_SECRET", "kapten_secret_rotated_away")
	err := a.Run([]string{"kapten", "projects", "list", "-ojson"})
	if err == nil || !strings.Contains(err.Error(), "invalid_client") || strings.Contains(err.Error(), "rotated_away") {
		t.Fatalf("wrong secret: %v", err)
	}
}

// With the DPoP scheme, each API request carries its own proof bound to the
// access token.
func TestDPoPSchemeSignsEachRequest(t *testing.T) {
	key, err := dpop.Generate()
	if err != nil {
		t.Fatal(err)
	}
	previous := apiAuthScheme
	apiAuthScheme = "DPoP"
	t.Cleanup(func() { apiAuthScheme = previous })
	req, _ := http.NewRequest(http.MethodGet, "https://api.example/projects?limit=1", nil)
	if err := authorize(req, "access", key); err != nil {
		t.Fatal(err)
	}
	first := req.Header.Get(dpop.Header)
	if req.Header.Get("Authorization") != "DPoP access" || first == "" {
		t.Fatalf("headers = %v", req.Header)
	}
	if err := authorize(req, "access", key); err != nil || req.Header.Get(dpop.Header) == first {
		t.Fatal("proof reused across requests")
	}
	apiAuthScheme = "Bearer"
	bearer, _ := http.NewRequest(http.MethodGet, "https://api.example/projects", nil)
	if err := authorize(bearer, "access", key); err != nil || bearer.Header.Get("Authorization") != "Bearer access" || bearer.Header.Get(dpop.Header) != "" {
		t.Fatalf("bearer headers = %v", bearer.Header)
	}
}

func TestServerModeWithPrivateKeyFile(t *testing.T) {
	keyring.MockInit()
	t.Setenv("KAPTEN_CONFIG_DIR", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public, _ := jwk.Import(private.Public())
	der, _ := x509.MarshalPKCS8PrivateKey(private)
	path := filepath.Join(t.TempDir(), "client.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	server := oauthtest.New(t)
	server.ApplicationKeys["cli_9f2a"] = public
	api := mockengine.New(t, spec.OpenAPI)
	t.Setenv("KAPTEN_ISSUER_URL", server.URL)
	t.Setenv("KAPTEN_API_URL", api.URL)
	t.Setenv("KAPTEN_CLIENT_ID", "cli_9f2a")
	t.Setenv("KAPTEN_CLIENT_PRIVATE_KEY_FILE", path)
	var out, stderr bytes.Buffer
	a := New(&out, &stderr)
	if err := a.Run([]string{"kapten", "projects", "list", "-ojson"}); err != nil {
		t.Fatalf("%v; %s", err, stderr.String())
	}
	if server.Count("client_credentials") != 1 || len(api.Requests()) != 1 {
		t.Fatalf("token requests=%d api requests=%d", server.Count("client_credentials"), len(api.Requests()))
	}
	out.Reset()
	if err := a.Run([]string{"kapten", "whoami"}); err != nil || !strings.Contains(out.String(), "From KAPTEN_CLIENT_ID and its private key") {
		t.Fatalf("whoami: %v %s", err, out.String())
	}
	t.Setenv("KAPTEN_CLIENT_SECRET", "kapten_secret_fixture")
	if err := a.Run([]string{"kapten", "projects", "list", "-ojson"}); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("secret and key: %v", err)
	}
}
