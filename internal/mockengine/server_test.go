package mockengine

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/kapten-io/cli/internal/spec"
)

func fixtureURL(s *Server, op Operation) string {
	path := op.Path
	index := 0
	for _, part := range strings.Split(op.Path, "/") {
		if strings.HasPrefix(part, "{") {
			path = strings.Replace(path, part, url.PathEscape(op.Args[index]), 1)
			index++
		}
	}
	q := url.Values{}
	for name, value := range op.Query {
		q.Set(name, value)
	}
	return s.URL + path + "?" + q.Encode()
}

func TestCanonicalGeneratedResponses(t *testing.T) {
	s := New(t, spec.OpenAPI)
	client := &http.Client{Timeout: 5 * time.Second}
	bodyless, ignored := 0, 0
	for _, op := range s.Operations {
		t.Run(op.ID, func(t *testing.T) {
			req, err := http.NewRequest(op.Method, fixtureURL(s, op), bytes.NewReader(op.Body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer synthetic-contract")
			for name, value := range op.Headers {
				req.Header.Set(name, value)
			}
			if op.Body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			route, params, err := s.router.FindRoute(req)
			if err != nil {
				t.Fatal(err)
			}
			header := http.Header{}
			if op.Response != nil {
				header.Set("Content-Type", "application/json")
			}
			input := &openapi3filter.RequestValidationInput{Request: req, Route: route, PathParams: params}
			responseInput := (&openapi3filter.ResponseValidationInput{RequestValidationInput: input, Status: op.Status, Header: header, Options: &openapi3filter.Options{IncludeResponseStatus: true}}).SetBodyBytes(op.Response)
			if err := openapi3filter.ValidateResponse(req.Context(), responseInput); err != nil {
				t.Fatalf("canonical response %s: %v", op.ID, err)
			}
			if op.Response == nil {
				bodyless++
			}
			// Ignored operations are not CLI commands. Validate every response,
			// but do not require generated CLI request fixtures for this inventory.
			if op.Ignored {
				ignored++
				return
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != op.Status {
				t.Fatalf("status=%d want=%d body=%s errors=%v", resp.StatusCode, op.Status, data, s.Errors())
			}
			if !bytes.Equal(data, op.Response) {
				t.Fatalf("response changed: %s", data)
			}
		})
	}
	if len(s.Errors()) != 0 {
		t.Fatalf("contract errors=%v", s.Errors())
	}
	if len(s.Requests()) != len(s.Operations)-ignored {
		t.Fatal("canonical operations omitted")
	}
	t.Logf("validated canonical responses=%d bodyless=%d ignored requests=%d exposed HTTP requests=%d", len(s.Operations), bodyless, ignored, len(s.Requests()))
}

func TestMockRejectsInvalidRequestsAndSeparatesIntentionalAuthErrors(t *testing.T) {
	s := New(t, spec.OpenAPI)
	client := &http.Client{Timeout: 5 * time.Second}
	var get, body Operation
	for _, op := range s.Operations {
		if !op.Ignored && op.Method == "GET" && len(op.Args) > 0 && get.ID == "" {
			get = op
		}
		if !op.Ignored && len(op.Body) > 0 && body.ID == "" {
			body = op
		}
	}
	if get.ID == "" || body.ID == "" {
		t.Fatal("canonical inventory has no suitable invalid-request cases")
	}
	type invalidCase struct {
		name, method, target, bearer string
		want                         int
		fixtureError                 bool
	}
	cases := []invalidCase{
		{"unknown route", "GET", s.URL + "/does-not-exist", "valid-synthetic", 404, true},
		{"missing bearer", get.Method, fixtureURL(s, get), "", 400, true},
		{"invalid path", get.Method, strings.Replace(fixtureURL(s, get), get.Args[0], "not-a-uuid", 1), "valid-synthetic", 400, true},
		{"missing required body", body.Method, fixtureURL(s, body), "valid-synthetic", 400, true},
		{"intentional bearer rejection", get.Method, fixtureURL(s, get), "rejected-synthetic", 401, false},
	}
	// Select an integer query parameter from the canonical inventory,
	// rather than relying on an unregistered UUID-format validator.
	invalidQueryURL := ""
	invalidQueryMethod := ""
	for _, op := range s.Operations {
		for name, value := range op.Query {
			if _, err := strconv.Atoi(value); err == nil {
				u, err := url.Parse(fixtureURL(s, op))
				if err != nil {
					t.Fatal(err)
				}
				q := u.Query()
				q.Set(name, "not-an-integer")
				u.RawQuery = q.Encode()
				invalidQueryURL, invalidQueryMethod = u.String(), op.Method
				break
			}
		}
		if invalidQueryURL != "" {
			break
		}
	}
	if invalidQueryURL == "" {
		t.Fatal("no canonical integer query")
	}
	cases = append(cases[:4], invalidCase{"invalid integer query", invalidQueryMethod, invalidQueryURL, "valid-synthetic", 400, true}, cases[4])
	s.RejectBearer("rejected-synthetic")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(s.Errors())
			req, err := http.NewRequest(tc.method, tc.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tc.bearer)
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status=%d want=%d errors=%v", resp.StatusCode, tc.want, s.Errors())
			}
			wantErrors := before
			if tc.fixtureError {
				wantErrors++
			}
			if len(s.Errors()) != wantErrors {
				t.Fatalf("errors=%v want count=%d", s.Errors(), wantErrors)
			}
		})
	}
	records := s.Requests()
	if len(records) != len(cases) || !records[len(records)-1].Rejected {
		t.Fatalf("invalid/rejected requests must be recorded: %v", records)
	}
	// Returned snapshots may be changed without mutating internal records.
	records[0].Method = "changed"
	if s.Requests()[0].Method == "changed" {
		t.Fatal("Requests aliases internal state")
	}
	t.Log(fmt.Sprintf("invalid requests=%d intentional auth rejection=%d", len(cases)-1, 1))
}

