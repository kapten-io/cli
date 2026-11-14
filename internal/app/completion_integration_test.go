package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestShellCompletionQueriesIntegration(t *testing.T) {
	isolateCommandFilesystem(t)
	// Completion requests are the protocol actually executed by generated shell
	// scripts. They must work without login and across both command adapters.
	for _, request := range []string{"__complete", "__completeNoDesc"} {
		cases := []struct {
			args []string
			want []string
		}{
			{[]string{request, ""}, []string{"auth", "mcp", "projects"}},
			{[]string{request, "a"}, []string{"auth", "applications"}},
			{[]string{request, "projects", ""}, []string{"create", "get", "list", "update", "delete"}},
			{[]string{request, "auth", ""}, []string{"login", "list", "use", "logout"}},
			{[]string{request, "auth", "l"}, []string{"login", "list", "logout"}},
			{[]string{request, "m"}, []string{"mcp"}},
			{[]string{request, "mcp", ""}, []string{"serve"}},
			{[]string{request, "mcp", "serve", "--request"}, []string{"--request-timeout"}},
		}
		for _, test := range cases {
			t.Run(strings.Join(test.args, "/"), func(t *testing.T) {
				var out, diagnostics bytes.Buffer
				if err := New(&out, &diagnostics).Run(append([]string{"kapten"}, test.args...)); err != nil {
					t.Fatalf("completion request: %v; %s", err, &diagnostics)
				}
				lines := strings.Split(strings.TrimSpace(out.String()), "\n")
				if len(lines) == 0 || !strings.HasPrefix(lines[len(lines)-1], ":") {
					t.Fatalf("completion protocol has no directive: %s", &out)
				}
				suggestions := map[string]bool{}
				for _, line := range lines[:len(lines)-1] {
					name, _, _ := strings.Cut(line, "\t")
					suggestions[name] = true
				}
				for _, want := range test.want {
					if !suggestions[want] {
						t.Fatalf("completion omitted %q: %s", want, &out)
					}
				}
			})
		}
	}
}
