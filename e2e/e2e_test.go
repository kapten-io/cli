// Package e2e runs the real kapten binary, as a separate process with its own
// HOME and configuration, against the mock Kapten of package mockserver: a
// fake auth service and the Engine API generated from the embedded OpenAPI
// document. These tests check what a user sees: output, exit codes, saved
// state and the requests that reach the services.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kapten-io/cli/internal/mockauth"
	"github.com/kapten-io/cli/internal/mockengine"
	"github.com/kapten-io/cli/internal/mockserver"
	"github.com/lestrrat-go/jwx/v3/jwk"
)

// binary is the kapten executable built once for the package.
var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kapten-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "kapten")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	build := exec.CommandContext(ctx, "go", "build", "-o", binary,
		"-ldflags", "-X main.version=1.2.3-e2e -X main.commit=e2e -X main.date=2026-01-01", "../cmd/kapten")
	output, err := build.CombinedOutput()
	cancel()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build kapten: %v\n%s", err, output)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// kapten is a mock Kapten and a user of the CLI with an empty HOME.
type kapten struct {
	t      *testing.T
	mock   *mockserver.Server
	home   string
	extra  []string
	server *httptest.Server
}

func newKapten(t *testing.T) *kapten {
	t.Helper()
	k := &kapten{t: t, home: t.TempDir()}
	k.mock, k.server = startMock(t)
	return k
}

func startMock(t *testing.T) (*mockserver.Server, *httptest.Server) {
	t.Helper()
	mock, err := mockserver.New(mockserver.Options{ManualApproval: true})
	if err != nil {
		t.Fatal(err)
	}
	mock.Auth.Errorf = t.Errorf
	server := httptest.NewServer(mock)
	t.Cleanup(server.Close)
	if err := mock.Listen(server.URL); err != nil {
		t.Fatal(err)
	}
	return mock, server
}

// env is the whole environment of the CLI: nothing leaks in from the
// developer's shell, such as a KAPTEN_CLIENT_ID or a real keychain.
func (k *kapten) env() []string {
	env := []string{
		"HOME=" + k.home,
		"USERPROFILE=" + k.home,
		"XDG_CONFIG_HOME=" + filepath.Join(k.home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(k.home, ".cache"),
		"KAPTEN_CONFIG_DIR=" + filepath.Join(k.home, ".config", "kapten"),
		"KAPTEN_CREDENTIALS_STORE=file",
		"KAPTEN_ISSUER_URL=" + k.mock.IssuerURL(),
		"KAPTEN_API_URL=" + k.mock.APIURL(),
		"PATH=" + os.Getenv("PATH"),
		"NO_COLOR=1",
	}
	if runtime.GOOS == "windows" {
		env = append(env, "SystemRoot="+os.Getenv("SystemRoot"), "APPDATA="+filepath.Join(k.home, "AppData"))
	}
	return append(env, k.extra...)
}

type result struct {
	stdout, stderr string
	code           int
}

func (r result) String() string {
	return fmt.Sprintf("exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
}

func (k *kapten) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env = k.env()
	cmd.Dir = k.home
	return cmd
}

// run runs kapten to completion.
func (k *kapten) run(args ...string) result {
	k.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := k.command(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := result{stdout: stdout.String(), stderr: stderr.String()}
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit):
		r.code = exit.ExitCode()
	case err != nil:
		k.t.Fatalf("run kapten %v: %v", args, err)
	}
	return r
}

// ok runs kapten and fails the test unless it succeeds.
func (k *kapten) ok(args ...string) result {
	k.t.Helper()
	r := k.run(args...)
	if r.code != 0 {
		k.t.Fatalf("kapten %s:\n%s", strings.Join(args, " "), r)
	}
	return r
}

// fails runs kapten and fails the test unless it exits with 1 and says want.
func (k *kapten) fails(want string, args ...string) result {
	k.t.Helper()
	r := k.run(args...)
	if r.code != 1 || !strings.Contains(r.stderr, want) {
		k.t.Fatalf("kapten %s: want exit 1 with %q on stderr, got\n%s", strings.Join(args, " "), want, r)
	}
	return r
}

