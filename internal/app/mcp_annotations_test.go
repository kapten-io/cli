package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/kapten-io/cli/internal/credentialstore"
	"github.com/kapten-io/cli/internal/spec"
	"github.com/kapten-io/cli/internal/mockengine"
)

func TestMCPToolAnnotationsPreserveProtocol(t *testing.T) {
	contract, err := newMCPContract([]string{"listProjects", "createProject", "deleteProject", "queryApplicationRunLogs"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	writer := newMCPFrameWriter(&out)
	writer.annotations = contract.annotations
	// Preserve arbitrary numeric IDs and upstream metadata rather than routing
	// tool results through float64 or replacing unrelated annotations/fields.
	payload := []byte(`{"jsonrpc":"2.0","id":9007199254740993,"result":{"nextCursor":"page-2","tools":[{"name":"deleteProject","inputSchema":{"type":"object"},"annotations":{"title":"Delete project","openWorldHint":false}}]}}`)
	frame := append([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(payload))), payload...)
	for _, b := range frame {
		if _, err := writer.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.finish(); err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil || string(envelope["id"]) != "9007199254740993" {
		t.Fatalf("protocol ID was changed: %s (%v)", &out, err)
	}
	var result struct {
		NextCursor string `json:"nextCursor"`
		Tools      []struct {
			InputSchema map[string]any `json:"inputSchema"`
			Annotations map[string]any `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(envelope["result"], &result); err != nil {
		t.Fatal(err)
	}
	hints := result.Tools[0].Annotations
	if result.NextCursor != "page-2" || result.Tools[0].InputSchema["type"] != "object" || hints["destructiveHint"] != true || hints["readOnlyHint"] != false || hints["title"] != "Delete project" || hints["openWorldHint"] != false {
		t.Fatalf("incorrect enrichment: %s", &out)
	}
	for _, payload := range []string{
		`{"jsonrpc":"2.0","id":"call-1","result":{"content":[{"type":"text","text":"fixture"}]}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"fixture"}}`,
	} {
		got, err := writer.annotateTools([]byte(payload))
		if err != nil || string(got) != payload {
			t.Fatalf("non-inventory response changed: %s (%v)", got, err)
		}
	}
	if _, err := writer.annotateTools([]byte(`{"result":{"tools":[{"name":"createContainerRegistry"}]}}`)); err == nil {
		t.Fatal("unknown helper tool bypassed the inventory guard")
	}
	for id, annotation := range contract.annotations {
		if id == "createProject" && (annotation.ReadOnlyHint || annotation.DestructiveHint != nil) {
			t.Fatal("non-read operation lost conservative MCP defaults")
		}
		if (id == "listProjects" || id == "queryApplicationRunLogs") && (!annotation.ReadOnlyHint || annotation.DestructiveHint == nil || *annotation.DestructiveHint) {
			t.Fatalf("read tool %s has wrong annotations", id)
		}
	}
}

func TestMCPWriteSubset(t *testing.T) {
	helper := mcpTestBinary(t)
	isolateCommandFilesystem(t)
	// Help needs no authentication and must no longer advertise read-only tools.
	var out, diagnostics bytes.Buffer
	if err := New(&out, &diagnostics).Run([]string{"kapten", "mcp", "serve", "--help"}); err != nil || !strings.Contains(out.String(), "including writes") || !strings.Contains(out.String(), "DELETE tools are annotated as destructive") {
		t.Fatalf("write-capable help: %v; %s", err, &out)
	}
	api := mockengine.New(t, spec.OpenAPI)
	saveAuthFixture(t, "https://issuer.example", api.URL, credentialstore.Token{AccessToken: "first"})
	var body any
	for _, op := range api.Operations {
		if op.ID == "createProject" {
			if err := json.Unmarshal(op.Body, &body); err != nil {
				t.Fatal(err)
			}
		}
	}
	replies := runMCPConversation(t, helper, []map[string]any{
		mcpMessage(1, "tools/list", nil),
		mcpMessage(2, "tools/call", map[string]any{"name": "createProject", "arguments": map[string]any{"body": body}}),
		mcpMessage(3, "tools/call", map[string]any{"name": "listProjects"}),
	}, "--operations=createProject")
	tools := replies[0]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "createProject" || replies[1]["error"] != nil || replies[1]["result"].(map[string]any)["isError"] == true || replies[2]["error"] == nil || len(api.Requests()) != 1 || len(api.Errors()) != 0 {
		t.Fatalf("write subset did not hold: replies=%v requests=%v errors=%v", replies, api.Requests(), api.Errors())
	}
}
