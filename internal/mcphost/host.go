// Package mcphost bridges MCP stdio to the separate, pinned Restish MCP helper.
// It deliberately does not implement MCP or discover Restish plugins.
package mcphost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rest-sh/restish/v2/plugin"
)

// Config is supplied by the application's guarded resource policy boundary. Spec must
// already be filtered, and Execute must independently enforce allowed requests.
// In remains caller-owned. Readers should unblock on context cancellation when
// possible: an arbitrary Reader (including idle os.Stdin) cannot be interrupted
// safely without closing it. At most one bounded read goroutine may remain until
// that reader next returns. Writers and Execute must return on cancellation;
// arbitrary blocking user callbacks cannot be forcibly interrupted by Go.
type Config struct {
	Executable     string
	In             io.Reader
	Out, Err       io.Writer
	Spec           []byte
	Operations     []string
	RequestTimeout int
	MaxResultBytes int
	Execute        func(context.Context, plugin.HTTPRequestMsg) plugin.HTTPResponseMsg
}

const inputChunkSize = 16 * 1024
const exitGrace = 2 * time.Second

var (
	errProtocol  = errors.New("mcp helper protocol failure")
	errTransport = errors.New("mcp helper transport failure")
	errOutput    = errors.New("mcp output write failed")
	errExit      = errors.New("mcp helper exited unsuccessfully")
)

type lockedWriter struct {
	sync.Mutex
	w io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.Lock()
	defer w.Unlock()
	n, err := w.w.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

// Serve launches Executable directly and brokers only the protocol capabilities
// needed by restish-mcp v2.3.0. Generic errors never include helper payloads.
func Serve(ctx context.Context, cfg Config) error {
	if cfg.Executable == "" || len(cfg.Spec) == 0 || cfg.Execute == nil || cfg.RequestTimeout < 0 || cfg.MaxResultBytes < 0 {
		return errors.New("invalid mcp host configuration")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.In == nil {
		cfg.In = strings.NewReader("")
	}
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	if cfg.Err == nil {
		cfg.Err = io.Discard
	}
	diagnostics := &lockedWriter{w: cfg.Err}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(runCtx, cfg.Executable)
	input, err := cmd.StdinPipe()
	if err != nil {
		return errTransport
	}
	defer input.Close()
	output, outputW, err := os.Pipe()
	if err != nil {
		return errTransport
	}
	defer output.Close()
	defer outputW.Close()
	diagnosticR, diagnosticW, err := os.Pipe()
	if err != nil {
		return errTransport
	}
	defer diagnosticR.Close()
	defer diagnosticW.Close()
	cmd.Stdout, cmd.Stderr = outputW, diagnosticW
	if err := cmd.Start(); err != nil {
		return errors.New("unable to start mcp helper; install the pinned restish-mcp executable")
	}
	// The parent must not retain the child's writing ends: EOF then follows exit.
	outputW.Close()
	diagnosticW.Close()
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	reaped := false
	var processing sync.WaitGroup
	defer func() {
		cancel()
		input.Close()
		output.Close()
		diagnosticR.Close()
		if !reaped {
			<-wait
		}
		// Unlike caller-owned input, our pipe readers can be interrupted and
		// joined. Execute must honor runCtx for this cleanup to complete.
		processing.Wait()
	}()

	transport := &lockedWriter{w: input}
	send := func(msg any) error {
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		if plugin.WriteMessage(transport, msg) != nil {
			return errTransport
		}
		return nil
	}
	// The helper uses Go's flag package, which stops at the first positional.
	args := []string{"serve", "--allow-write-tools", "--request-timeout", strconv.Itoa(cfg.RequestTimeout), "--max-result-bytes", strconv.Itoa(cfg.MaxResultBytes)}
	// Write tools are safe only because the caller filters the spec AND requests.
	if len(cfg.Operations) > 0 {
		args = append(args, "--operations", strings.Join(cfg.Operations, ","))
	}
	args = append(args, "kapten")
	if err := send(plugin.InitMsg{Type: plugin.MsgTypeInit, Command: "mcp", Args: args}); err != nil {
		return err
	}
	failures := make(chan error, 2)
	diagnosticDone := make(chan error, 1)
	processing.Add(1)
	go func() {
		defer processing.Done()
		_, err := io.Copy(diagnostics, diagnosticR)
		if err != nil {
			err = errOutput
		}
		diagnosticDone <- err
	}()
	go func() {
		if err := forwardInput(runCtx, cfg.In, send); err != nil && runCtx.Err() == nil {
			failures <- err
		}
	}()
	protocol := make(chan error, 1)
	done := make(chan struct{}, 1)
	processing.Add(1)
	go func() {
		defer processing.Done()
		protocol <- broker(runCtx, output, cfg, diagnostics, send, done)
	}()
	var finished <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			input.Close()
			finished = time.After(exitGrace)
		case <-finished:
			return errExit
		case err := <-diagnosticDone:
			diagnosticDone = nil
			if err != nil {
				return err
			}
		case err := <-failures:
			return err
		case err := <-wait:
			reaped = true
			wait = nil
			if err != nil {
				return errExit
			}
			// The decoder still has buffered stdout to process after process exit.
		case err := <-protocol:
			if err != nil {
				return err
			}
			input.Close()
			if !reaped {
				timer := time.NewTimer(exitGrace)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return ctx.Err()
				case err := <-failures:
					return err
				case err := <-wait:
					reaped = true
					if err != nil {
						return errExit
					}
				case <-timer.C:
					return errExit
				}
			}
			if diagnosticDone != nil {
				select {
				case err := <-diagnosticDone:
					if err != nil {
						return err
					}
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(exitGrace):
					return errOutput
				}
			}
			return nil
		}
	}
}