var userCode = regexp.MustCompile(`code ([A-Z0-9]{4}-[A-Z0-9]{4})`)

// login runs kapten login and approves the code it prints on the device
// page, as a user would in a browser.
func (k *kapten) login(args ...string) result {
	k.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := k.command(ctx, append([]string{"login", "--no-browser"}, args...)...)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		k.t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		k.t.Fatal(err)
	}
	var stdout strings.Builder
	scanner := bufio.NewScanner(stdoutPipe)
	approved := false
	for scanner.Scan() {
		line := scanner.Text()
		stdout.WriteString(line + "\n")
		if m := userCode.FindStringSubmatch(line); m != nil && !approved {
			approved = true
			k.approve(m[1])
		}
	}
	err = cmd.Wait()
	r := result{stdout: stdout.String(), stderr: stderr.String()}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		r.code = exit.ExitCode()
	} else if err != nil {
		k.t.Fatal(err)
	}
	if !approved {
		k.t.Fatalf("login printed no code:\n%s", r)
	}
	return r
}

// approve submits the device page's form, as the user's browser would.
func (k *kapten) approve(code string) {
	k.t.Helper()
	res, err := http.PostForm(k.mock.IssuerURL()+"/device", url.Values{"user_code": {code}})
	if err != nil {
		k.t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		k.t.Fatalf("approve %s: HTTP %d %s", code, res.StatusCode, body)
	}
}

// requests are the Engine requests the mock received since index from.
func (k *kapten) requests(from int) []mockengine.Request {
	return k.mock.Engine.Requests()[from:]
}

func decode[T any](t *testing.T, data string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(data), &v); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	return v
}

func TestVersionAndHelp(t *testing.T) {
	k := newKapten(t)
	if r := k.ok("--version"); r.stdout != "kapten 1.2.3-e2e (commit e2e, built 2026-01-01)\n" {
		t.Fatalf("version: %q", r.stdout)
	}
	help := k.ok("--help").stdout
	for _, want := range []string{"Get started:", "login ", "whoami ", "Resources:", "projects ", "Create and manage projects", "mcp "} {
		if !strings.Contains(help, want) {
			t.Errorf("root help lacks %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, "Operations tagged") {
		t.Errorf("root help has a generic group description:\n%s", help)
	}
	if !strings.Contains(k.ok("login", "--help").stdout, "--issuer-url") {
		t.Error("login help lacks --issuer-url")
	}
	if len(k.requests(0)) != 0 {
		t.Fatal("help reached the API")
	}
}

func TestLoginWhoamiLogout(t *testing.T) {
	k := newKapten(t)
	k.fails("no active Kapten context; run `kapten login` first", "projects", "list")

	r := k.login()
	if r.code != 0 {
		t.Fatalf("login:\n%s", r)
	}
	for _, want := range []string{"/auth/device?user_code=WDJB-MJHT", "Waiting for approval", "Logged in to organization Acme", "dev@kapten.local", "context acme"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("login output lacks %q:\n%s", want, r)
		}
	}
	if r.stderr != "" {
		t.Errorf("login warned with an explicit file store:\n%s", r.stderr)
	}

	whoami := k.ok("whoami").stdout
	for _, want := range []string{"User dev@kapten.local", "Organization Acme", "From context acme", "Issuer " + k.mock.IssuerURL(), "API " + k.mock.APIURL()} {
		if !strings.Contains(whoami, want) {
			t.Errorf("whoami lacks %q:\n%s", want, whoami)
		}
	}
	who := decode[map[string]string](t, k.ok("whoami", "--json").stdout)
	if who["source"] != "login" || who["context"] != "acme" || who["email"] != "dev@kapten.local" || who["organization_name"] != "Acme" || who["api_url"] != k.mock.APIURL() || who["storage"] != "file" {
		t.Errorf("whoami --json: %v", who)
	}
	contexts := decode[struct {
		Active   string `json:"active"`
		Contexts []struct {
			Name   string `json:"name"`
			Issuer string `json:"issuer"`
			APIURL string `json:"api_url"`
		} `json:"contexts"`
	}](t, k.ok("auth", "list", "--json").stdout)
	if contexts.Active != "acme" || len(contexts.Contexts) != 1 || contexts.Contexts[0].Issuer != k.mock.IssuerURL() || contexts.Contexts[0].APIURL != k.mock.APIURL() {
		t.Errorf("auth list: %+v", contexts)
	}

	// The credentials, DPoP key included, are only readable by the user.
	credentials := filepath.Join(k.home, ".config", "kapten", "credentials.json")
	info, err := os.Stat(credentials)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("credentials mode %v", info.Mode().Perm())
	}

	// Logging in again as the same user refreshes the same context.
	if r := k.login(); r.code != 0 || !strings.Contains(r.stdout, "context acme") {
		t.Fatalf("second login:\n%s", r)
	}
	if n := len(decode[map[string]any](t, k.ok("auth", "list", "--json").stdout)["contexts"].([]any)); n != 1 {
		t.Errorf("second login added a context: %d", n)
	}

	k.ok("logout")
	k.fails("no active Kapten context", "whoami")
	data, err := os.ReadFile(credentials)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "access_token") {
		t.Errorf("logout left credentials: %s", data)
	}
}

