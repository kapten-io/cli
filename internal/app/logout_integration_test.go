package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/kapten-io/cli/internal/contextstore"
	"github.com/kapten-io/cli/internal/credentialstore"
	"github.com/kapten-io/cli/internal/oauth/oauthtest"
	"github.com/kapten-io/cli/internal/spec"
	"github.com/kapten-io/cli/internal/mockengine"
	"github.com/zalando/go-keyring"
)

func TestLogoutNamedAndDefault(t *testing.T) {
	for _, name := range []string{"", "second"} {
		t.Run("target="+name, func(t *testing.T) {
			keyring.MockInit()
			store, first := saveAuthFixture(t, "https://issuer.example", "https://issuer.example", credentialstore.Token{AccessToken: "first-access", RefreshToken: "first-refresh"})
			second := first
			second.Name, second.CredentialKey = "second", "second-key"
			if _, err := store.Set(second.CredentialKey, keyed(t, credentialstore.Token{AccessToken: "second-access"})); err != nil {
				t.Fatal(err)
			}
			if err := contextstore.Save(contextstore.Config{Version: 1, Active: first.Name, Contexts: []contextstore.Context{first, second}}); err != nil {
				t.Fatal(err)
			}
			args := []string{"kapten", "logout"}
			if name != "" {
				args = append(args, name)
			}
			var warnings bytes.Buffer
			if err := New(&bytes.Buffer{}, &warnings).Run(args); err != nil {
				t.Fatal(err)
			}
			removed, wantRemaining := first, second
			if name != "" {
				removed, wantRemaining = second, first
			}
			c, err := contextstore.Load()
			if err != nil || len(c.Contexts) != 1 || c.Contexts[0].Name != wantRemaining.Name || c.Active != wantRemaining.Name {
				t.Fatalf("remaining: %+v %v", c, err)
			}
			if _, _, err := store.Get(removed.CredentialKey); err == nil {
				t.Fatal("kept the logged out context's credentials")
			}
			if _, _, err := store.Get(wantRemaining.CredentialKey); err != nil {
				t.Fatal("removed another context's credentials")
			}
			if warnings.Len() != 0 {
				t.Fatalf("unexpected warning: %s", warnings.String())
			}
		})
	}
}

func TestRefreshBackendTransitionWarnsOnce(t *testing.T) {
	keyring.MockInit()
	t.Cleanup(keyring.MockInit)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	api := mockengine.New(t, spec.OpenAPI)
	server := oauthtest.New(t)
	store, selected := loggedIn(t, server, api.URL)
	expire(t, store, selected)
	// Lookup succeeded in keychain; the refresh write must fall back.
	server.Set(func(s *oauthtest.Server) {
		s.OnToken = func() { keyring.MockInitWithError(errors.New("synthetic write failure")) }
	})
	var warnings bytes.Buffer
	a := New(&bytes.Buffer{}, &warnings)
	if err := a.Run([]string{"kapten", "projects", "list", "-ojson"}); err != nil {
		t.Fatal(err)
	}
	c, err := contextstore.Load()
	if err != nil || c.Contexts[0].Storage != "file" {
		t.Fatalf("refresh backend not persisted: %+v %v", c, err)
	}
	if strings.Count(warnings.String(), "owner-only file") != 1 {
		t.Fatalf("missing fallback warning: %s", warnings.String())
	}
	warnings.Reset()
	for i := 0; i < 2; i++ {
		if err := a.Run([]string{"kapten", "projects", "list", "-ojson"}); err != nil {
			t.Fatal(err)
		}
	}
	// The context records the file store: later commands do not warn again.
	if warnings.Len() != 0 {
		t.Fatalf("warned again after the transition: %s", warnings.String())
	}
	if len(api.Errors()) != 0 || len(api.Requests()) != 3 {
		t.Fatalf("API mock requests/errors: %d %v", len(api.Requests()), api.Errors())
	}
}
