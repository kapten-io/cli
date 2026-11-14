package app

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// Restish v2.3.0's helper uses legacy Content-Length framing. Standard MCP
// stdio uses one JSON-RPC message per line. Translate framing and enrich tool
// annotations: JSON-RPC, tool generation and execution remain upstream.
const mcpInputMessageLimit = 4 << 20
const mcpOutputFrameLimit = 20 << 20
const mcpFrameHeaderLimit = 8 << 10

type mcpLineReader struct {
	reader  *bufio.Reader
	pending *bytes.Reader
}

func newMCPLineReader(in io.Reader) *mcpLineReader {
	return &mcpLineReader{reader: bufio.NewReader(in)}
}

func (r *mcpLineReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.pending != nil && r.pending.Len() > 0 {
		return r.pending.Read(p)
	}
	var payload []byte
	for {
		part, err := r.reader.ReadSlice('\n')
		if len(part) > mcpInputMessageLimit-len(payload) {
			return 0, errors.New("MCP input message exceeds the size limit")
		}
		payload = append(payload, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, errors.New("read MCP input failed")
		}
		if len(payload) == 0 && errors.Is(err, io.EOF) {
			return 0, io.EOF
		}
		payload = bytes.TrimSpace(payload)
		if len(payload) == 0 {
			payload = nil
			continue
		}
		if !json.Valid(payload) || payload[0] != '{' {
			return 0, errors.New("MCP stdin requires newline-delimited JSON-RPC objects")
		}
		var frame bytes.Buffer
		fmt.Fprintf(&frame, "Content-Length: %d\r\n\r\n", len(payload))
		frame.Write(payload)
		r.pending = bytes.NewReader(frame.Bytes())
		return r.pending.Read(p)
	}
}

type mcpFrameWriter struct {
	out         io.Writer
	pending     []byte
	length      int
	annotations map[string]mcpToolAnnotations
}

func newMCPFrameWriter(out io.Writer) *mcpFrameWriter {
	if out == nil {
		out = io.Discard
	}
	return &mcpFrameWriter{out: out, length: -1}
}

func (w *mcpFrameWriter) Write(p []byte) (int, error) {
	if len(p) > mcpOutputFrameLimit+mcpFrameHeaderLimit-len(w.pending) {
		return 0, errors.New("MCP helper output exceeds the size limit")
	}
	w.pending = append(w.pending, p...)
	for {
		if w.length < 0 {
			i := bytes.Index(w.pending, []byte("\r\n\r\n"))
			if i < 0 {
				if len(w.pending) > mcpFrameHeaderLimit {
					return 0, errors.New("invalid MCP helper framing")
				}
				return len(p), nil
			}
			if i > mcpFrameHeaderLimit {
				return 0, errors.New("MCP helper headers exceed the size limit")
			}
			header := string(w.pending[:i])
			if !strings.HasPrefix(header, "Content-Length: ") || strings.ContainsAny(header, "\r\n") {
				return 0, errors.New("invalid MCP helper framing")
			}
			n, err := strconv.Atoi(strings.TrimPrefix(header, "Content-Length: "))
			if err != nil || n <= 0 || n > mcpOutputFrameLimit {
				return 0, errors.New("invalid MCP helper message length")
			}
			w.length = n
			w.pending = w.pending[i+4:]
		}
		if len(w.pending) < w.length {
			return len(p), nil
		}
		payload := w.pending[:w.length]
		var compact bytes.Buffer
		if err := json.Compact(&compact, payload); err != nil || len(payload) == 0 || payload[0] != '{' {
			return 0, errors.New("invalid MCP helper JSON-RPC output")
		}
		data, err := w.annotateTools(compact.Bytes())
		if err != nil {
			return 0, err
		}
		if len(data) > mcpOutputFrameLimit {
			return 0, errors.New("annotated MCP output exceeds the size limit")
		}
		data = append(data, '\n')
		n, err := w.out.Write(data)
		if err != nil {
			return 0, err
		}
		if n != len(data) {
			return 0, io.ErrShortWrite
		}
		w.pending = w.pending[w.length:]
		w.length = -1
		if len(w.pending) == 0 {
			w.pending = nil
			return len(p), nil
		}
	}
}

// Restish v2.3.0 does not emit ToolAnnotations. Enrich tools/list results from
// the guarded contract, not from names supplied by the client. RawMessage keeps
// request IDs, schemas and unrelated response fields unchanged.
func (w *mcpFrameWriter) annotateTools(payload []byte) ([]byte, error) {
	if w.annotations == nil {
		return payload, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return nil, errors.New("invalid MCP response envelope")
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(envelope["result"], &result) != nil || result["tools"] == nil {
		return payload, nil
	}
	var tools []map[string]json.RawMessage
	if err := json.Unmarshal(result["tools"], &tools); err != nil {
		return nil, errors.New("invalid MCP tool inventory")
	}
	for _, tool := range tools {
		var name string
		if json.Unmarshal(tool["name"], &name) != nil {
			return nil, errors.New("invalid MCP tool name")
		}
		annotation, ok := w.annotations[name]
		if !ok {
			return nil, errors.New("MCP helper exposed an operation outside the contract")
		}
		var hints map[string]json.RawMessage
		if tool["annotations"] != nil && json.Unmarshal(tool["annotations"], &hints) != nil {
			return nil, errors.New("invalid MCP tool annotations")
		}
		if hints == nil {
			hints = map[string]json.RawMessage{}
		}
		hints["readOnlyHint"], _ = json.Marshal(annotation.ReadOnlyHint)
		if annotation.DestructiveHint != nil {
			hints["destructiveHint"], _ = json.Marshal(*annotation.DestructiveHint)
		}
		tool["annotations"], _ = json.Marshal(hints)
	}
	result["tools"], _ = json.Marshal(tools)
	envelope["result"], _ = json.Marshal(result)
	return json.Marshal(envelope)
}

// Diagnostics can arrive from helper pipes and the delegated OAuth/runtime
// concurrently. Serialize both paths even when an embedder supplies a buffer.
type mcpDiagnosticWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (w *mcpDiagnosticWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.out == nil {
		return len(p), nil
	}
	return w.out.Write(p)
}

func (w *mcpFrameWriter) finish() error {
	if len(w.pending) != 0 || w.length >= 0 {
		return errors.New("MCP helper returned an incomplete output frame")
	}
	return nil
}
