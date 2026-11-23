package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/kapten-io/cli/internal/contextstore"
	"github.com/kapten-io/cli/internal/credentialstore"
	"github.com/kapten-io/cli/internal/oauth/oauthtest"
	"github.com/kapten-io/cli/internal/spec"
	"github.com/kapten-io/cli/internal/mockengine"
	"github.com/rest-sh/restish/v2/plugin"
)

func mcpTestBinary(t *testing.T) string {
	t.Helper()
	name := "restish-mcp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	binary := filepath.Join(t.TempDir(), name)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goBinary += ".exe"
	}
	cmd := exec.CommandContext(ctx, goBinary, "build", "-mod=readonly", "-o", binary, "github.com/rest-sh/restish/v2/cmd/restish-mcp")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build pinned MCP helper: %v\n%s", err, output)
	}
	return binary
}

func mcpMessage(id int, method string, params any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}
}

func runMCPConversation(t *testing.T, helper string, messages []map[string]any, args ...string) []map[string]any {
	t.Helper()
	var input, out, diagnostics bytes.Buffer
	for _, message := range messages {
		if err := json.NewEncoder(&input).Encode(message); err != nil {
			t.Fatal(err)
		}
	}
	a := New(&out, &diagnostics)
	a.In, a.MCPExecutable = &input, helper
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.RunContext(ctx, append([]string{"kapten", "mcp", "serve"}, args...)); err != nil {
		t.Fatalf("MCP conversation: %v\nstderr: %s\nstdout: %s", err, &diagnostics, &out)
	}
	if bytes.Contains(out.Bytes(), []byte("Content-Length:")) || bytes.Contains(out.Bytes(), []byte("owner-only")) {
		t.Fatalf("stdout is not clean newline-delimited MCP: %s", &out)
	}
	var replies []map[string]any
	scanner := bufio.NewScanner(&out)
	scanner.Buffer(make([]byte, 4096), mcpOutputFrameLimit)
	for scanner.Scan() {
		var reply map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &reply); err != nil || reply["jsonrpc"] != "2.0" {
			t.Fatalf("invalid stdio MCP message: %s (%v)", scanner.Bytes(), err)
		}
		replies = append(replies, reply)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(replies) != len(messages) {
		t.Fatalf("replies=%d requests=%d: %+v", len(replies), len(messages), replies)
	}
	if strings.Contains(out.String(), "synthetic-refresh") || strings.Contains(out.String(), "Bearer first") {
		t.Fatal("credentials appeared in MCP stdout")
	}
	return replies
}

