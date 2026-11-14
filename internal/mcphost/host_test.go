package mcphost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rest-sh/restish/v2/plugin"
)

const fixtureSpec = `{"openapi":"3.0.3","paths":{}}`

// Symlinks select fixture modes without a shell, environment changes, or adding
// test-runner arguments to the public adapter configuration.
func TestMain(m *testing.M) {
	if mode, ok := strings.CutPrefix(filepath.Base(os.Args[0]), "mcphost-fixture-"); ok {
		fixture(strings.TrimSuffix(mode, ".exe"))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func fixture(mode string) {
	_ = os.WriteFile(os.Args[0]+".pid", []byte(strconv.Itoa(os.Getpid())), 0600)
	dec := plugin.NewDecoder(os.Stdin)
	var init plugin.InitMsg
	if dec.ReadMessage(&init) != nil {
		os.Exit(21)
	}
	expected := []string{"serve", "--allow-write-tools", "--request-timeout", "2", "--max-result-bytes", "4096", "--operations", "getThing,queryLogs", "kapten"}
	if init.Type != plugin.MsgTypeInit || init.Command != "mcp" || !reflect.DeepEqual(init.Args, expected) {
		os.Exit(22)
	}
	send := func(v any) {
		if plugin.WriteMessage(os.Stdout, v) != nil {
			os.Exit(23)
		}
	}
	switch mode {
	case "hang":
		for {
			time.Sleep(time.Hour)
		}
	case "nonzero":
		os.Exit(7)
	case "eof":
		return
	case "malformed":
		_, _ = os.Stdout.Write([]byte{0xff})
		return
	case "unexpected":
		send(map[string]any{"type": "config-read", "secret": "synthetic-private-payload"})
		return
	case "callback":
		send(plugin.HTTPRequestMsg{Type: plugin.MsgTypeHTTPRequest, RequestID: "cancel-1", Method: "GET", URI: "kapten/things"})
		for {
			time.Sleep(time.Hour)
		}
	case "bad-timeout":
		send(plugin.HTTPRequestMsg{Type: plugin.MsgTypeHTTPRequest, Method: "GET", URI: "kapten/things", Timeout: -1})
		return
	case "bad-data":
		send(map[string]any{"type": plugin.MsgTypeStdoutData, "data": 42})
		return
	case "bad-spec":
		send(plugin.APISpecMsg{Type: plugin.MsgTypeAPISpec, Name: "other"})
		return
	case "bad-profile":
		send(plugin.APISpecMsg{Type: plugin.MsgTypeAPISpec, Name: "kapten", Profile: "other"})
		return
	case "done-error":
		send(plugin.DoneMsg{Type: plugin.MsgTypeDone, ExitCode: 9})
		return
	case "done-hang":
		send(plugin.DoneMsg{Type: plugin.MsgTypeDone})
		for {
			time.Sleep(time.Hour)
		}
	case "after-done":
		send(plugin.DoneMsg{Type: plugin.MsgTypeDone})
		send(plugin.StdoutDataMsg{Type: plugin.MsgTypeStdoutData, Data: []byte("bad")})
		return
	case "output":
		send(plugin.StdoutDataMsg{Type: plugin.MsgTypeStdoutData, Data: []byte("out")})
		for {
			time.Sleep(time.Hour)
		}
	case "stderr":
		_, _ = os.Stderr.Write([]byte("diagnostic"))
		for {
			time.Sleep(time.Hour)
		}
	}
	send(plugin.APISpecMsg{Type: plugin.MsgTypeAPISpec, Name: "kapten", RequestID: "spec-1"})
	var input bytes.Buffer
	closed := false
	readReply := func(want string) []byte {
		for {
			raw, err := dec.ReadRaw()
			if err != nil {
				os.Exit(24)
			}
			switch plugin.MessageType(raw) {
			case plugin.MsgTypeStdinData:
				var data plugin.StdinDataMsg
				if plugin.DecMode.Unmarshal(raw, &data) != nil || len(data.Data) > inputChunkSize {
					os.Exit(25)
				}
				input.Write(data.Data)
			case plugin.MsgTypeStdinClose:
				closed = true
			case want:
				return raw
			default:
				os.Exit(26)
			}
		}
	}
	var spec plugin.APISpecResponseMsg
	if plugin.DecMode.Unmarshal(readReply(plugin.MsgTypeAPISpecResponse), &spec) != nil || spec.RequestID != "spec-1" || spec.ContentType != "application/json" || string(spec.Raw) != fixtureSpec {
		os.Exit(27)
	}
	send(plugin.HTTPRequestMsg{Type: plugin.MsgTypeHTTPRequest, RequestID: "http-1", Method: "GET", URI: "kapten/things", Timeout: 1})
	var response plugin.HTTPResponseMsg
	if plugin.DecMode.Unmarshal(readReply(plugin.MsgTypeHTTPResponse), &response) != nil || response.RequestID != "http-1" || response.Type != plugin.MsgTypeHTTPResponse || response.Status != 200 {
		os.Exit(28)
	}
	for !closed {
		raw, err := dec.ReadRaw()
		if err != nil {
			os.Exit(29)
		}
		switch plugin.MessageType(raw) {
		case plugin.MsgTypeStdinData:
			var data plugin.StdinDataMsg
			if plugin.DecMode.Unmarshal(raw, &data) != nil || len(data.Data) > inputChunkSize {
				os.Exit(30)
			}
			input.Write(data.Data)
		case plugin.MsgTypeStdinClose:
			closed = true
		default:
			os.Exit(31)
		}
	}
	send(plugin.StdoutDataMsg{Type: plugin.MsgTypeStdoutData, Data: input.Bytes()})
	send(plugin.StderrDataMsg{Type: plugin.MsgTypeStderrData, Data: []byte("protocol diagnostic\n")})
	_, _ = os.Stderr.Write([]byte("process diagnostic\n"))
	send(plugin.DoneMsg{Type: plugin.MsgTypeDone})
}

func config(t *testing.T, mode string) Config {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "mcphost-fixture-"+mode)
	if runtime.GOOS == "windows" {
		path += ".exe"
		// Hard links avoid requiring Windows symlink privileges.
		err = os.Link(executable, path)
	} else {
		err = os.Symlink(executable, path)
	}
	if err != nil {
		t.Fatal(err)
	}
	return Config{Executable: path, In: strings.NewReader(""), Out: io.Discard, Err: io.Discard, Spec: []byte(fixtureSpec), Operations: []string{"getThing", "queryLogs"}, RequestTimeout: 2, MaxResultBytes: 4096, Execute: func(context.Context, plugin.HTTPRequestMsg) plugin.HTTPResponseMsg {
		return plugin.HTTPResponseMsg{Status: 200}
	}}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func assertReaped(t *testing.T, cfg Config) {
	t.Helper()
	data, err := os.ReadFile(cfg.Executable + ".pid")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	assertProcessReaped(t, pid)
}

func TestServeStdioAndBroker(t *testing.T) {
	cfg := config(t, "echo")
	data := bytes.Repeat([]byte("\x00stdio\n"), 9000)
	cfg.In = bytes.NewReader(data)
	var out, diagnostics bytes.Buffer
	cfg.Out, cfg.Err = &out, &diagnostics
	called := false
	cfg.Execute = func(ctx context.Context, req plugin.HTTPRequestMsg) plugin.HTTPResponseMsg {
		called = true
		if req.URI != "kapten/things" || req.Method != "GET" {
			t.Error("incorrect callback request")
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > time.Second {
			t.Error("request deadline not preserved")
		}
		return plugin.HTTPResponseMsg{Type: "wrong", RequestID: "wrong", Status: 200, Body: map[string]any{"ok": true}}
	}
	if err := Serve(testContext(t), cfg); err != nil {
		t.Fatal(err)
	}
	if !called || !bytes.Equal(out.Bytes(), data) {
		t.Fatal("stdio or HTTP callback mismatch")
	}
	if !strings.Contains(diagnostics.String(), "protocol diagnostic") || !strings.Contains(diagnostics.String(), "process diagnostic") {
		t.Fatalf("missing diagnostics: %q", diagnostics.String())
	}
	assertReaped(t, cfg)
}

func TestServeFailures(t *testing.T) {
	for _, mode := range []string{"eof", "nonzero", "malformed", "unexpected", "bad-spec", "bad-profile", "bad-timeout", "bad-data", "done-error", "done-hang", "after-done"} {
		t.Run(mode, func(t *testing.T) {
			cfg := config(t, mode)
			err := Serve(testContext(t), cfg)
			if err == nil {
				t.Fatal("expected failure")
			}
			if strings.Contains(err.Error(), "synthetic-private-payload") {
				t.Fatal("helper payload leaked")
			}
			assertReaped(t, cfg)
		})
	}
}

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("synthetic-sensitive-output-error")
}

type shortWriter struct{}

func (shortWriter) Write([]byte) (int, error) { return 0, nil }

func TestOutputFailureReaps(t *testing.T) {
	for _, mode := range []string{"output", "stderr"} {
		for _, writer := range []io.Writer{errorWriter{}, shortWriter{}} {
			t.Run(fmt.Sprintf("%s-%T", mode, writer), func(t *testing.T) {
				cfg := config(t, mode)
				cfg.Out, cfg.Err = writer, writer
				if err := Serve(testContext(t), cfg); !errors.Is(err, errOutput) {
					t.Fatalf("expected output error, got %v", err)
				}
				assertReaped(t, cfg)
			})
		}
	}
}

type idleReader struct {
	release  chan struct{}
	returned chan struct{}
	started  chan struct{}
}

func (r *idleReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	close(r.returned)
	return 0, io.EOF
}

func TestCancellationDoesNotWaitForIdleInput(t *testing.T) {
	cfg := config(t, "hang")
	reader := &idleReader{release: make(chan struct{}), returned: make(chan struct{}), started: make(chan struct{})}
	cfg.In = reader
	ctx, cancel := context.WithCancel(testContext(t))
	result := make(chan error, 1)
	go func() { result <- Serve(ctx, cfg) }()
	// Wait for startup without introducing a service or a fixed startup sleep.
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(cfg.Executable + ".pid"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			close(reader.release)
			t.Fatal("fixture did not start")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-reader.started:
	case <-time.After(time.Second):
		cancel()
		close(reader.release)
		t.Fatal("input reader did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve waited for caller-owned idle input")
	}
	close(reader.release)
	select {
	case <-reader.returned:
	case <-time.After(time.Second):
		t.Fatal("input goroutine did not stop after reader returned")
	}
	assertReaped(t, cfg)
}

func TestMissingHelper(t *testing.T) {
	cfg := config(t, "echo")
	cfg.Executable += "-missing"
	if err := Serve(testContext(t), cfg); err == nil || !strings.Contains(err.Error(), "pinned restish-mcp") {
		t.Fatalf("missing helper error: %v", err)
	}
}
