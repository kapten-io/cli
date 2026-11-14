// Package mockengine provides a loopback-only, schema-generated OpenAPI contract server.
package mockengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/pb33f/libopenapi"
	high "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pb33f/libopenapi/renderer"
)

const recordLimit = 4096
const bodyLimit = 4 << 20

func init() {
	// kin-openapi validates registered formats but does not register UUID by
	// default. Register once at package initialization, before concurrent tests.
	if _, exists := openapi3.SchemaStringFormats["uuid"]; !exists {
		openapi3.DefineStringFormatValidator("uuid", openapi3.NewRegexpFormatValidator(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`))
	}
}

// Request includes intentional bearer rejections as well as accepted requests.
type Request struct {
	Method, Path, OperationID, Authorization string
	Body                                     []byte
	Query                                    string
	Rejected                                 bool
}

type Operation struct {
	ID, Method, Path string
	Groups           []string
	Command          string
	Ignored          bool
	Args             []string
	Query            map[string]string
	// PositionalQuery names the required query parameters, which the CLI
	// takes as positional arguments after the path ones, in this order.
	PositionalQuery []string
	Body            []byte
	Status          int
	Response        []byte
}

// QueryFlags are the CLI flags of the optional query parameters, sorted.
func (o Operation) QueryFlags() []string {
	names := []string{}
	for name := range o.Query {
		if !slices.Contains(o.PositionalQuery, name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	flags := []string{}
	for _, name := range names {
		flag := strings.Trim(flagSeparators.ReplaceAllString(name, "-"), "-")
		flags = append(flags, "--"+flag+"="+o.Query[name])
	}
	return flags
}

var flagSeparators = regexp.MustCompile(`[^a-zA-Z0-9]+`)

type Server struct {
	URL        string
	Document   *openapi3.T
	Operations []Operation
	router     routers.Router
	mu         sync.Mutex
	requests   []Request
	errors     []error
	rejected   map[string]bool
	responses  map[string]Operation
	variants   map[string]map[string]Operation

	// Authenticate, when set, checks bearer tokens, such as against the
	// mockauth server that issued them, instead of accepting any.
	Authenticate func(token string) error
	// Unbounded keeps serving once the request record is full, for a
	// long-running mock: it then stops recording.
	Unbounded bool
}

// New serves the mock for the duration of a test.
func New(t testing.TB, data []byte) *Server {
	t.Helper()
	s, err := Build(data)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(s.serveHTTP))
	t.Cleanup(server.Close)
	if err := s.Listen(server.URL); err != nil {
		t.Fatal(err)
	}
	return s
}

// Build precomputes immutable fixtures serially; no random generator is
// shared by handlers. Call Listen before serving requests.
func Build(data []byte) (*Server, error) {
	doc, err := openapi3.NewLoader().LoadFromData(data)
	if err != nil {
		return nil, err
	}
	if err := doc.Validate(context.Background()); err != nil {
		return nil, err
	}
	native, err := libopenapi.NewDocument(data)
	if err != nil {
		return nil, err
	}
	model, err := native.BuildV3Model()
	if err != nil {
		return nil, err
	}
	// An absent dictionary uses the renderer's deterministic synthetic fallback,
	// never a machine-specific system dictionary.
	generate := renderer.NewMockGeneratorWithDictionary("", renderer.JSON)
	generate.SetSeed(731)
	s := &Server{Document: doc, rejected: map[string]bool{}, responses: map[string]Operation{}, variants: map[string]map[string]Operation{}}
	errorGenerate := renderer.NewMockGeneratorWithDictionary("", renderer.JSON)
	errorGenerate.SetSeed(731)
	paths := make([]string, 0, doc.Paths.Len())
	for path := range doc.Paths.Map() {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		item := doc.Paths.Value(path)
		nativeItem := model.Model.Paths.PathItems.GetOrZero(path)
		methods := make([]string, 0)
		for method := range item.Operations() {
			methods = append(methods, method)
		}
		sort.Strings(methods)
		for _, method := range methods {
			op := item.GetOperation(method)
			nop := nativeOperation(nativeItem, method)
			if nop == nil || op.OperationID == "" {
				return nil, fmt.Errorf("unknown operation %s %s", method, path)
			}
			fixture := Operation{ID: op.OperationID, Method: method, Path: path, Groups: op.Tags, Query: map[string]string{}}
			fixture.Ignored = extensionBool(item.Extensions["x-cli-ignore"]) || extensionBool(op.Extensions["x-cli-ignore"])
			fixture.Command, _ = op.Extensions["x-cli-name"].(string)
			if !fixture.Ignored && (fixture.Command == "" || len(op.Tags) == 0) {
				return nil, fmt.Errorf("unannotated exposed operation %s", op.OperationID)
			}
			codes := []int{}
			for code := range op.Responses.Map() {
				n, e := strconv.Atoi(code)
				if e == nil && n >= 200 && n < 300 {
					codes = append(codes, n)
				}
			}
			sort.Ints(codes)
			if len(codes) == 0 && fixture.Ignored {
				// Browser-only redirects (GitHub setup return) have no body to mock.
				continue
			}
			if len(codes) == 0 {
				return nil, fmt.Errorf("%s: no declared success response", op.OperationID)
			}
			fixture.Status = codes[0]
			response := nop.Responses.Codes.GetOrZero(strconv.Itoa(fixture.Status))
			if response.Content != nil && response.Content.Len() > 0 {
				media := response.Content.GetOrZero("application/json")
				if media == nil {
					return nil, fmt.Errorf("%s: unsupported response content", op.OperationID)
				}
				fixture.Response, err = generate.GenerateMock(media, "")
				if err == nil {
					fixture.Response, err = normalizeFixture(fixture.Response, op.Responses.Value(strconv.Itoa(fixture.Status)).Value.Content["application/json"].Schema.Value, fixture.ID)
				}
				if err != nil {
					return nil, fmt.Errorf("%s response: %v", op.OperationID, err)
				}
			}
			// Generate path/query parameters from their canonical high-level schemas.
			params := append(append([]*high.Parameter{}, nativeItem.Parameters...), nop.Parameters...)
			values := map[string]string{}
			requiredQuery := []string{}
			for _, p := range params {
				if p.In != "path" && p.In != "query" {
					if p.Required != nil && *p.Required {
						return nil, fmt.Errorf("unsupported required %s parameter %s", p.In, p.Name)
					}
					continue
				}
				b, e := generate.GenerateMock(p, "")
				if e != nil {
					return nil, fmt.Errorf("%s parameter %s: %v", op.OperationID, p.Name, e)
				}
				var value any
				if e = json.Unmarshal(b, &value); e != nil {
					return nil, fmt.Errorf("%s parameter %s: %v", op.OperationID, p.Name, e)
				}
				// libopenapi's UUID/time formats bypass SetSeed; normalize only
				// those format-generated values, without modifying canonical schemas.
				for _, ref := range append(append(openapi3.Parameters{}, item.Parameters...), op.Parameters...) {
					if ref.Value.Name == p.Name && ref.Value.In == p.In && ref.Value.Schema != nil {
						value = normalizeValue(value, ref.Value.Schema.Value, fixture.ID+"/"+p.Name)
					}
				}
				text := fmt.Sprint(value)
				if p.In == "path" {
					values[p.Name] = text
				} else {
					fixture.Query[p.Name] = text
					if p.Required != nil && *p.Required {
						requiredQuery = append(requiredQuery, p.Name)
					}
				}
			}
			// Positional ordering follows path occurrence, not schema declaration order.
			for _, part := range strings.Split(path, "/") {
				if strings.HasPrefix(part, "{") {
					fixture.Args = append(fixture.Args, values[strings.Trim(part, "{}")])
				}
			}
			for _, name := range requiredQuery {
				fixture.Args = append(fixture.Args, fixture.Query[name])
				fixture.PositionalQuery = append(fixture.PositionalQuery, name)
			}
			if nop.RequestBody != nil && nop.RequestBody.Required != nil && *nop.RequestBody.Required {
				media := nop.RequestBody.Content.GetOrZero("application/json")
				if media == nil {
					return nil, fmt.Errorf("%s: unsupported request content", op.OperationID)
				}
				fixture.Body, err = generate.GenerateMock(media, "")
				if err == nil {
					fixture.Body, err = normalizeFixture(fixture.Body, op.RequestBody.Value.Content["application/json"].Schema.Value, fixture.ID)
				}
				if err != nil {
					return nil, fmt.Errorf("%s body: %v", op.OperationID, err)
				}
			}
			s.Operations = append(s.Operations, fixture)
			if _, ok := s.responses[fixture.ID]; ok {
				return nil, fmt.Errorf("duplicate operationID %s", fixture.ID)
			}
			s.responses[fixture.ID] = fixture
			s.variants[fixture.ID] = map[string]Operation{strconv.Itoa(fixture.Status): fixture}
			// Generate other declared/default responses independently, so adding
			// error scenarios cannot change seeded success/request fixtures.
			responseCodes := []string{}
			for code := range op.Responses.Map() {
				responseCodes = append(responseCodes, code)
			}
			sort.Strings(responseCodes)
			for _, code := range responseCodes {
				canonical := op.Responses.Value(code)
				if code == strconv.Itoa(fixture.Status) {
					continue
				}
				response := nop.Responses.Codes.GetOrZero(code)
				if code == "default" {
					response = nop.Responses.Default
				}
				variant := fixture
				variant.Response = nil
				if response != nil && response.Content != nil {
					if media := response.Content.GetOrZero("application/json"); media != nil {
						variant.Response, err = errorGenerate.GenerateMock(media, "")
						if err == nil {
							variant.Response, err = normalizeFixture(variant.Response, canonical.Value.Content["application/json"].Schema.Value, fixture.ID+"/"+code)
						}
						if err != nil {
							return nil, fmt.Errorf("%s response %s: %v", fixture.ID, code, err)
						}
					}
				}
				s.variants[fixture.ID][code] = variant
			}
		}
	}
	return s, nil
}

// Listen sets the URL the mock is served at; requests are routed under it
// and under its /api path.
func (s *Server) Listen(url string) error {
	s.URL = strings.TrimRight(url, "/")
	s.Document.Servers = openapi3.Servers{{URL: s.URL}, {URL: s.URL + "/api"}}
	router, err := gorillamux.NewRouter(s.Document)
	if err != nil {
		return err
	}
	s.router = router
	return nil
}

// ServeHTTP answers an operation with its generated response, after
// validating the request against the document.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.serveHTTP(w, r) }

func nativeOperation(p *high.PathItem, method string) *high.Operation {
	switch method {
	case "GET":
		return p.Get
	case "POST":
		return p.Post
	case "PUT":
		return p.Put
	case "PATCH":
		return p.Patch
	case "DELETE":
		return p.Delete
	case "HEAD":
		return p.Head
	case "OPTIONS":
		return p.Options
	case "TRACE":
		return p.Trace
	}
	return nil
}
func extensionBool(v any) bool { b, _ := v.(bool); return b }

// RespondWithStatus selects a declared response (or its range/default fallback).
// Bodies are generated from the unchanged contract, never per-operation fixtures.
func (s *Server) RespondWithStatus(t testing.TB, operationID string, status int) []byte {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	variants, ok := s.variants[operationID]
	if !ok {
		t.Fatalf("unknown response operation %s", operationID)
	}
	var selected Operation
	for _, code := range []string{strconv.Itoa(status), fmt.Sprintf("%dXX", status/100), "default"} {
		if variant, ok := variants[code]; ok {
			selected = variant
			break
		}
	}
	if selected.ID == "" {
		t.Fatalf("%s has no declared response for HTTP %d", operationID, status)
	}
	selected.Status = status
	s.responses[operationID] = selected
	return append([]byte(nil), selected.Response...)
}

func (s *Server) RejectBearer(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejected[token] = true
}
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Request{}, s.requests...)
	for i := range out {
		out[i].Body = bytes.Clone(out[i].Body)
	}
	return out
}
func (s *Server) Errors() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]error{}, s.errors...)
}
func (s *Server) fail(w http.ResponseWriter, status int, err error) {
	s.mu.Lock()
	if len(s.errors) < recordLimit {
		s.errors = append(s.errors, err)
	}
	s.mu.Unlock()
	http.Error(w, err.Error(), status)
}
func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
	if err != nil || len(body) > bodyLimit {
		s.fail(w, 400, fmt.Errorf("request body exceeds budget or cannot be read: %v", err))
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	route, params, routeErr := s.router.FindRoute(r)
	record := Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Authorization: r.Header.Get("Authorization"), Body: body}
	if routeErr == nil {
		record.OperationID = route.Operation.OperationID
	}
	s.mu.Lock()
	record.Rejected = s.rejected[strings.TrimPrefix(record.Authorization, "Bearer ")]
	full := len(s.requests) >= recordLimit
	if !full {
		s.requests = append(s.requests, record)
	}
	s.mu.Unlock()
	if full && !s.Unbounded {
		s.fail(w, 429, fmt.Errorf("request record budget exhausted"))
		return
	}
	if routeErr != nil {
		s.fail(w, 404, routeErr)
		return
	}
	if record.Rejected {
		http.Error(w, "synthetic bearer rejection", 401)
		return
	}
	if s.Authenticate != nil {
		token, ok := strings.CutPrefix(record.Authorization, "Bearer ")
		if !ok || s.Authenticate(token) != nil {
			// The Engine's error document, as its middleware answers.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"the access token is invalid or expired","reason":"unauthenticated"}`))
			return
		}
	}
	input := &openapi3filter.RequestValidationInput{Request: r, PathParams: params, Route: route, Options: &openapi3filter.Options{AuthenticationFunc: func(_ context.Context, _ *openapi3filter.AuthenticationInput) error {
		if !strings.HasPrefix(record.Authorization, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(record.Authorization, "Bearer ")) == "" {
			return fmt.Errorf("missing synthetic bearer")
		}
		return nil
	}}}
	if err := openapi3filter.ValidateRequest(r.Context(), input); err != nil {
		s.fail(w, 400, err)
		return
	}
	s.mu.Lock()
	fixture, ok := s.responses[record.OperationID]
	s.mu.Unlock()
	if !ok {
		s.fail(w, 500, fmt.Errorf("unknown operation %s", record.OperationID))
		return
	}
	header := http.Header{}
	if fixture.Response != nil {
		header.Set("Content-Type", "application/json")
	}
	output := (&openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: fixture.Status, Header: header, Options: &openapi3filter.Options{IncludeResponseStatus: true}}).SetBodyBytes(fixture.Response)
	if err := openapi3filter.ValidateResponse(r.Context(), output); err != nil {
		s.fail(w, 500, fmt.Errorf("%s response: %w", fixture.ID, err))
		return
	}
	for k, values := range header {
		w.Header()[k] = values
	}
	w.WriteHeader(fixture.Status)
	_, _ = w.Write(fixture.Response)
}