func TestMockFixturesRemainStableAcrossHTTPServers(t *testing.T) {
	first, second := New(t, spec.OpenAPI), New(t, spec.OpenAPI)
	client := &http.Client{Timeout: 5 * time.Second}
	for i, op := range first.Operations {
		other := second.Operations[i]
		if op.ID != other.ID || !bytes.Equal(op.Body, other.Body) || !bytes.Equal(op.Response, other.Response) || fmt.Sprint(op.Args) != fmt.Sprint(other.Args) || fmt.Sprint(op.Query) != fmt.Sprint(other.Query) {
			t.Fatalf("seeded generation differs for %s\nfirst response=%s\nsecond response=%s\nfirst body=%s\nsecond body=%s\nargs=%v / %v\nquery=%v / %v", op.ID, op.Response, other.Response, op.Body, other.Body, op.Args, other.Args, op.Query, other.Query)
		}
		if op.Ignored {
			continue
		}
		responses := make([][]byte, 0, 2)
		for _, server := range []*Server{first, second} {
			req, err := http.NewRequest(op.Method, fixtureURL(server, op), bytes.NewReader(op.Body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer synthetic-contract")
			for name, value := range op.Headers {
				req.Header.Set(name, value)
			}
			if op.Body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			response, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != op.Status {
				t.Fatalf("%s: status=%d err=%v body=%s", op.ID, response.StatusCode, err, data)
			}
			responses = append(responses, data)
		}
		if !bytes.Equal(responses[0], responses[1]) {
			t.Fatalf("HTTP fixtures differ for %s", op.ID)
		}
	}
	if len(first.Errors()) != 0 || len(second.Errors()) != 0 {
		t.Fatalf("fixture contract failures: %v / %v", first.Errors(), second.Errors())
	}
}
