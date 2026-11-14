package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/kapten-io/cli/internal/spec"
	"github.com/kapten-io/cli/internal/mockengine"
)

func TestAPIErrorOutputIntegration(t *testing.T) {
	for _, status := range []int{400, 403, 404, 409, 500} {
		for _, format := range []string{"human", "json"} {
			t.Run(fmt.Sprintf("%d/%s", status, format), func(t *testing.T) {
				isolateCommandFilesystem(t)
				server := mockengine.New(t, spec.OpenAPI)
				resourceSetup(t, server)
				expected := server.RespondWithStatus(t, "getProject", status)
				args := []string{"kapten", "projects", "get", "00000000-0000-4000-8000-000000000001", "--rsh-no-cache", "--rsh-retry=0", "--rsh-timeout=2s"}
				if format == "json" {
					args = append(args, "-ojson")
				}
				var out, diagnostics bytes.Buffer
				err := New(&out, &diagnostics).Run(args)
				if err == nil {
					t.Fatal("HTTP error returned command success")
				}
				if format == "human" {
					if !IsRenderedAPIError(err) {
						t.Fatalf("human error was not classified as already rendered: %v", err)
					}
					var body map[string]any
					if err := json.Unmarshal(expected, &body); err != nil {
						t.Fatal(err)
					}
					message, _ := body["message"].(string)
					if message == "" {
						t.Fatal("canonical error schema has no message")
					}
					want := fmt.Sprintf("Error: %s (HTTP %d %s)\n", message, status, http.StatusText(status))
					if status == http.StatusUnauthorized {
						want += "Your session is no longer valid: run `kapten login` again.\n"
					}
					// Errors go to stderr: stdout only ever holds results.
					if diagnostics.String() != want || out.Len() != 0 {
						t.Fatalf("human error stderr=%q want=%q; stdout=%q", diagnostics.String(), want, out.String())
					}
				} else {
					if IsRenderedAPIError(err) {
						t.Fatal("JSON output suppressed entrypoint error classification")
					}
					assertResourceJSON(t, out.Bytes(), expected)
				}
				if len(server.Errors()) != 0 || len(server.Requests()) != 1 {
					t.Fatalf("error fixture failed contract or retried: requests=%v errors=%v", server.Requests(), server.Errors())
				}
				if strings.Contains(out.String(), "resource-contract-token") || strings.Contains(diagnostics.String(), "resource-contract-token") {
					t.Fatal("credentials leaked to error output")
				}
			})
		}
	}
}

func TestDefaultResourceOutputIntegration(t *testing.T) {
	isolateCommandFilesystem(t)
	server := mockengine.New(t, spec.OpenAPI)
	resourceSetup(t, server)
	var out, diagnostics bytes.Buffer
	cli := New(&out, &diagnostics)
	if err := cli.Run([]string{"kapten", "projects", "get", "00000000-0000-4000-8000-000000000001"}); err != nil {
		t.Fatalf("default JSON body output: %v; %s", err, &diagnostics)
	}
	var body map[string]any
	if err := json.Unmarshal(out.Bytes(), &body); err != nil || body["id"] == nil {
		t.Fatalf("default output: %s %v", &out, err)
	}
	out.Reset()
	diagnostics.Reset()
	if err := cli.Run([]string{"kapten", "projects", "list", "--rsh-no-paginate"}); err != nil {
		t.Fatalf("default table output: %v; %s", err, &diagnostics)
	}
	if !strings.Contains(strings.ToLower(out.String()), "name") || strings.Contains(out.String(), `"records"`) {
		t.Fatalf("default list did not render records table: %s", &out)
	}
	if len(server.Errors()) != 0 || len(server.Requests()) != 2 {
		t.Fatalf("default rendering requests=%v errors=%v", server.Requests(), server.Errors())
	}
}
