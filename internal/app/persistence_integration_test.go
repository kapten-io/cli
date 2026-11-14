package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kapten-io/cli/internal/contextstore"
	"github.com/kapten-io/cli/internal/credentialstore"
	"github.com/kapten-io/cli/internal/spec"
	"github.com/kapten-io/cli/internal/mockengine"
)

func isolateCommandFilesystem(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	for key, value := range map[string]string{"HOME": home, "USERPROFILE": home, "XDG_CACHE_HOME": filepath.Join(home, "cache"), "KAPTEN_CONFIG_DIR": filepath.Join(home, "config")} {
		t.Setenv(key, value)
	}
	return home
}

func seedCommandContexts(t *testing.T, api string) {
	t.Helper()
	contexts := []contextstore.Context{
		{Name: "first", APIURL: api, OrganizationID: "org-one", Subject: "subject-one", CredentialKey: "first-key", Storage: "file"},
		{Name: "second", APIURL: api, OrganizationID: "org-two", Subject: "subject-two", CredentialKey: "second-key", Storage: "file"},
	}
	store := credentialstore.New("kapten", contextstore.CredentialsPath())
	for _, selected := range contexts {
		if _, err := store.Set(selected.CredentialKey, keyed(t, credentialstore.Token{AccessToken: "synthetic-" + selected.Name})); err != nil {
			t.Fatal(err)
		}
	}
	if err := contextstore.Save(contextstore.Config{Version: 1, Active: "first", Contexts: contexts}); err != nil {
		t.Fatal(err)
	}
}

func TestPrivatePersistenceThroughCommands(t *testing.T) {
	home := isolateCommandFilesystem(t)
	fallbackKeyring(t)
	server := mockengine.New(t, spec.OpenAPI)
	seedCommandContexts(t, server.URL)
	var stdout, stderr bytes.Buffer
	cli := New(&stdout, &stderr)
	if err := cli.Run([]string{"kapten", "auth", "use", "second"}); err != nil {
		t.Fatal(err)
	}
	if err := cli.Run([]string{"kapten", "projects", "get", "00000000-0000-4000-8000-000000000001"}); err != nil {
		t.Fatalf("resource read: %v; %s", err, &stderr)
	}
	if len(server.Errors()) != 0 {
		t.Fatal(server.Errors())
	}
	requests := server.Requests()
	if len(requests) != 1 || requests[0].Authorization != "Bearer synthetic-second" {
		t.Fatalf("selected-context request: %+v", requests)
	}
	c, err := contextstore.Load()
	if err != nil || c.Active != "second" {
		t.Fatalf("persisted active context: %+v, %v", c, err)
	}
	var credentials map[string]credentialstore.Token
	data, err := os.ReadFile(contextstore.CredentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &credentials); err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 2 || credentials["first-key"].AccessToken != "synthetic-first" {
		t.Fatal("unselected credentials were lost")
	}
	// The contexts already record the file store, which login reported.
	if strings.Contains(stderr.String(), "warning") {
		t.Fatalf("warned about a known file store: %s", &stderr)
	}
	for _, path := range []string{contextstore.Path(), contextstore.CredentialsPath(), filepath.Join(contextstore.Dir(), "restish.json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("private file permissions %s = %o", path, info.Mode().Perm())
		}
	}
	if err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(entry.Name(), ".kapten-") && strings.HasSuffix(entry.Name(), ".tmp") {
			return fmt.Errorf("temporary replacement leaked: %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedCredentialsFailWithoutOverwriteOrRequest(t *testing.T) {
	for _, contents := range []string{"null", "[]", "not-json"} {
		t.Run(contents, func(t *testing.T) {
			isolateCommandFilesystem(t)
			fallbackKeyring(t)
			server := mockengine.New(t, spec.OpenAPI)
			seedCommandContexts(t, server.URL)
			if err := os.WriteFile(contextstore.CredentialsPath(), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			var out, diagnostics bytes.Buffer
			err := New(&out, &diagnostics).Run([]string{"kapten", "projects", "get", "00000000-0000-4000-8000-000000000001"})
			if err == nil {
				t.Fatal("malformed credentials accepted")
			}
			got, readErr := os.ReadFile(contextstore.CredentialsPath())
			if readErr != nil || string(got) != contents {
				t.Fatalf("damaged credentials overwritten: %q %v", got, readErr)
			}
			if len(server.Requests()) != 0 {
				t.Fatal("request sent despite credential failure")
			}
		})
	}
}

func TestConfigFailuresThroughAuthCommands(t *testing.T) {
	for _, contents := range []string{`{"version":2,"contexts":[]}`, "not-json"} {
		t.Run(contents, func(t *testing.T) {
			isolateCommandFilesystem(t)
			if err := os.MkdirAll(contextstore.Dir(), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(contextstore.Path(), []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := New(&bytes.Buffer{}, &bytes.Buffer{}).Run([]string{"kapten", "auth", "list", "--json"}); err == nil {
				t.Fatal("invalid config accepted")
			}
			got, err := os.ReadFile(contextstore.Path())
			if err != nil || string(got) != contents {
				t.Fatalf("invalid config overwritten: %q %v", got, err)
			}
		})
	}
	t.Run("empty-json-list", func(t *testing.T) {
		isolateCommandFilesystem(t)
		var out bytes.Buffer
		if err := New(&out, &bytes.Buffer{}).Run([]string{"kapten", "auth", "list", "--json"}); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Contexts []contextstore.Context `json:"contexts"`
		}
		if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Contexts == nil || len(result.Contexts) != 0 {
			t.Fatalf("empty JSON list: %s, %v", &out, err)
		}
	})
}

func TestEmbeddedContractCacheThroughCommand(t *testing.T) {
	isolateCommandFilesystem(t)
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cache, "kapten")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(dir, "openapi.yml")
	if err := os.WriteFile(unrelated, []byte("older installation's contract"), 0o600); err != nil {
		t.Fatal(err)
	}
	runHelp := func() {
		t.Helper()
		if err := New(&bytes.Buffer{}, &bytes.Buffer{}).Run([]string{"kapten", "projects", "list", "--help"}); err != nil {
			t.Fatal(err)
		}
	}
	runHelp()
	path := filepath.Join(dir, fmt.Sprintf("openapi-%x.yml", sha256.Sum256(spec.OpenAPI)))
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, spec.OpenAPI) {
		t.Fatal("command did not materialize embedded contract", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	runHelp()
	after, err := os.Stat(path)
	if err != nil || !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("unchanged contract was unnecessarily replaced", err)
	}
	if err := os.WriteFile(path, []byte("stale contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	runHelp()
	data, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(data, spec.OpenAPI) {
		t.Fatal("command did not repair corrupted cache", err)
	}
	data, err = os.ReadFile(unrelated)
	if err != nil || string(data) != "older installation's contract" {
		t.Fatal("unrelated contract overwritten", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := New(&bytes.Buffer{}, &bytes.Buffer{}).Run([]string{"kapten", "projects", "list", "--help"}); err == nil {
		t.Fatal("cache filesystem error was hidden")
	}
}
