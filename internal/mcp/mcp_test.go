package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// session sends lines to a server and returns its answers, one per request.
func session(t *testing.T, tools []Tool, lines ...string) []map[string]any {
	t.Helper()
	var out strings.Builder
	if err := Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out, "test", "v1", tools); err != nil {
		t.Fatal(err)
	}
	var answers []map[string]any
	for l := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("answer %q: %v", l, err)
		}
		answers = append(answers, m)
	}
	return answers
}

func TestServe(t *testing.T) {
	echo := Tool{Name: "echo", Description: "says it back", InputSchema: json.RawMessage(`{"type":"object"}`),
		Call: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct{ Text string }
			json.Unmarshal(args, &a)
			if a.Text == "" {
				return "", errors.New("say something")
			}
			return a.Text, nil
		}}
	got := session(t, []Tool{echo},
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"c","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":"three","method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"resources/list"}`,
		`not json`,
	)
	if len(got) != 7 { // the notification gets no answer
		t.Fatalf("%d answers: %v", len(got), got)
	}
	init := got[0]["result"].(map[string]any)
	if init["protocolVersion"] != "2025-03-26" || init["serverInfo"].(map[string]any)["name"] != "test" {
		t.Errorf("initialize = %v", init)
	}
	tools := got[1]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo" {
		t.Errorf("tools/list = %v", tools)
	}
	ok := got[2]["result"].(map[string]any)
	if got[2]["id"] != "three" || ok["isError"] != false || ok["content"].([]any)[0].(map[string]any)["text"] != "hi" {
		t.Errorf("call = %v", got[2])
	}
	failed := got[3]["result"].(map[string]any)
	if failed["isError"] != true || failed["content"].([]any)[0].(map[string]any)["text"] != "say something" {
		t.Errorf("failed call = %v", got[3])
	}
	for i, code := range map[int]float64{4: -32602, 5: -32601, 6: -32700} {
		if e, _ := got[i]["error"].(map[string]any); e == nil || e["code"] != code {
			t.Errorf("answer %d = %v, want error %v", i, got[i], code)
		}
	}
}

// An unknown protocol version gets the newest this server speaks.
func TestServeNegotiatesVersion(t *testing.T) {
	got := session(t, nil, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	if v := got[0]["result"].(map[string]any)["protocolVersion"]; v != versions[0] {
		t.Errorf("version %v", v)
	}
}
