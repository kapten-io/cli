package mcphost

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/rest-sh/restish/v2/plugin"
)

func TestCallbackCancellationReaps(t *testing.T) {
	cfg := config(t, "callback")
	started := make(chan struct{})
	stopped := make(chan struct{})
	cfg.Execute = func(ctx context.Context, req plugin.HTTPRequestMsg) plugin.HTTPResponseMsg {
		close(started)
		<-ctx.Done()
		close(stopped)
		return plugin.HTTPResponseMsg{Error: "cancelled"}
	}
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- Serve(ctx, cfg) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("callback did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback cancellation did not stop helper")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("callback still running after Serve returned")
	}
	assertReaped(t, cfg)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic-sensitive-input-error")
}

func TestInputForwardingFailureAndEOF(t *testing.T) {
	var messages []any
	send := func(v any) error { messages = append(messages, v); return nil }
	if err := forwardInput(context.Background(), failingReader{}, send); err == nil || strings.Contains(err.Error(), "synthetic-sensitive") {
		t.Fatalf("unexpected input error: %v", err)
	}
	// io.Reader is allowed to return bytes and EOF together.
	if err := forwardInput(context.Background(), &finalReader{}, send); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("got %d messages", len(messages))
	}
	data, ok := messages[0].(plugin.StdinDataMsg)
	if !ok || string(data.Data) != "final" {
		t.Fatal("final bytes lost")
	}
	if _, ok := messages[1].(plugin.StdinCloseMsg); !ok {
		t.Fatal("EOF not forwarded")
	}
}

type finalReader struct{}

func (*finalReader) Read(p []byte) (int, error) { return copy(p, "final"), io.EOF }

func TestInvalidConfigurationAndCancelledStartup(t *testing.T) {
	cfg := config(t, "echo")
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Executable = "" },
		func(c *Config) { c.Spec = nil },
		func(c *Config) { c.Execute = nil },
		func(c *Config) { c.RequestTimeout = -1 },
		func(c *Config) { c.MaxResultBytes = -1 },
	} {
		invalid := cfg
		mutate(&invalid)
		if err := Serve(testContext(t), invalid); err == nil {
			t.Fatal("invalid config accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Serve(ctx, cfg); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
