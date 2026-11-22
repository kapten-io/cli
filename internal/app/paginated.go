package app

import (
	"strings"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/kapten-io/cli/internal/spec"
)

// paginatedListCommands holds "<group> <command>" for every list command whose
// operation takes a page number: only those answer with records to show as a
// table. A list the Engine returns whole keeps its JSON envelope.
var paginatedListCommands = sync.OnceValue(func() map[string]bool {
	commands := map[string]bool{}
	doc, err := openapi3.NewLoader().LoadFromData(spec.OpenAPI)
	if err != nil {
		return commands
	}
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			name, _ := op.Extensions["x-cli-name"].(string)
			if name != "list" && !strings.HasPrefix(name, "list-") {
				continue
			}
			paginated := false
			for _, parameter := range op.Parameters {
				if parameter.Value != nil && parameter.Value.In == "query" && parameter.Value.Name == "page[number]" {
					paginated = true
				}
			}
			if !paginated {
				continue
			}
			for _, group := range op.Tags {
				commands[group+" "+name] = true
			}
		}
	}
	return commands
})
