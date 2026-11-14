package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kapten-io/cli/internal/mockengine"
	"github.com/kapten-io/cli/internal/spec"
)

// The docs reference has a page per group and a section per command, for
// every command the CLI exposes, and leaves stale pages out.
func TestReferenceDocumentsEveryCommand(t *testing.T) {
	isolateCommandFilesystem(t)
	server := mockengine.New(t, spec.OpenAPI)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "removed-group.mdx"), []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if err := New(&out, &diagnostics).WriteReference(context.Background(), dir); err != nil {
		t.Fatalf("write reference: %v; %s", err, &diagnostics)
	}
	if _, err := os.Stat(filepath.Join(dir, "removed-group.mdx")); !os.IsNotExist(err) {
		t.Fatal("a page with no group was kept")
	}
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, operation := range server.Operations {
		if operation.Ignored {
			continue
		}
		page := read(operation.Groups[0] + ".mdx")
		heading := "\n## kapten " + operation.Groups[0] + " " + operation.Command + "\n"
		if !strings.Contains(page, heading) {
			t.Errorf("%s.mdx has no section %q", operation.Groups[0], strings.TrimSpace(heading))
		}
	}
	for _, command := range []string{"login", "whoami", "logout", "list", "use"} {
		if !strings.Contains(read("auth.mdx"), "\n## kapten auth "+command+"\n") {
			t.Errorf("auth.mdx does not document %s", command)
		}
	}
	if !strings.Contains(read("mcp.mdx"), "\n## kapten mcp serve\n") || !strings.Contains(read("index.mdx"), "`--context string`") {
		t.Error("the mcp page or the global flags are missing")
	}
	// Outside code, < and { would be parsed as JSX by MDX.
	code := regexp.MustCompile("(?s)```.*?```|`[^`\n]*`")
	for _, name := range []string{"applications.mdx", "auth.mdx", "environments.mdx", "index.mdx", "mcp.mdx"} {
		if text := code.ReplaceAllString(read(name), ""); strings.ContainsAny(text, "<{") {
			t.Errorf("%s has unescaped < or { outside code", name)
		}
	}
}

func TestProseEscapesOnlyOutsideCodeSpans(t *testing.T) {
	got := prose("Use `kapten <cmd>` for {x} <y>")
	want := "Use `kapten <cmd>` for &#123;x&#125; &lt;y&gt;"
	if got != want {
		t.Fatalf("prose() = %q, want %q", got, want)
	}
}
