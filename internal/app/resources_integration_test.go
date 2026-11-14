package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kapten-io/cli/internal/contextstore"
	"github.com/kapten-io/cli/internal/credentialstore"
	"github.com/kapten-io/cli/internal/spec"
	"github.com/kapten-io/cli/internal/mockengine"
)

func resourceSetup(t *testing.T, server *mockengine.Server) {
	t.Helper()
	t.Setenv("KAPTEN_CONFIG_DIR", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("KAPTEN_API_URL", server.URL)
	for _, name := range []string{"RSH_OUTPUT_FORMAT", "RSH_FILTER", "RSH_PROFILE", "RSH_SERVER", "RSH_AUTH", "RSH_HEADER", "RSH_QUERY"} {
		t.Setenv(name, "")
	}
	if err := contextstore.Save(contextstore.Config{Version: 1, Active: "contract", Contexts: []contextstore.Context{{Name: "contract", APIURL: server.URL, CredentialKey: "resource-contract"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := credentialstore.New("kapten", contextstore.CredentialsPath()).Set("resource-contract", keyed(t, credentialstore.Token{AccessToken: "resource-contract-token", TokenType: "Bearer"})); err != nil {
		t.Fatal(err)
	}
}

func TestEveryResourceOperationIntegration(t *testing.T) {
	server := mockengine.New(t, spec.OpenAPI)
	resourceSetup(t, server)
	commands := map[string]string{}
	covered := map[string]bool{}
	ignored := 0
	for _, op := range server.Operations {
		if op.Ignored {
			ignored++
			continue
		}
		group := op.Groups[0]
		command := group + " " + op.Command
		if old, ok := commands[command]; ok {
			t.Fatalf("command collision %s: %s and %s", command, old, op.ID)
		}
		commands[command] = op.ID
		t.Run(command, func(t *testing.T) {
			argv := []string{"kapten", group, op.Command}
			argv = append(argv, op.Args...)
			argv = append(argv, op.QueryFlags()...)
			argv = append(argv, "--rsh-no-paginate", "--rsh-no-cache", "--rsh-retry", "0", "--rsh-timeout", "5s", "--rsh-output-format", "json", "--rsh-filter", ".body")
			if op.Body != nil {
				argv = append(argv, string(op.Body))
			}
			start := len(server.Requests())
			var out, stderr bytes.Buffer
			err := New(&out, &stderr).Run(argv)
			if err != nil {
				t.Fatalf("%s (%s): %v\nargv=%q\nstderr=%s\nmock errors=%v", command, op.ID, err, argv, stderr.String(), server.Errors())
			}
			got := server.Requests()[start:]
			if len(got) != 1 {
				t.Fatalf("%s: expected one bounded request, got %v", op.ID, got)
			}
			req := got[0]
			expectedPath := op.Path
			for i, part := range pathArguments(op.Path) {
				expectedPath = strings.Replace(expectedPath, "{"+part+"}", url.PathEscape(op.Args[i]), 1)
			}
			if req.OperationID != op.ID || req.Method != op.Method || (req.Path != expectedPath && req.Path != "/api"+expectedPath) || req.Authorization != "Bearer resource-contract-token" || req.Rejected {
				t.Fatalf("request = %+v; expected %s %s (%s)", req, op.Method, expectedPath, op.ID)
			}
			query, err := url.ParseQuery(req.Query)
			if err != nil {
				t.Fatal(err)
			}
			for key, value := range op.Query {
				if query.Get(key) != value {
					t.Errorf("query %s=%q, want %q", key, query.Get(key), value)
				}
			}
			if op.Body != nil {
				assertResourceJSON(t, req.Body, op.Body)
			} else if len(req.Body) != 0 {
				t.Errorf("unexpected request body %q", req.Body)
			}
			if op.Response != nil {
				assertResourceJSON(t, out.Bytes(), op.Response)
			} else if trimmed := strings.TrimSpace(out.String()); trimmed != "" && trimmed != "null" {
				t.Errorf("bodyless response produced %q", out.String())
			}
			covered[op.ID] = true
		})
	}
	if len(server.Errors()) != 0 {
		t.Errorf("contract errors: %v", server.Errors())
	}
	for _, op := range server.Operations {
		if !op.Ignored && !covered[op.ID] {
			t.Errorf("omitted exposed operation %s", op.ID)
		}
	}
	t.Logf("canonical operations=%d ignored=%d exposed=%d commands=%d covered=%d", len(server.Operations), ignored, len(server.Operations)-ignored, len(commands), len(covered))
}

func pathArguments(path string) []string {
	matches := regexp.MustCompile(`\{([^}]+)\}`).FindAllStringSubmatch(path, -1)
	out := []string{}
	for _, match := range matches {
		out = append(out, match[1])
	}
	return out
}
func assertResourceJSON(t *testing.T, got, want []byte) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("output/request is not parsed JSON: %v: %s", err, got)
	}
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("JSON differs\ngot: %s\nwant: %s", got, want)
	}
}

func TestEveryResourceHelpIntegration(t *testing.T) {
	server := mockengine.New(t, spec.OpenAPI)
	t.Setenv("KAPTEN_CONFIG_DIR", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("KAPTEN_API_URL", server.URL)
	// Deliberately no credentials or context: help must never authenticate.
	groups := map[string][]mockengine.Operation{}
	for _, op := range server.Operations {
		if !op.Ignored {
			groups[op.Groups[0]] = append(groups[op.Groups[0]], op)
		}
	}
	runHelp := func(t *testing.T, args ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		if err := New(&out, &stderr).Run(append([]string{"kapten"}, args...)); err != nil {
			t.Fatalf("help %v: %v; %s", args, err, stderr.String())
		}
		if len(server.Requests()) != 0 {
			t.Fatal("help made network requests")
		}
		return out.String()
	}
	root := runHelp(t, "--help")
	for group, ops := range groups {
		t.Run(group, func(t *testing.T) {
			if !helpListsCommand(root, group) {
				t.Errorf("root help omits %s", group)
			}
			text := runHelp(t, group, "--help")
			for _, op := range ops {
				if !helpListsCommand(text, op.Command) {
					t.Errorf("group help omits %s (%s)", op.Command, op.ID)
				}
				t.Run(op.Command, func(t *testing.T) {
					text := runHelp(t, group, op.Command, "--help")
					if !strings.Contains(text, fmt.Sprintf("kapten %s %s", group, op.Command)) {
						t.Errorf("unexpected help surface: %s", text)
					}
					for _, name := range pathArguments(op.Path) {
						if !strings.Contains(text, strings.ReplaceAll(name, "_", "-")) {
							t.Errorf("help omits argument %s", name)
						}
					}
				})
			}
		})
	}
	t.Logf("resource help groups=%d; zero credential/network calls", len(groups))
}
func helpListsCommand(text, command string) bool {
	return regexp.MustCompile(`(?m)^\s+` + regexp.QuoteMeta(command) + `(?:\s|$)`).MatchString(text)
}

func TestResourceInvalidBodyIntegration(t *testing.T) {
	server := mockengine.New(t, spec.OpenAPI)
	resourceSetup(t, server)
	var selected mockengine.Operation
	for _, op := range server.Operations {
		if op.Ignored || op.Body == nil {
			continue
		}
		// Select a body whose canonical schema rejects an empty object,
		// without naming an operation or hardcoding a request fixture.
		canonical := server.Document.Paths.Value(op.Path).GetOperation(op.Method)
		if err := canonical.RequestBody.Value.Content["application/json"].Schema.Value.VisitJSON(map[string]any{}); err != nil {
			selected = op
			break
		}
	}
	if selected.ID == "" {
		t.Fatal("no exposed body schema rejects an empty object")
	}
	argv := append([]string{"kapten", selected.Groups[0], selected.Command}, selected.Args...)
	keys := []string{}
	for key := range selected.Query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		flag := strings.Trim(regexp.MustCompile(`[^a-zA-Z0-9]+`).ReplaceAllString(key, "-"), "-")
		argv = append(argv, "--"+flag+"="+selected.Query[key])
	}
	argv = append(argv, "--rsh-no-paginate", "--rsh-no-cache", "--rsh-retry", "0", "--rsh-timeout", "5s", "--rsh-output-format", "json", "{}")
	var out, stderr bytes.Buffer
	if err := New(&out, &stderr).Run(argv); err == nil {
		t.Fatalf("invalid body accepted: %s", out.String())
	}
	requests := server.Requests()
	if len(requests) != 1 || requests[0].OperationID != selected.ID || requests[0].Authorization != "Bearer resource-contract-token" {
		t.Fatalf("invalid request was not sent once to canonical operation: %v", requests)
	}
	if len(server.Errors()) != 1 {
		t.Fatalf("expected one request validation failure: %v", server.Errors())
	}
	if !strings.Contains(server.Errors()[0].Error(), "request body") {
		t.Fatalf("unexpected failure: %v", server.Errors())
	}
}
