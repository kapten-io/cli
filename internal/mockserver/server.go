// Package mockserver is a Kapten stand-in for the CLI: a fake auth service
// (package mockauth) under /auth and the Engine API generated from the CLI's
// embedded OpenAPI document (package mockengine) under /api, on one listener.
// Engine requests need an access token the fake auth issued.
//
// The end-to-end tests run the real kapten binary against it, and
// `kapten-mock` serves it for trying the CLI by hand.
package mockserver

import (
	"net/http"
	"strings"
	"time"

	"github.com/kapten-io/cli/internal/mockauth"
	"github.com/kapten-io/cli/internal/mockengine"
	"github.com/kapten-io/cli/internal/spec"
)

// Application is the OAuth application the mock accepts in server mode
// (KAPTEN_CLIENT_ID and KAPTEN_CLIENT_SECRET).
const (
	ApplicationID     = "cli_6d6f636b-0000-4000-8000-000000000001"
	ApplicationSecret = "kapten_sk_mock"
)

// Options tune the fake auth service.
type Options struct {
	// ManualApproval keeps logins pending until their code is approved on
	// the /auth/device page or through Auth.Approve.
	ManualApproval bool
	// TokenTTL is the lifetime of access tokens (default one hour).
	TokenTTL time.Duration
	// PollInterval is the device poll interval in seconds (default 1).
	PollInterval int
}

// Server is the mock. Call Listen with its base URL before serving it.
type Server struct {
	Auth   *mockauth.Server
	Engine *mockengine.Server
	base   string
}

// New builds the mock from the embedded Engine OpenAPI document.
func New(opts Options) (*Server, error) {
	engine, err := mockengine.Build(spec.OpenAPI)
	if err != nil {
		return nil, err
	}
	engine.Unbounded = true
	auth := mockauth.New()
	auth.OrganizationName = "Acme"
	auth.ManualApproval = opts.ManualApproval
	auth.Interval = 1
	if opts.PollInterval > 0 {
		auth.Interval = opts.PollInterval
	}
	if opts.TokenTTL > 0 {
		auth.TTL = opts.TokenTTL
	}
	auth.Applications[ApplicationID] = ApplicationSecret
	engine.Authenticate = auth.Verify

	return &Server{Auth: auth, Engine: engine}, nil
}

// Listen sets the base URL the mock is served at.
func (s *Server) Listen(base string) error {
	s.base = strings.TrimRight(base, "/")
	s.Auth.URL = s.IssuerURL()

	return s.Engine.Listen(s.base)
}

// IssuerURL is the fake auth service, KAPTEN_ISSUER_URL.
func (s *Server) IssuerURL() string { return s.base + "/auth" }

// APIURL is the mock Engine API, KAPTEN_API_URL.
func (s *Server) APIURL() string { return s.base + "/api" }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if rest, ok := strings.CutPrefix(r.URL.Path, "/auth"); ok && (rest == "" || strings.HasPrefix(rest, "/")) {
		r = r.Clone(r.Context())
		r.URL.Path = rest
		r.URL.RawPath = ""
		s.Auth.ServeHTTP(w, r)
		return
	}
	s.Engine.ServeHTTP(w, r)
}