func TestMCPServerIntegration(t *testing.T) {
	helper := mcpTestBinary(t)
	t.Run("every exposed operation including writes and DELETE annotations", func(t *testing.T) {
		isolateCommandFilesystem(t)
		api := mockengine.New(t, spec.OpenAPI)
		saveAuthFixture(t, "https://issuer.example", api.URL, credentialstore.Token{AccessToken: "first", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour).Unix()})
		contract, err := newMCPContract(nil)
		if err != nil {
			t.Fatal(err)
		}
		messages := []map[string]any{mcpMessage(1, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "kapten-test", "version": "1"}}), mcpMessage(2, "tools/list", nil)}
		for _, op := range api.Operations {
			if !contract.operations[op.ID] {
				continue
			}
			args := map[string]any{}
			for i, name := range pathArguments(op.Path) {
				args[name] = op.Args[i]
			}
			item := api.Document.Paths.Value(op.Path)
			parameters := append(append(openapi3.Parameters{}, item.Parameters...), item.GetOperation(op.Method).Parameters...)
			for _, ref := range parameters {
				p := ref.Value
				if p.In != "query" {
					continue
				}
				value, ok := op.Query[p.Name]
				if !ok {
					continue
				}
				var v any = value
				if p.Schema != nil && p.Schema.Value != nil {
					switch {
					case p.Schema.Value.Type.Is("boolean"):
						v, err = strconv.ParseBool(value)
					case p.Schema.Value.Type.Is("integer"), p.Schema.Value.Type.Is("number"):
						v, err = strconv.ParseFloat(value, 64)
					case p.Schema.Value.Type.Is("array"):
						v = strings.Split(value, ",")
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				args[p.Name] = v
			}
			for name, value := range op.Headers {
				args[name] = value
			}
			if op.Body != nil {
				var body any
				if err := json.Unmarshal(op.Body, &body); err != nil {
					t.Fatal(err)
				}
				args["body"] = body
			}
			messages = append(messages, mcpMessage(len(messages)+1, "tools/call", map[string]any{"name": op.ID, "arguments": args}))
		}
		messages = append(messages, mcpMessage(len(messages)+1, "tools/call", map[string]any{"name": "createContainerRegistry", "arguments": map[string]any{}}))
		replies := runMCPConversation(t, helper, messages)
		tools := replies[1]["result"].(map[string]any)["tools"].([]any)
		if len(tools) != len(contract.operations) {
			t.Fatalf("tool inventory=%d expected=%d", len(tools), len(contract.operations))
		}
		methods := map[string]int{}
		for _, value := range tools {
			tool := value.(map[string]any)
			id := tool["name"].(string)
			if !contract.operations[id] {
				t.Fatalf("unapproved tool: %+v", tool)
			}
			annotation := tool["annotations"].(map[string]any)
			for _, op := range api.Operations {
				if op.ID != id {
					continue
				}
				methods[op.Method]++
				read := op.Method == http.MethodGet || op.Method == http.MethodHead || id == "queryApplicationRunLogs" || id == "queryJobRunLogs" || id == "queryDatabaseRunLogs" || id == "queryCacheRunLogs"
				if annotation["readOnlyHint"] != read {
					t.Fatalf("incorrect read annotation for %s: %+v", id, annotation)
				}
				if op.Method == http.MethodDelete && annotation["destructiveHint"] != true {
					t.Fatalf("DELETE %s is not marked destructive: %+v", id, annotation)
				}
				if read && annotation["destructiveHint"] != false {
					t.Fatalf("read %s marked destructive: %+v", id, annotation)
				}
			}
		}
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
			if methods[method] == 0 {
				t.Fatalf("no %s tools covered", method)
			}
		}
		for _, id := range []string{"createProject", "updateProject", "deleteProject"} {
			if !contract.operations[id] {
				t.Fatalf("expected resource mutation %s is unavailable", id)
			}
		}
		for i, reply := range replies[2 : len(replies)-1] {
			if reply["error"] != nil || reply["result"].(map[string]any)["isError"] == true {
				t.Fatalf("tool %s failed: %+v", messages[i+2]["params"].(map[string]any)["name"], reply)
			}
		}
		if replies[len(replies)-1]["error"] == nil {
			t.Fatal("CLI-ignored tool was accepted")
		}
		if len(api.Errors()) != 0 || len(api.Requests()) != len(contract.operations) {
			t.Fatalf("requests=%d tools=%d errors=%v", len(api.Requests()), len(contract.operations), api.Errors())
		}
		for _, req := range api.Requests() {
			if req.Authorization != "Bearer first" {
				t.Fatalf("wrong context token for %s", req.OperationID)
			}
		}
		t.Logf("real pinned helper: %d resource tools executed, method inventory=%v; all DELETE tools marked destructive", len(tools), methods)
	})

	t.Run("explicit context and operation subset", func(t *testing.T) {
		isolateCommandFilesystem(t)
		api := mockengine.New(t, spec.OpenAPI)
		store, first := saveAuthFixture(t, "https://issuer.example", api.URL, credentialstore.Token{AccessToken: "first", Expiry: time.Now().Add(time.Hour).Unix()})
		second := first
		second.Name, second.CredentialKey = "second", "second-key"
		if _, err := store.Set(second.CredentialKey, keyed(t, credentialstore.Token{AccessToken: "second", Expiry: time.Now().Add(time.Hour).Unix()})); err != nil {
			t.Fatal(err)
		}
		if err := contextstore.Save(contextstore.Config{Version: 1, Active: first.Name, Contexts: []contextstore.Context{first, second}}); err != nil {
			t.Fatal(err)
		}
		replies := runMCPConversation(t, helper, []map[string]any{mcpMessage(1, "tools/list", nil), mcpMessage(2, "tools/call", map[string]any{"name": "listProjects"}), mcpMessage(3, "tools/call", map[string]any{"name": "deleteProject", "arguments": map[string]any{}})}, "--context=second", "--operations=listProjects")
		tools := replies[0]["result"].(map[string]any)["tools"].([]any)
		if len(tools) != 1 || tools[0].(map[string]any)["name"] != "listProjects" || len(api.Requests()) != 1 || api.Requests()[0].Authorization != "Bearer second" {
			t.Fatalf("subset/context mismatch: tools=%v requests=%v", tools, api.Requests())
		}
		if replies[2]["error"] == nil {
			t.Fatal("operation subset accepted an excluded DELETE")
		}
	})

	t.Run("context remains pinned after active context changes", func(t *testing.T) {
		isolateCommandFilesystem(t)
		fixtures := mockengine.New(t, spec.OpenAPI)
		var response []byte
		for _, op := range fixtures.Operations {
			if op.ID == "listProjects" {
				response = op.Response
			}
		}
		var bearers []string
		var bearerMu sync.Mutex
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			bearerMu.Lock()
			bearers = append(bearers, r.Header.Get("Authorization"))
			firstRequest := len(bearers) == 1
			bearerMu.Unlock()
			if firstRequest {
				c, err := contextstore.Load()
				if err != nil {
					t.Error(err)
				} else {
					c.Active = "second"
					if err := contextstore.Save(c); err != nil {
						t.Error(err)
					}
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(response)
		}))
		defer api.Close()
		store, first := saveAuthFixture(t, "https://issuer.example", api.URL, credentialstore.Token{AccessToken: "first", Expiry: time.Now().Add(time.Hour).Unix()})
		second := first
		second.Name, second.CredentialKey = "second", "second-key"
		if _, err := store.Set(second.CredentialKey, keyed(t, credentialstore.Token{AccessToken: "second"})); err != nil {
			t.Fatal(err)
		}
		if err := contextstore.Save(contextstore.Config{Version: 1, Active: first.Name, Contexts: []contextstore.Context{first, second}}); err != nil {
			t.Fatal(err)
		}
		runMCPConversation(t, helper, []map[string]any{
			mcpMessage(1, "tools/call", map[string]any{"name": "listProjects"}),
			mcpMessage(2, "tools/call", map[string]any{"name": "listProjects"}),
		}, "--operations=listProjects")
		bearerMu.Lock()
		defer bearerMu.Unlock()
		if len(bearers) != 2 || bearers[0] != "Bearer first" || bearers[1] != "Bearer first" {
			t.Fatalf("context changed inside session: %v", bearers)
		}
		if c, err := contextstore.Load(); err != nil || c.Active != "second" {
			t.Fatalf("active context was not changed by the fixture: %v", err)
		}
	})

	t.Run("401 refresh uses Kapten handler", func(t *testing.T) {
		isolateCommandFilesystem(t)
		api := mockengine.New(t, spec.OpenAPI)
		server := oauthtest.New(t)
		store, selected := loggedIn(t, server, api.URL)
		before, _, _ := store.Get(selected.CredentialKey)
		api.RejectBearer(before.AccessToken)
		runMCPConversation(t, helper, []map[string]any{mcpMessage(1, "tools/call", map[string]any{"name": "listProjects"})}, "--operations=listProjects")
		token, _, err := store.Get(selected.CredentialKey)
		refreshes := server.Count("refresh_token")
		if err != nil || token.AccessToken == before.AccessToken || token.RefreshToken == before.RefreshToken || refreshes != 1 || len(api.Requests()) != 2 || len(api.Errors()) != 0 {
			t.Fatalf("refresh mismatch: token refreshed=%t refreshes=%d requests=%d errors=%v", token.AccessToken != before.AccessToken, refreshes, len(api.Requests()), api.Errors())
		}
	})
}

