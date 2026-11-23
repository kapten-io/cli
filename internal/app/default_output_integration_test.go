package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kapten-io/cli/internal/spec"
	"github.com/kapten-io/cli/internal/mockengine"
)

// Explicit JSON coverage is paired with each command's actual default formatter.
func TestEveryResourceDefaultOutputIntegration(t *testing.T) {
	server := mockengine.New(t, spec.OpenAPI)
	resourceSetup(t, server)
	count := 0
	for _, operation := range server.Operations {
		if operation.Ignored {
			continue
		}
		t.Run(operation.Groups[0]+" "+operation.Command, func(t *testing.T) {
			args := append([]string{"kapten", operation.Groups[0], operation.Command}, operation.Args...)
			args = append(args, operation.QueryFlags()...)
			args = append(args, "--rsh-no-paginate", "--rsh-no-cache", "--rsh-retry=0", "--rsh-timeout=5s")
			if operation.Body != nil {
				args = append(args, string(operation.Body))
			}
			before := len(server.Requests())
			var out, diagnostics bytes.Buffer
			if err := New(&out, &diagnostics).Run(args); err != nil {
				t.Fatalf("default output failed: %v; %s", err, &diagnostics)
			}
			if len(server.Requests()) != before+1 || server.Requests()[before].OperationID != operation.ID {
				t.Fatal("default command did not issue exactly one expected request")
			}
			if operation.Response == nil {
				if strings.TrimSpace(out.String()) != "" {
					t.Fatalf("bodyless output=%q", out.String())
				}
			} else if operation.Command == "list" || strings.HasPrefix(operation.Command, "list-") {
				var response map[string]any
				_ = json.Unmarshal(operation.Response, &response)
				records, paginated := response["records"].([]any)
				if !paginated {
					assertResourceJSON(t, out.Bytes(), operation.Response)
				} else {
					if len(records) == 0 || strings.TrimSpace(out.String()) == "" {
						t.Fatal("generated nonempty list produced no table rows")
					}
					for _, record := range records {
						row, ok := record.(map[string]any)
						if !ok {
							t.Fatal("unexpected non-object record")
						}
						id, _ := row["id"].(string)
						if id != "" && !strings.Contains(out.String(), id) {
							t.Fatalf("table omitted record id %s: %s", id, &out)
						}
					}
				}
			} else {
				assertResourceJSON(t, out.Bytes(), operation.Response)
			}
			count++
		})
	}
	if len(server.Errors()) != 0 {
		t.Fatal(server.Errors())
	}
	t.Logf("default formatter resource commands exercised=%d", count)
}