func TestLoginDeniedOrExpired(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  func(*mockserver.Server)
		want string
	}{
		{"denied", func(m *mockserver.Server) { m.Auth.Set(func(s *mockauth.Server) { s.Deny = true }) }, "login denied in the browser"},
		{"expired", func(m *mockserver.Server) { m.Auth.Set(func(s *mockauth.Server) { s.Expire = true }) }, "the code expired before it was approved; run `kapten login` again"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := newKapten(t)
			tc.set(k.mock)
			r := k.login()
			if r.code != 1 || !strings.Contains(r.stderr, tc.want) {
				t.Fatalf("want %q:\n%s", tc.want, r)
			}
			k.fails("no active Kapten context", "whoami")
		})
	}
}

func TestLoginToAnotherKaptenWithFlags(t *testing.T) {
	k := newKapten(t)
	if r := k.login(); r.code != 0 {
		t.Fatalf("login:\n%s", r)
	}
	// A second Kapten, such as staging, named with flags: they win over
	// KAPTEN_ISSUER_URL and KAPTEN_API_URL, and are saved in the context.
	staging, _ := startMock(t)
	first := k.mock
	k.mock = staging // the code is approved on staging's device page
	r := k.login("--name", "staging", "--issuer-url", staging.IssuerURL(), "--api-url", staging.APIURL())
	k.mock = first
	if r.code != 0 {
		t.Fatalf("staging login:\n%s", r)
	}
	from := len(staging.Engine.Requests())
	k.ok("projects", "list")
	if len(staging.Engine.Requests()) != from+1 {
		t.Fatal("the new login is not the active context")
	}
	before := len(k.requests(0))
	k.ok("--context", "acme", "projects", "list")
	if len(k.requests(0)) != before+1 || len(staging.Engine.Requests()) != from+1 {
		t.Fatal("--context did not select the first Kapten")
	}
	k.ok("auth", "use", "acme")
	k.ok("projects", "list")
	if len(k.requests(0)) != before+2 {
		t.Fatal("auth use did not switch the active context")
	}
	k.fails(`Kapten context "nope" was not found`, "--context", "nope", "projects", "list")
	k.fails("invalid API URL", "login", "--no-browser", "--api-url", "ftp://example.com")
}

