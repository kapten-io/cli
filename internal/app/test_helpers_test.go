package app

import (
	"errors"

	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kapten-io/cli/internal/credentialstore"
	"github.com/kapten-io/cli/internal/dpop"
	"github.com/zalando/go-keyring"
)

// Serial integration tests never access the real operating-system keychain.
func TestMain(m *testing.M) {
	keyring.MockInit()
	// os.UserCacheDir ignores XDG_CACHE_HOME on macOS; isolate HOME as well.
	cache, err := os.UserCacheDir()
	if err != nil {
		panic(err)
	}
	if os.Getenv("GOCACHE") == "" {
		_ = os.Setenv("GOCACHE", filepath.Join(cache, "go-build"))
	}
	home, err := os.MkdirTemp("", "kapten-app-integration-home-")
	if err != nil {
		panic(err)
	}
	for key, value := range map[string]string{"HOME": home, "USERPROFILE": home, "XDG_CACHE_HOME": filepath.Join(home, "cache"), "KAPTEN_CONFIG_DIR": filepath.Join(home, "config")} {
		if err := os.Setenv(key, value); err != nil {
			panic(err)
		}
	}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if strings.HasPrefix(key, "RSH_") {
			_ = os.Unsetenv(key)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
func fallbackKeyring(t *testing.T) {
	t.Helper()
	keyring.MockInitWithError(errors.New("synthetic keychain unavailable"))
	t.Cleanup(keyring.MockInit)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// keyed gives saved credentials a DPoP key, as `kapten login` does.
func keyed(t testing.TB, tok credentialstore.Token) credentialstore.Token {
	t.Helper()
	if tok.DPoPKey != "" {
		return tok
	}
	key, err := dpop.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if tok.DPoPKey, err = key.Marshal(); err != nil {
		t.Fatal(err)
	}

	return tok
}