func TestMCPHelpAndValidation(t *testing.T) {
	isolateCommandFilesystem(t)
	for _, args := range [][]string{{"mcp", "--help"}, {"mcp", "serve", "--help"}, {"help", "mcp"}, {"--help", "mcp"}} {
		var out, diagnostics bytes.Buffer
		if err := New(&out, &diagnostics).Run(append([]string{"kapten"}, args...)); err != nil || !strings.Contains(out.String(), "kapten mcp") {
			t.Fatalf("MCP help %v: %v; %s", args, err, &out)
		}
	}
	for _, args := range [][]string{{"mcp", "serve"}, {"mcp", "serve", "--allow-write-tools"}, {"mcp", "serve", "--request-timeout=0"}, {"mcp", "serve", "--max-result-bytes=0"}, {"mcp", "serve", "--operations=createContainerRegistry"}, {"mcp", "serve", "--operations="}, {"mcp", "serve", "other-api"}} {
		var out, diagnostics bytes.Buffer
		if err := New(&out, &diagnostics).Run(append([]string{"kapten"}, args...)); err == nil || out.Len() != 0 {
			t.Fatalf("invalid MCP invocation %v returned %v; stdout=%s", args, err, &out)
		}
	}
	api := mockengine.New(t, spec.OpenAPI)
	saveAuthFixture(t, "https://issuer.example", api.URL, credentialstore.Token{AccessToken: "first"})
	a := New(io.Discard, io.Discard)
	a.MCPExecutable = filepath.Join(t.TempDir(), "missing-helper")
	if err := a.Run([]string{"kapten", "mcp", "serve"}); err == nil || !strings.Contains(err.Error(), "complete Kapten release") || len(api.Requests()) != 0 {
		t.Fatalf("missing helper behavior: %v", err)
	}
}