func TestEveryResourceCommand(t *testing.T) {
	k := newKapten(t)
	if r := k.login(); r.code != 0 {
		t.Fatalf("login:\n%s", r)
	}
	exposed := 0
	for _, op := range k.mock.Engine.Operations {
		if op.Ignored {
			continue
		}
		exposed++
		args := append([]string{op.Groups[0], op.Command}, op.Args...)
		args = append(args, op.QueryFlags()...)
		args = append(args, "-o", "json")
		if op.Body != nil {
			args = append(args, string(op.Body))
		}
		from := len(k.requests(0))
		r := k.run(args...)
		if r.code != 0 {
			t.Errorf("kapten %s (%s):\n%s", strings.Join(args, " "), op.ID, r)
			continue
		}
		got := k.requests(from)
		if len(got) != 1 || got[0].OperationID != op.ID || got[0].Rejected {
			t.Errorf("kapten %s: requests %+v", strings.Join(args[:2], " "), got)
			continue
		}
		if !strings.HasPrefix(got[0].Authorization, "Bearer ") {
			t.Errorf("%s: no bearer token", op.ID)
		}
		if op.Response != nil && !json.Valid([]byte(r.stdout)) {
			t.Errorf("%s: -o json printed %q", op.ID, r.stdout)
		}
	}
	if errs := k.mock.Engine.Errors(); len(errs) != 0 {
		t.Errorf("requests outside the contract: %v", errs)
	}
	if exposed < 50 {
		t.Fatalf("only %d exposed operations", exposed)
	}
}

func TestDefaultOutputIsATable(t *testing.T) {
	k := newKapten(t)
	if r := k.login(); r.code != 0 {
		t.Fatalf("login:\n%s", r)
	}
	r := k.ok("projects", "list")
	header := strings.SplitN(r.stdout, "\n", 3)
	if len(header) < 2 || !strings.Contains(header[1], "name") || !strings.Contains(header[1], "id") || !strings.Contains(header[1], "description") {
		t.Fatalf("projects list is not a table:\n%s", r)
	}
	// -o json prints the page as the API returns it, pagination included.
	page := decode[struct {
		Records    []map[string]any `json:"records"`
		Pagination map[string]any   `json:"pagination"`
	}](t, k.ok("projects", "list", "-o", "json").stdout)
	if len(page.Records) == 0 || page.Records[0]["id"] == nil || page.Pagination == nil {
		t.Fatalf("projects list -o json: %+v", page)
	}
}

func TestExpiredAccessTokenIsRefreshed(t *testing.T) {
	k := newKapten(t)
	if r := k.login(); r.code != 0 {
		t.Fatalf("login:\n%s", r)
	}
	// The saved token looks fresh to the CLI, but the Engine refuses it:
	// the CLI refreshes once and retries.
	k.mock.Auth.ExpireAccessTokens()
	from := len(k.requests(0))
	k.ok("projects", "list")
	got := k.requests(from)
	if len(got) != 2 || got[0].Authorization == got[1].Authorization {
		t.Fatalf("want a refused request then a retry with a new token: %+v", got)
	}
	if n := k.mock.Auth.Count("refresh_token"); n != 1 {
		t.Fatalf("refreshes: %d", n)
	}
	// The rotated refresh token was saved: the next command needs none.
	k.ok("projects", "list")
	if n := k.mock.Auth.Count("refresh_token"); n != 1 {
		t.Fatalf("refreshes after the retry: %d", n)
	}
}

func TestEndedSessionAsksToLogInAgain(t *testing.T) {
	k := newKapten(t)
	if r := k.login(); r.code != 0 {
		t.Fatalf("login:\n%s", r)
	}
	k.mock.Auth.RevokeSessions()
	k.fails("run `kapten login` again", "projects", "list")
	k.fails("run `kapten login` again", "whoami")
}

