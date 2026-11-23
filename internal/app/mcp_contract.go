package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/kapten-io/cli/internal/spec"
	"github.com/rest-sh/restish/v2/plugin"
)

const mcpRequestBodyLimit = 1 << 20

type mcpContract struct {
	raw         []byte
	operations  map[string]bool
	annotations map[string]mcpToolAnnotations
	router      routers.Router
}

// Expose the same resource operations as the CLI, while honoring explicit MCP
// exclusions. The projection and request guard share this exact inventory.
func mcpExposedOperation(op *openapi3.Operation) bool {
	return op != nil && op.OperationID != "" && op.Extensions["x-cli-ignore"] != true && op.Extensions["x-mcp-ignore"] != true
}

type mcpToolAnnotations struct {
	ReadOnlyHint    bool  `json:"readOnlyHint"`
	DestructiveHint *bool `json:"destructiveHint,omitempty"`
}

// Semantic-read POSTs remain read-only hints, not write permission exceptions.
func mcpReadOperation(method string, op *openapi3.Operation) bool {
	switch method {
	case http.MethodGet, http.MethodHead:
		return true
	case http.MethodPost:
		switch op.OperationID {
		case "queryApplicationRunLogs", "queryJobRunLogs", "queryDatabaseRunLogs", "queryCacheRunLogs":
			return true
		}
	}
	return false
}

func newMCPContract(only []string) (*mcpContract, error) {
	loader := openapi3.NewLoader()
	// References are resolved only from the embedded contract, never the network.
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromData(spec.OpenAPI)
	if err != nil {
		return nil, fmt.Errorf("load MCP contract: %w", err)
	}
	allowed := map[string]bool{}
	annotations := map[string]mcpToolAnnotations{}
	for _, item := range doc.Paths.Map() {
		if item.Extensions["x-cli-ignore"] == true || item.Extensions["x-mcp-ignore"] == true {
			continue
		}
		for method, op := range item.Operations() {
			if !mcpExposedOperation(op) {
				continue
			}
			allowed[op.OperationID] = true
			annotation := mcpToolAnnotations{ReadOnlyHint: mcpReadOperation(method, op)}
			if annotation.ReadOnlyHint || method == http.MethodDelete {
				destructive := method == http.MethodDelete
				annotation.DestructiveHint = &destructive
			}
			// Other writes retain MCP's conservative destructiveHint default.
			annotations[op.OperationID] = annotation
		}
	}
	selected := map[string]bool{}
	for _, id := range only {
		id = strings.TrimSpace(id)
		if id == "" || !allowed[id] {
			return nil, fmt.Errorf("MCP operation %q is not an exposed Kapten operation", id)
		}
		selected[id] = true
	}
	for name, item := range doc.Paths.Map() {
		if item.Extensions["x-cli-ignore"] == true || item.Extensions["x-mcp-ignore"] == true {
			doc.Paths.Delete(name)
			continue
		}
		for method, op := range item.Operations() {
			if !mcpExposedOperation(op) || (len(only) > 0 && !selected[op.OperationID]) {
				item.SetOperation(method, nil)
			}
		}
		if len(item.Operations()) == 0 {
			doc.Paths.Delete(name)
		}
	}
	// The host fixes the target to the selected context; spec server URLs are
	// neither an MCP tool argument nor an outbound-routing authority.
	doc.Servers = nil
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	router, err := gorillamux.NewRouter(doc)
	if err != nil {
		return nil, fmt.Errorf("build MCP request guard: %w", err)
	}
	if len(only) > 0 {
		allowed = selected
		for id := range annotations {
			if !selected[id] {
				delete(annotations, id)
			}
		}
	}
	return &mcpContract{raw: raw, operations: allowed, annotations: annotations, router: router}, nil
}

func (c *mcpContract) validate(ctx context.Context, msg plugin.HTTPRequestMsg) ([]byte, error) {
	// Upstream generates registered-API-relative URIs. Never accept an absolute
	// URL, fragment, path traversal, or an alternate API from the helper.
	u, err := url.Parse(msg.URI)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || u.Fragment != "" || !strings.HasPrefix(u.Path, "kapten/") {
		return nil, errors.New("MCP request must target the Kapten API")
	}
	p := strings.TrimPrefix(u.Path, "kapten")
	if path.Clean(p) != p || strings.Contains(u.RawPath, "%2f") || strings.Contains(u.RawPath, "%2F") {
		return nil, errors.New("MCP request path is not canonical")
	}
	if msg.Filter != "" {
		return nil, errors.New("MCP request filters are not supported")
	}
	var body []byte
	if msg.Body != nil {
		body, err = json.Marshal(msg.Body)
		if err != nil || len(body) > mcpRequestBodyLimit {
			return nil, errors.New("MCP request body is invalid or exceeds the size limit")
		}
	}
	u.Path = p
	if u.RawPath != "" {
		u.RawPath = strings.TrimPrefix(u.RawPath, "kapten")
	}
	u.Scheme, u.Host = "https", "kapten.invalid"
	req, err := http.NewRequestWithContext(ctx, msg.Method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid MCP request")
	}
	for name, value := range msg.Headers {
		if strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "Proxy-Authorization") || strings.EqualFold(name, "Host") {
			return nil, errors.New("MCP tools cannot supply credentials or routing headers")
		}
		req.Header.Set(name, value)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", msg.ContentType)
	}
	route, params, err := c.router.FindRoute(req)
	if err != nil || route.Operation == nil || !c.operations[route.Operation.OperationID] {
		return nil, errors.New("MCP request is not an allowed Kapten operation")
	}
	if len(body) > 0 && route.Operation.RequestBody == nil {
		return nil, errors.New("MCP operation does not accept a request body")
	}
	allowedHeaders := map[string]bool{}
	parameters := append(append(openapi3.Parameters{}, route.PathItem.Parameters...), route.Operation.Parameters...)
	for _, parameter := range parameters {
		if parameter.Value != nil && parameter.Value.In == "header" {
			allowedHeaders[strings.ToLower(parameter.Value.Name)] = true
		}
	}
	for header := range msg.Headers {
		if !allowedHeaders[strings.ToLower(header)] {
			return nil, errors.New("MCP request header is not part of the embedded API contract")
		}
	}
	input := &openapi3filter.RequestValidationInput{Request: req, PathParams: params, Route: route,
		Options: &openapi3filter.Options{AuthenticationFunc: func(context.Context, *openapi3filter.AuthenticationInput) error { return nil }}}
	if err := openapi3filter.ValidateRequest(ctx, input); err != nil {
		// Validation text can echo tool input. Keep errors actionable but redacted.
		return nil, errors.New("MCP request does not match the embedded API contract")
	}
	return body, nil
}

// Keep delegated JSON rendering bounded independently of the plugin's final
// result-size limit (which runs after HTTP execution and normalization).
type mcpOutputBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *mcpOutputBuffer) Bytes() []byte { return b.buffer.Bytes() }

func (b *mcpOutputBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.buffer.Len() {
		return 0, io.ErrShortBuffer
	}
	return b.buffer.Write(data)
}
