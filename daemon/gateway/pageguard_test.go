package gateway

import (
	"encoding/json"
	"strings"
	"testing"
)

// sessionNew is the line a page writes to start a session: the page names `servers` (JSON) in mcpServers.
func sessionNew(id int, servers string, extra string) string {
	return `{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"session/new","params":{"cwd":"/work","mcpServers":` + servers + extra + `}}`
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// ownBridge is the entry the page lists for its session: the `opened.mcp` the daemon gave it, named "page" (the
// sdk/web `session.mcpServer("page")`).
func ownBridge(t *testing.T, opened map[string]any) string {
	t.Helper()
	server, ok := opened["mcp"].(map[string]any)
	if !ok {
		t.Fatalf("opened = %v: the daemon gave the session no bridge", opened)
	}
	entry := map[string]any{"name": "page", "command": server["command"], "args": server["args"], "env": server["env"]}
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// echoed is whether the agent (cat) got exactly this line: it sends it back as a frame.
func (p *pageConn) echoed(line string) bool {
	_, ok := p.find(func(m map[string]any) bool { return frameOf("out")(m) && m["data"] == line })
	return ok
}

// afterwards writes a marker line and waits for its echo: by then every earlier write of the session was handled.
func (p *pageConn) afterwards(id string) {
	p.t.Helper()
	const marker = `{"marker":"after"}`
	p.send(map[string]any{"t": "write", "id": id, "line": marker})
	p.next("the marker echoed", func(m map[string]any) bool { return frameOf("out")(m) && m["data"] == marker })
}

func TestAPageStartsASessionWithItsOwnBridgeAndWithNoServersAndTheAgentGetsBoth(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	opened := p.open("s1")

	none := sessionNew(1, `[]`, "")
	own := sessionNew(2, `[`+ownBridge(t, opened)+`]`, "")
	for _, line := range []string{none, own} {
		p.send(map[string]any{"t": "write", "id": "s1", "line": line})
	}
	p.afterwards("s1")
	for _, line := range []string{none, own} {
		if !p.echoed(line) {
			t.Errorf("the agent did not get %s", line)
		}
	}
}

func TestAPageThatListsAnotherProgramGetsAnErrorAndTheAgentNeverSeesTheLine(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	opened := p.open("s1")

	foreign := sessionNew(7, `[{"name":"x","command":"/bin/sh","args":["-c","id"],"env":[]}]`, "")
	alongside := sessionNew(8, `[`+ownBridge(t, opened)+`,{"name":"x","command":"/bin/sh","args":[]}]`, "")
	for _, line := range []string{foreign, alongside} {
		p.send(map[string]any{"t": "write", "id": "s1", "line": line})
	}
	p.afterwards("s1")

	for id, line := range map[int]string{7: foreign, 8: alongside} {
		if p.echoed(line) {
			t.Errorf("the agent got %s", line)
		}
		reply, ok := p.find(func(m map[string]any) bool {
			data, _ := m["data"].(string)
			return frameOf("out")(m) && strings.Contains(data, `"id":`+itoa(id)) && strings.Contains(data, `"code":-32602`) &&
				strings.Contains(data, "a page may list only agentd's own tool bridge in mcpServers")
		})
		if !ok {
			t.Errorf("no error answered request %d: %v", id, p.all())
		}
		if reply != nil && reply["id"] != "s1" {
			t.Errorf("the error came on session %v", reply["id"])
		}
	}
}

func TestAPageThatSetsAMetaOnANewSessionGetsAnErrorAndTheAgentNeverSeesTheLine(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	p.open("s1")

	meta := sessionNew(3, `[]`, `,"_meta":{"claudeCode":{"options":{"mcpServers":{"x":{"command":"/bin/sh"}}}}}`)
	p.send(map[string]any{"t": "write", "id": "s1", "line": meta})
	p.afterwards("s1")
	if p.echoed(meta) {
		t.Error("the agent got a session/new with _meta options")
	}
	if _, ok := p.find(func(m map[string]any) bool {
		data, _ := m["data"].(string)
		return frameOf("out")(m) && strings.Contains(data, `"id":3`) && strings.Contains(data, "_meta")
	}); !ok {
		t.Errorf("no error answered the request: %v", p.all())
	}
}

// The folder guard still runs after this one: a page whose bridge is fine and whose folder is not is refused by it.
func TestThePageGuardLeavesTheFolderGuardItsTurn(t *testing.T) {
	r := startRig(t, "cat")
	r.sessions.o.Folders = func(string) ([]string, error) { return []string{r.dir}, nil }
	p := mustDial(t, r.port)
	opened := p.open("s1")

	outside := `{"jsonrpc":"2.0","id":4,"method":"session/new","params":{"cwd":"/elsewhere","mcpServers":[` + ownBridge(t, opened) + `]}}`
	p.send(map[string]any{"t": "write", "id": "s1", "line": outside})
	p.afterwards("s1")
	if p.echoed(outside) {
		t.Error("the agent got a folder outside the allowed ones")
	}
	if _, ok := p.find(func(m map[string]any) bool {
		data, _ := m["data"].(string)
		return frameOf("out")(m) && strings.Contains(data, `"id":4`) && strings.Contains(data, "outside the folders you allowed")
	}); !ok {
		t.Errorf("the folder guard did not answer: %v", p.all())
	}
}

// A line without an id has nobody to answer in JSON-RPC, and a page must not be left to guess: it gets the page
// protocol's own error message for the session.
func TestALineAPageSendsThatIsNotOneJSONMessageGetsAnErrorMessageAndNotTheAgent(t *testing.T) {
	r := startRig(t, "cat")
	p := mustDial(t, r.port)
	p.open("s1")
	lines := []string{"not json", "[1]", "{\"a\":\n1}", `{"jsonrpc":"2.0","method":"session/new","params":{"mcpServers":[{"name":"x","command":"/bin/sh"}]}}`}
	for _, line := range lines {
		p.send(map[string]any{"t": "write", "id": "s1", "line": line})
	}
	p.afterwards("s1")
	for _, line := range lines {
		if p.echoed(line) {
			t.Errorf("the agent got %q", line)
		}
	}
	errors := 0
	for _, m := range p.all() {
		if m["t"] == "error" && m["id"] == "s1" && strings.HasPrefix(m["message"].(string), "not passed to the agent: ") {
			errors++
		}
	}
	if errors != len(lines) {
		t.Errorf("%d error messages for %d refused lines: %v", errors, len(lines), p.all())
	}
}