func TestServerMode(t *testing.T) {
	t.Run("client secret", func(t *testing.T) {
		k := newKapten(t)
		k.extra = []string{"KAPTEN_CLIENT_ID=" + mockserver.ApplicationID, "KAPTEN_CLIENT_SECRET=" + mockserver.ApplicationSecret}
		k.ok("projects", "list")
		who := decode[map[string]string](t, k.ok("whoami", "--json").stdout)
		if who["source"] != "environment" || who["client_id"] != mockserver.ApplicationID || who["organization_name"] != "Acme" {
			t.Errorf("whoami --json: %v", who)
		}
		k.fails("KAPTEN_CLIENT_ID is set", "login", "--no-browser")
		if _, err := os.Stat(filepath.Join(k.home, ".config", "kapten", "credentials.json")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("server mode saved credentials: %v", err)
		}
	})
	t.Run("wrong secret", func(t *testing.T) {
		k := newKapten(t)
		k.extra = []string{"KAPTEN_CLIENT_ID=" + mockserver.ApplicationID, "KAPTEN_CLIENT_SECRET=wrong"}
		k.fails("invalid_client", "projects", "list")
		if len(k.requests(0)) != 0 {
			t.Fatal("reached the API without a token")
		}
	})
	t.Run("missing secret", func(t *testing.T) {
		k := newKapten(t)
		k.extra = []string{"KAPTEN_CLIENT_ID=" + mockserver.ApplicationID}
		k.fails("KAPTEN_CLIENT_ID is set without KAPTEN_CLIENT_SECRET", "projects", "list")
	})
	t.Run("private key", func(t *testing.T) {
		k := newKapten(t)
		private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		public, err := jwk.Import(private.Public())
		if err != nil {
			t.Fatal(err)
		}
		const id = "cli_6d6f636b-0000-4000-8000-0000000000aa"
		k.mock.Auth.Set(func(s *mockauth.Server) { s.ApplicationKeys[id] = public })
		der, err := x509.MarshalPKCS8PrivateKey(private)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "deploy.pem")
		if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
		k.extra = []string{"KAPTEN_CLIENT_ID=" + id, "KAPTEN_CLIENT_PRIVATE_KEY_FILE=" + path}
		k.ok("projects", "list")
		if !strings.Contains(k.ok("whoami").stdout, "OAuth application") {
			t.Error("whoami does not name the application")
		}
	})
	t.Run("an explicit context wins", func(t *testing.T) {
		k := newKapten(t)
		if r := k.login(); r.code != 0 {
			t.Fatalf("login:\n%s", r)
		}
		k.extra = []string{"KAPTEN_CLIENT_ID=" + mockserver.ApplicationID, "KAPTEN_CLIENT_SECRET=" + mockserver.ApplicationSecret}
		if who := decode[map[string]string](t, k.ok("whoami", "--json").stdout); who["source"] != "environment" {
			t.Errorf("environment does not win over the active login: %v", who)
		}
		if who := decode[map[string]string](t, k.ok("--context", "acme", "whoami", "--json").stdout); who["source"] != "login" {
			t.Errorf("--context does not win over the environment: %v", who)
		}
	})
}

func TestAPIErrors(t *testing.T) {
	k := newKapten(t)
	if r := k.login(); r.code != 0 {
		t.Fatalf("login:\n%s", r)
	}
	k.mock.Engine.RespondWithStatus(t, "getProject", http.StatusNotFound)
	r := k.run("projects", "get", "00000000-0000-4000-8000-000000000001")
	if r.code != 1 || r.stdout != "" || !strings.Contains(r.stderr, "(HTTP 404 Not Found)") {
		t.Fatalf("a 404 must exit 1 with the error on stderr only:\n%s", r)
	}
	r = k.run("projects", "get", "00000000-0000-4000-8000-000000000001", "-o", "json")
	if r.code != 1 || !json.Valid([]byte(r.stdout)) {
		t.Fatalf("-o json keeps the error document on stdout for scripts:\n%s", r)
	}
	k.fails("", "projects", "get", "not-a-uuid")
}

func TestUnreachableServices(t *testing.T) {
	k := newKapten(t)
	if r := k.login(); r.code != 0 {
		t.Fatalf("login:\n%s", r)
	}
	k.server.Close()
	r := k.run("projects", "list", "--rsh-retry", "0")
	if r.code != 1 || r.stderr == "" {
		t.Fatalf("unreachable API:\n%s", r)
	}
	host := strings.TrimPrefix(k.server.URL, "http://")
	k.fails("could not reach "+host, "login", "--no-browser")
}
