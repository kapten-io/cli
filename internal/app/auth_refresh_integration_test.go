package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kapten-io/cli/internal/contextstore"
	"github.com/kapten-io/cli/internal/credentialstore"
	"github.com/kapten-io/cli/internal/oauth/oauthtest"
	"github.com/kapten-io/cli/internal/spec"
	"github.com/kapten-io/cli/internal/mockengine"
	"github.com/zalando/go-keyring"
)

func TestResourceHelpAndVersionOrdering(t *testing.T) {
	t.Setenv("KAPTEN_CONFIG_DIR", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, args := range [][]string{
		{"--context=missing", "--help"}, {"projects", "list", "--help"}, {"--version", "--help"}, {"--help", "--version"}, {},
	} {
		var out, stderr bytes.Buffer
		if err := New(&out, &stderr).Run(append([]string{"kapten"}, args...)); err != nil {
			t.Fatalf("%q: %v; %s", args, err, stderr.String())
		}
		if !strings.Contains(out.String(), "Usage:") {
			t.Fatalf("resource help not captured: %q", out.String())
		}
	}
	for _, args := range [][]string{{"--context=missing", "--version"}, {"--version", "auth", "login"}, {"auth", "login", "--version"}} {
		var out bytes.Buffer
		if err := New(&out, &bytes.Buffer{}).Run(append([]string{"kapten"}, args...)); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "kapten dev") {
			t.Fatal(out.String())
		}
	}
}

func saveAuthFixture(t *testing.T, issuer, api string, tok credentialstore.Token) (*credentialstore.Store, contextstore.Context) {
	t.Helper()
	t.Setenv("KAPTEN_CONFIG_DIR", t.TempDir())
	x := contextstore.Context{Name: "test", Issuer: issuer, APIURL: api, CredentialKey: "fixture", Storage: "keychain"}
	if err := contextstore.Save(contextstore.Config{Version: 1, Active: x.Name, Contexts: []contextstore.Context{x}}); err != nil {
		t.Fatal(err)
	}
	store := credentialstore.New("kapten", contextstore.CredentialsPath())
	if _, err := store.Set(x.CredentialKey, keyed(t, tok)); err != nil {
		t.Fatal(err)
	}
	return store, x
}

// loggedIn logs in against server through the CLI and returns the saved
// context, whose tokens server knows.
func loggedIn(t *testing.T, server *oauthtest.Server, api string) (*credentialstore.Store, contextstore.Context) {
	t.Helper()
	t.Setenv("KAPTEN_CONFIG_DIR", t.TempDir())
	a, _, stderr, _ := loginApp(t, server, api)
	if err := a.Run([]string{"kapten", "login", "--name", "test"}); err != nil {
		t.Fatalf("login: %v; %s", err, stderr.String())
	}
	selected, err := selectedContext("test")
	if err != nil {
		t.Fatal(err)
	}

	return credentialstore.New("kapten", contextstore.CredentialsPath()), selected
}

// expire makes the saved access token of selected expire within the margin.
func expire(t *testing.T, store *credentialstore.Store, selected contextstore.Context) credentialstore.Token {
	t.Helper()
	tok, _, err := store.Get(selected.CredentialKey)
	if err != nil {
		t.Fatal(err)
	}
	tok.Expiry = time.Now().Add(10 * time.Second).Unix()
	if _, err := store.Set(selected.CredentialKey, tok); err != nil {
		t.Fatal(err)
	}

	return tok
}

func TestRequestAuthRefreshAndFallbackMetadata(t *testing.T) {
	keyring.MockInit()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	api := mockengine.New(t, spec.OpenAPI)
	server := oauthtest.New(t)
	store, selected := loggedIn(t, server, api.URL)
	before := expire(t, store, selected)
	// The keychain becomes unavailable: the refreshed tokens go to the file.
	fallbackKeyring(t)
	if _, err := store.Set(selected.CredentialKey, before); err != nil {
		t.Fatal(err)
	}
	var warnings bytes.Buffer
	a := New(&bytes.Buffer{}, &warnings)
	if err := a.Run([]string{"kapten", "projects", "list", "-ojson"}); err != nil {
		t.Fatal(err)
	}
	c, err := contextstore.Load()
	if err != nil || c.Contexts[0].Storage != "file" {
		t.Fatalf("metadata not updated: %+v %v", c, err)
	}
	tok, backend, err := store.Get(selected.CredentialKey)
	if err != nil || backend != "file" || tok.RefreshToken == before.RefreshToken || tok.AccessToken == before.AccessToken || tok.DPoPKey != before.DPoPKey {
		t.Fatalf("refresh not persisted: %s %v", backend, err)
	}
	if err := a.Run([]string{"kapten", "projects", "list", "-ojson"}); err != nil {
		t.Fatal(err)
	}
	if server.Count("refresh_token") != 1 {
		t.Fatalf("valid token refreshed: %d", server.Count("refresh_token"))
	}
	if len(api.Errors()) != 0 || len(api.Requests()) != 2 {
		t.Fatalf("mock requests/errors: %d %v", len(api.Requests()), api.Errors())
	}
	for _, req := range api.Requests() {
		if req.Authorization != "Bearer "+tok.AccessToken {
			t.Fatalf("missing fresh bearer: %+v", req)
		}
	}
	// Warned once, when the credentials moved to the file; not on every command.
	if strings.Count(warnings.String(), "warning:") != 1 {
		t.Fatalf("warnings: %s", warnings.String())
	}
}

