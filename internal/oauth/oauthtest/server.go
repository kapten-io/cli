// Package oauthtest serves the fake auth service of package mockauth for the
// duration of a test.
package oauthtest

import (
	"net/http/httptest"
	"testing"

	"github.com/kapten-io/cli/internal/mockauth"
)

// Server is a fake auth service.
type Server = mockauth.Server

// New starts a fake auth service; unexpected requests fail the test.
func New(t testing.TB) *Server {
	s := mockauth.New()
	s.Errorf = t.Errorf
	server := httptest.NewServer(s)
	t.Cleanup(server.Close)
	s.URL = server.URL

	return s
}
