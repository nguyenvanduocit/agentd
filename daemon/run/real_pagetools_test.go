package run

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// Runs the real Claude adapter as a page's agent and has it call a tool the page serves (`agentd-mcp --page`). It costs
// a little money (one short turn), so it only runs with RUN_REAL_CLAUDE=1 and a logged-in Claude on this machine:
//
//	RUN_REAL_CLAUDE=1 go test -count=1 -run Real -v ./daemon/run/

// pageToolServer is the page's MCP server: one tool, secret_word, that returns the word only this test knows.
type pageToolServer struct {
	word string
	mu   sync.Mutex
	hit  bool
}

func (s *pageToolServer) called() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hit
}

// answer is the JSON-RPC message to send back for the agent's MCP request, or nil when it needs none (notifications).
func (s *pageToolServer) answer(req message) message {
	id, has := req["id"]
	if !has {
		return nil
	}
	reply := func(result any) message { return message{"jsonrpc": "2.0", "id": id, "result": result} }
	params, _ := req["params"].(map[string]any)
	switch req["method"] {
	case "initialize":
		return reply(message{"protocolVersion": params["protocolVersion"], "capabilities": message{"tools": message{}}, "serverInfo": message{"name": "page", "version": "1"}})
	case "tools/list":
		return reply(message{"tools": []message{{"name": "secret_word", "description": "Returns the secret word of this page", "inputSchema": message{"type": "object", "properties": message{}}}}})
	case "tools/call":
		s.mu.Lock()
		s.hit = true
		s.mu.Unlock()
		return reply(message{"content": []message{{"type": "text", "text": s.word}}})
	}
	return message{"jsonrpc": "2.0", "id": id, "error": message{"code": -32601, "message": fmt.Sprintf("%v is not served", req["method"])}}
}

// permissionAnswer is the client's answer to what the agent asks it: permission for the page's tool is the page's to
// give, nothing else is offered.
func permissionAnswer(req message) message {
	if req["method"] != "session/request_permission" {
		return message{"id": req["id"], "error": message{"code": -32601, "message": "not offered"}}
	}
	var optionID any
	if params, ok := req["params"].(map[string]any); ok {
		options, _ := params["options"].([]any)
		for _, o := range options {
			if option, ok := o.(map[string]any); ok && strings.HasPrefix(fmt.Sprint(option["kind"]), "allow") {
				optionID = option["optionId"]
				break
			}
		}
	}
	return message{"id": req["id"], "result": message{"outcome": message{"outcome": "selected", "optionId": optionID}}}
}

// chunkText is the text a session/update adds to the agent's reply.
func chunkText(req message) string {
	params, _ := req["params"].(map[string]any)
	update, _ := params["update"].(map[string]any)
	if update["sessionUpdate"] != "agent_message_chunk" {
		return ""
	}
	content, _ := update["content"].(map[string]any)
	text, _ := content["text"].(string)
	return text
}

func TestRealClaudeAgentCallsAToolThePageServesThroughAgentdMcpAndSaysWhatItReturned(t *testing.T) {
	command, env := realClaude(t)
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	server := &pageToolServer{word: "agentd" + hex.EncodeToString(raw)}

	box := newSandbox(t)
	box.writeApps(t, nil, []registry.BrowserGrant{{Origin: pageOrigin, GrantedAt: registry.GrantStamp(time.Now())}})
	merged := map[string]string{"AGENTD_AGENT_CLAUDE": command}
	for k, v := range env {
		merged[k] = v
	}
	g := waitForGateway(t, startDaemon(t, box, nil, merged), box)

	p := mustDial(t, g.port, pageOrigin)
	p.next(t, "hello", ofType("hello"))
	const id = "s1"

	var mu sync.Mutex
	var text strings.Builder
	answers := map[int]message{}
	toAgent := func(m message) {
		m["jsonrpc"] = "2.0"
		line, _ := json.Marshal(m)
		_ = p.write(message{"t": "write", "id": id, "line": string(line)})
	}
	onAgentLine := func(acp message) {
		_, isRequest := acp["method"].(string)
		switch {
		case isRequest && acp["id"] != nil:
			toAgent(permissionAnswer(acp))
		case acp["method"] == "session/update":
			mu.Lock()
			text.WriteString(chunkText(acp))
			mu.Unlock()
		default:
			if n, ok := acp["id"].(float64); ok {
				mu.Lock()
				answers[int(n)] = acp
				mu.Unlock()
			}
		}
	}
	p.mu.Lock()
	p.onMessage = func(m message) {
		switch {
		case m["t"] == "mcp":
			var req message
			if json.Unmarshal([]byte(fmt.Sprint(m["line"])), &req) != nil {
				return
			}
			if out := server.answer(req); out != nil {
				line, _ := json.Marshal(out)
				_ = p.write(message{"t": "mcp", "id": id, "line": string(line)})
			}
		case m["t"] == "frame" && m["kind"] == "out":
			var acp message
			if json.Unmarshal([]byte(fmt.Sprint(m["data"])), &acp) == nil {
				onAgentLine(acp)
			}
		}
	}
	p.mu.Unlock()

	answer := func(n int, what string, limit time.Duration) message {
		t.Helper()
		var found message
		waitForWithin(t, limit, what, func() bool {
			mu.Lock()
			defer mu.Unlock()
			found = answers[n]
			return found != nil
		})
		return found
	}

	opened := openSession(t, p, id)
	mcp := mcpOf(t, opened)
	if !strings.HasPrefix(mcp.Command, "/") {
		t.Fatalf("the MCP command %q is not absolute", mcp.Command)
	}
	if mcp.Args == nil {
		mcp.Args = []string{}
	}
	toAgent(message{"id": 1, "method": "initialize", "params": message{"protocolVersion": 1, "clientCapabilities": message{"fs": message{"readTextFile": false, "writeTextFile": false}, "terminal": false}}})
	answer(1, "initialize", 120*time.Second)
	toAgent(message{"id": 2, "method": "session/new", "params": message{"cwd": box.work, "mcpServers": []any{message{"name": "page", "command": mcp.Command, "args": mcp.Args, "env": mcp.Env}}}})
	created, _ := answer(2, "session/new", 120*time.Second)["result"].(map[string]any)
	sessionID, _ := created["sessionId"].(string)
	if sessionID == "" {
		t.Fatalf("session/new answered %v", created)
	}
	toAgent(message{"id": 3, "method": "session/prompt", "params": message{"sessionId": sessionID, "prompt": []any{message{"type": "text", "text": "Call the tool secret_word of the MCP server named page, then reply with exactly the word it returned and nothing else."}}}})
	done := answer(3, "the turn", 150*time.Second)

	mu.Lock()
	reply := text.String()
	mu.Unlock()
	t.Logf("stopReason=%v tool called=%v reply=%q", done["result"], server.called(), reply)
	if !server.called() {
		t.Fatal("the agent did not call the page's tool")
	}
	if !strings.Contains(reply, server.word) {
		t.Fatalf("the reply %q does not contain %q", reply, server.word)
	}
}