func TestRestish401ForcesRefresh(t *testing.T) {
	keyring.MockInit()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	api := mockengine.New(t, spec.OpenAPI)
	server := oauthtest.New(t)
	store, selected := loggedIn(t, server, api.URL)
	tok, _, _ := store.Get(selected.CredentialKey)
	api.RejectBearer(tok.AccessToken)
	var out, stderr bytes.Buffer
	if err := New(&out, &stderr).Run([]string{"kapten", "projects", "list", "-ojson"}); err != nil {
		t.Fatalf("%v; %s", err, stderr.String())
	}
	if len(api.Requests()) != 2 || server.Count("refresh_token") != 1 || len(api.Errors()) != 0 {
		t.Fatalf("requests=%d refreshes=%d errors=%v", len(api.Requests()), server.Count("refresh_token"), api.Errors())
	}
	if !strings.Contains(out.String(), `"records"`) {
		t.Fatalf("response missing: %s", out.String())
	}
}

// Rotating refresh tokens are single-use: kapten processes running at the
// same time must refresh once, not each, or auth revokes the grant.
func TestConcurrentRefreshesShareOneRotation(t *testing.T) {
	keyring.MockInit()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	api := mockengine.New(t, spec.OpenAPI)
	server := oauthtest.New(t)
	store, selected := loggedIn(t, server, api.URL)
	expire(t, store, selected)
	errs := make(chan error, 4)
	for range 4 {
		go func() {
			var stderr bytes.Buffer
			err := New(&bytes.Buffer{}, &stderr).Run([]string{"kapten", "projects", "list", "-ojson"})
			if err != nil {
				err = fmt.Errorf("%w; %s", err, stderr.String())
			}
			errs <- err
		}()
	}
	for range 4 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if server.Count("refresh_token") != 1 {
		t.Fatalf("refreshes = %d", server.Count("refresh_token"))
	}
}

func TestRevokedSessionAsksToLogInAgain(t *testing.T) {
	keyring.MockInit()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	api := mockengine.New(t, spec.OpenAPI)
	server := oauthtest.New(t)
	store, selected := loggedIn(t, server, api.URL)
	tok := expire(t, store, selected)
	tok.RefreshToken = "refresh-unknown"
	if _, err := store.Set(selected.CredentialKey, tok); err != nil {
		t.Fatal(err)
	}
	err := New(&bytes.Buffer{}, &bytes.Buffer{}).Run([]string{"kapten", "projects", "list", "-ojson"})
	if err == nil || !strings.Contains(err.Error(), "run `kapten login` again") {
		t.Fatalf("err = %v", err)
	}
	if len(api.Requests()) != 0 {
		t.Fatal("request sent without a valid token")
	}
}

func TestCredentialsWithoutDPoPKeyAskToLogInAgain(t *testing.T) {
	keyring.MockInit()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	api := mockengine.New(t, spec.OpenAPI)
	saveAuthFixture(t, "https://issuer.example", api.URL, credentialstore.Token{AccessToken: "old", DPoPKey: "not-a-key"})
	err := New(&bytes.Buffer{}, &bytes.Buffer{}).Run([]string{"kapten", "projects", "list", "-ojson"})
	if err == nil || !strings.Contains(err.Error(), "predate DPoP") {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthRefreshCancellation(t *testing.T) {
	keyring.MockInit()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	entered := make(chan struct{})
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	defer server.Close()
	defer close(stop)
	saveAuthFixture(t, server.URL, server.URL, credentialstore.Token{AccessToken: "expired", RefreshToken: "refresh", Expiry: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- New(&bytes.Buffer{}, &bytes.Buffer{}).RunContext(ctx, []string{"kapten", "projects", "list", "-ojson"})
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("discovery not entered")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("refresh ignored cancellation")
	}
}

func TestLogoutAllAlwaysCleans(t *testing.T) {
	fallbackKeyring(t)
	store, first := saveAuthFixture(t, "https://issuer.example", "", credentialstore.Token{AccessToken: "access1", RefreshToken: "refresh1"})
	second := first
	second.Name, second.CredentialKey = "second", "second-token"
	third := first
	third.Name, third.CredentialKey, third.Issuer = "third", "third-token", ":invalid"
	for _, x := range []contextstore.Context{second, third} {
		if _, err := store.Set(x.CredentialKey, keyed(t, credentialstore.Token{AccessToken: x.Name})); err != nil {
			t.Fatal(err)
		}
	}
	if err := contextstore.Save(contextstore.Config{Version: 1, Active: first.Name, Contexts: []contextstore.Context{first, second, third}}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := New(&out, &bytes.Buffer{}).Run([]string{"kapten", "auth", "logout", "--all"}); err != nil {
		t.Fatal(err)
	}
	for _, x := range []contextstore.Context{first, second, third} {
		if _, _, err := store.Get(x.CredentialKey); err == nil {
			t.Fatalf("credentials retained: %s", x.Name)
		}
		if !strings.Contains(out.String(), "Logged out of context "+x.Name) {
			t.Fatalf("output: %s", out.String())
		}
	}
	c, err := contextstore.Load()
	if err != nil || len(c.Contexts) != 0 || c.Active != "" {
		t.Fatalf("contexts retained: %+v %v", c, err)
	}
}
