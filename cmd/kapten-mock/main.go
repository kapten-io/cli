// Command kapten-mock serves a stand-in for Kapten's auth service and Engine
// API on one local port, to try the kapten CLI without an account:
//
//	go run ./cmd/kapten-mock
//
// Logins are approved on the page the CLI prints. Engine responses are
// generated from the CLI's embedded OpenAPI document: they have the right
// shape, not real data. This program is a development tool, not part of the
// released CLI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kapten-io/cli/internal/mockserver"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "address to listen on")
	auto := flag.Bool("auto-approve", false, "approve logins without the /auth/device page")
	ttl := flag.Duration("token-ttl", time.Hour, "access token lifetime")
	flag.Parse()

	server, err := mockserver.New(mockserver.Options{ManualApproval: !*auto, TokenTTL: *ttl})
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	if err := server.Listen("http://" + listener.Addr().String()); err != nil {
		log.Fatal(err)
	}
	fmt.Printf(`Kapten mock listening on http://%s

Point the CLI at it:

  export KAPTEN_ISSUER_URL=%s KAPTEN_API_URL=%s
  kapten login --name mock

or authenticate as an OAuth application:

  export KAPTEN_CLIENT_ID=%s KAPTEN_CLIENT_SECRET=%s

`, listener.Addr(), server.IssuerURL(), server.APIURL(), mockserver.ApplicationID, mockserver.ApplicationSecret)

	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