// normalizeFixture compensates for the renderer's unseeded crypto UUIDs and
// wall-clock formats. This is schema-driven fixture normalization, not a schema
// rewrite or an operation-specific response. Validation still uses the original.
func normalizeFixture(data []byte, schema *openapi3.Schema, key string) ([]byte, error) {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	return json.Marshal(normalizeValue(value, schema, key))
}
func normalizeValue(value any, schema *openapi3.Schema, key string) any {
	if schema == nil || value == nil {
		return value
	}
	for _, ref := range schema.AllOf {
		value = normalizeValue(value, ref.Value, key)
	}
	for _, ref := range schema.OneOf {
		value = normalizeValue(value, ref.Value, key)
	}
	for _, ref := range schema.AnyOf {
		value = normalizeValue(value, ref.Value, key)
	}
	if _, ok := value.(string); ok {
		switch schema.Format {
		case "uuid":
			hash := sha256.Sum256([]byte("731/" + key))
			hash[6] = (hash[6] & 0x0f) | 0x40
			hash[8] = (hash[8] & 0x3f) | 0x80
			return fmt.Sprintf("%x-%x-%x-%x-%x", hash[:4], hash[4:6], hash[6:8], hash[8:10], hash[10:16])
		case "date-time":
			return "2025-01-02T03:04:05Z"
		case "date":
			return "2025-01-02"
		case "time":
			return "03:04:05"
		}
	}
	switch v := value.(type) {
	case map[string]any:
		for name, child := range v {
			if ref := schema.Properties[name]; ref != nil {
				v[name] = normalizeValue(child, ref.Value, key+"/"+name)
			} else if ref := schema.AdditionalProperties.Schema; ref != nil {
				v[name] = normalizeValue(child, ref.Value, key+"/"+name)
			}
		}
	case []any:
		if schema.Items != nil {
			for i, child := range v {
				v[i] = normalizeValue(child, schema.Items.Value, fmt.Sprintf("%s/%d", key, i))
			}
		}
	}
	return value
}