func TestMCPRequestGuard(t *testing.T) {
	contract, err := newMCPContract([]string{"listProjects"})
	if err != nil {
		t.Fatal(err)
	}
	for _, msg := range []plugin.HTTPRequestMsg{
		{Method: "POST", URI: "kapten/project", Body: map[string]any{"name": "fixture"}},
		{Method: "GET", URI: "https://example.invalid/project"},
		{Method: "GET", URI: "other/project"},
		{Method: "GET", URI: "kapten/project#fragment"},
		{Method: "GET", URI: "kapten/../project"},
		{Method: "GET", URI: "kapten/project", Headers: map[string]string{"Authorization": "synthetic"}},
		{Method: "GET", URI: "kapten/project", Headers: map[string]string{"Cookie": "synthetic"}},
		{Method: "GET", URI: "kapten/project", Filter: ".body"},
		{Method: "GET", URI: "kapten/cluster"},
	} {
		if _, err := contract.validate(context.Background(), msg); err == nil {
			t.Fatalf("request guard accepted %s %s", msg.Method, msg.URI)
		}
	}
	if _, err := contract.validate(context.Background(), plugin.HTTPRequestMsg{Method: "GET", URI: "kapten/project"}); err != nil {
		t.Fatalf("guard rejected valid read: %v", err)
	}
}

func TestMCPFramesIntegration(t *testing.T) {
	input := newMCPLineReader(strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n"))
	frame, err := io.ReadAll(input)
	if err != nil || !bytes.HasPrefix(frame, []byte("Content-Length:")) {
		t.Fatalf("input framing: %q %v", frame, err)
	}
	var out bytes.Buffer
	writer := newMCPFrameWriter(&out)
	for _, value := range frame {
		if _, err := writer.Write([]byte{value}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.finish(); err != nil || !json.Valid(bytes.TrimSpace(out.Bytes())) || out.Bytes()[out.Len()-1] != '\n' {
		t.Fatalf("output framing: %q %v", out.Bytes(), err)
	}
	pipelined := newMCPLineReader(strings.NewReader(strings.Repeat("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n\n", 200)))
	frameSize := len("Content-Length: 40\r\n\r\n{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}")
	if n, err := pipelined.Read(make([]byte, 16*1024)); err != nil || n < 10*frameSize || n%frameSize != 0 {
		t.Fatalf("buffered messages were not coalesced: %d %v", n, err)
	}
	if _, err := newMCPLineReader(strings.NewReader(strings.Repeat("x", mcpInputMessageLimit+1))).Read(make([]byte, 16)); err == nil {
		t.Fatal("oversized input accepted")
	}
	if _, err := newMCPFrameWriter(io.Discard).Write([]byte("Content-Length: 999999999\r\n\r\n")); err == nil {
		t.Fatal("oversized helper output accepted")
	}
}
