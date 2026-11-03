// Package mcp serves tools over the Model Context Protocol's stdio
// transport: JSON-RPC 2.0 messages, one per line, from an agent that
// started this process. It implements only what a tools server needs
// (initialize, ping, tools/list, tools/call).
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"slices"
)

// Tool is one tool the agent can call. Call's text is the result; an error
// is reported to the agent as a failed call, which it can act on.
type Tool struct {
	Name        string
	Description string
	InputSchema json.RawMessage // a JSON Schema object for the arguments
	Call        func(ctx context.Context, args json.RawMessage) (string, error)
}

// versions are the protocol versions this server speaks, newest first.
var versions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"` // absent for a notification
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve answers requests from in on out until in ends or ctx is done.
// Tool calls run one at a time, in order.
func Serve(ctx context.Context, in io.Reader, out io.Writer, name, version string, tools []Tool) error {
	enc := json.NewEncoder(out)
	send := func(r response) error {
		r.JSONRPC = "2.0"
		return enc.Encode(r)
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			if err := send(response{ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}}); err != nil {
				return err
			}
			continue
		}
		if len(req.ID) == 0 {
			continue // a notification (initialized, cancelled): nothing to answer
		}
		result, rerr := handle(ctx, req, name, version, tools)
		if err := send(response{ID: req.ID, Result: result, Error: rerr}); err != nil {
			return err
		}
	}
	return sc.Err()
}

func handle(ctx context.Context, req request, name, version string, tools []Tool) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		v := versions[0]
		if slices.Contains(versions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": name, "version": version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		list := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			list = append(list, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.InputSchema})
		}
		return map[string]any{"tools": list}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "bad tools/call params"}
		}
		i := slices.IndexFunc(tools, func(t Tool) bool { return t.Name == p.Name })
		if i < 0 {
			return nil, &rpcError{-32602, "no tool named " + p.Name}
		}
		if len(p.Arguments) == 0 {
			p.Arguments = json.RawMessage("{}")
		}
		text, err := tools[i].Call(ctx, p.Arguments)
		if err != nil {
			text = err.Error()
		}
		return map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": err != nil}, nil
	default:
		return nil, &rpcError{-32601, "method not found: " + req.Method}
	}
}