func forwardInput(ctx context.Context, in io.Reader, send func(any) error) error {
	buf := make([]byte, inputChunkSize)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n, err := in.Read(buf)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if n > 0 {
			if sendErr := send(plugin.StdinDataMsg{Type: plugin.MsgTypeStdinData, Data: buf[:n]}); sendErr != nil {
				return sendErr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return send(plugin.StdinCloseMsg{Type: plugin.MsgTypeStdinClose})
			}
			return errors.New("mcp input read failed")
		}
	}
}

func broker(ctx context.Context, in io.Reader, cfg Config, diagnostics io.Writer, send func(any) error, done chan<- struct{}) error {
	dec := plugin.NewDecoder(in)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		raw, err := dec.ReadRaw()
		if err != nil {
			return fmt.Errorf("%w: decode helper message", errProtocol)
		} // EOF without done is not success.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		switch plugin.MessageType(raw) {
		case plugin.MsgTypeAPISpec:
			var msg plugin.APISpecMsg
			if plugin.DecMode.Unmarshal(raw, &msg) != nil || msg.Name != "kapten" || (msg.Profile != "" && msg.Profile != "default") {
				return fmt.Errorf("%w: invalid API spec request", errProtocol)
			}
			if err := send(plugin.APISpecResponseMsg{Type: plugin.MsgTypeAPISpecResponse, RequestID: msg.RequestID, Name: msg.Name, Profile: msg.Profile, ContentType: "application/json", Raw: cfg.Spec}); err != nil {
				return err
			}
		case plugin.MsgTypeHTTPRequest:
			var msg plugin.HTTPRequestMsg
			if plugin.DecMode.Unmarshal(raw, &msg) != nil || msg.URI == "" || msg.Timeout < 0 {
				return fmt.Errorf("%w: invalid HTTP request", errProtocol)
			}
			timeout := cfg.RequestTimeout
			if msg.Timeout > 0 && (timeout == 0 || msg.Timeout < timeout) {
				timeout = msg.Timeout
			}
			requestCtx := ctx
			cancel := func() {}
			if timeout > 0 {
				requestCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
			}
			response := cfg.Execute(requestCtx, msg)
			cancel()
			response.Type, response.RequestID = plugin.MsgTypeHTTPResponse, msg.RequestID
			if err := send(response); err != nil {
				return err
			}
		case plugin.MsgTypeStdoutData, plugin.MsgTypeStderrData:
			var msg plugin.StdoutDataMsg
			if plugin.DecMode.Unmarshal(raw, &msg) != nil || msg.Data == nil {
				return fmt.Errorf("%w: invalid output data", errProtocol)
			}
			out := cfg.Out
			if msg.Type == plugin.MsgTypeStderrData {
				out = diagnostics
			}
			n, err := out.Write(msg.Data)
			if err != nil || n != len(msg.Data) {
				return errOutput
			}
		case plugin.MsgTypeDone:
			var msg plugin.DoneMsg
			if plugin.DecMode.Unmarshal(raw, &msg) != nil {
				return errProtocol
			}
			if msg.ExitCode != 0 {
				return errExit
			}
			done <- struct{}{}
			// Drain to EOF to reject unexpected data after done and preserve diagnostics.
			if _, err := dec.ReadRaw(); !errors.Is(err, io.EOF) {
				return errProtocol
			}
			return nil
		default:
			return fmt.Errorf("%w: unsupported helper message", errProtocol)
		}
	}
}
