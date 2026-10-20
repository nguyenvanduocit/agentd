package pagetools

import (
	"encoding/json"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/agents"
)

func TestParseToDaemonReadsOpenWriteAndClose(t *testing.T) {
	cases := map[string]ToDaemon{
		`{"t":"open","id":"s1","agent":"claude"}`:    {T: "open", ID: "s1", Agent: "claude"},
		`{"t":"write","id":"s1","line":"{\"a\":1}"}`: {T: "write", ID: "s1", Line: `{"a":1}`},
		`{"t":"close","id":"s1"}`:                    {T: "close", ID: "s1"},
	}
	for text, want := range cases {
		got, ok := ParseToDaemon(text)
		if !ok || got != want {
			t.Errorf("ParseToDaemon(%s) = %+v, %v; want %+v", text, got, ok, want)
		}
	}
}

func TestParseToDaemonReadsMcpALineOfThePagesServerForTheSessionsBridge(t *testing.T) {
	got, ok := ParseToDaemon(`{"t":"mcp","id":"s1","line":"{\"id\":1}"}`)
	want := ToDaemon{T: "mcp", ID: "s1", Line: `{"id":1}`}
	if !ok || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, ok, want)
	}
}

func TestParseToDaemonRefusesAnythingElse(t *testing.T) {
	bad := []string{
		`{"t":"exec","id":"s1"}`, `{"t":"open","agent":"claude"}`, `{"t":"open","id":"","agent":"claude"}`,
		`{"t":"open","id":"s1"}`, `{"t":"write","id":"s1","line":5}`, `{"t":"close","id":7}`,
		`{"t":"mcp","id":"s1"}`, `{"t":"mcp","id":"s1","line":{}}`, `{"t":"mcp","line":"x"}`,
		`[]`, `null`, `{oops`, ``,
	}
	for _, text := range bad {
		if got, ok := ParseToDaemon(text); ok {
			t.Errorf("ParseToDaemon(%q) = %+v; want a refusal", text, got)
		}
	}
}

// Go's encoding/json matches keys without regard to case; the wire does not.
func TestParseToDaemonKeysAreCaseSensitive(t *testing.T) {
	if got, ok := ParseToDaemon(`{"T":"close","ID":"s1"}`); ok {
		t.Errorf("got %+v; want a refusal", got)
	}
}

func TestEncodeMatchesTheWire(t *testing.T) {
	statuses := []agents.AgentStatus{{Name: "claude", Title: "Claude Code", Ready: true}}
	srv := PageMcpServer("/bin/m", "/s", "tok")
	cases := []struct {
		msg  FromDaemon
		want string
	}{
		{NewHello("1.2.3", statuses), `{"t":"hello","version":"1.2.3","agents":[{"name":"claude","title":"Claude Code","ready":true,"reason":""}]}`},
		{NewHello("1", nil), `{"t":"hello","version":"1","agents":[]}`},
		{NewOpened("s1", 42, "/w", nil), `{"t":"opened","id":"s1","pid":42,"cwd":"/w"}`},
		{NewOpened("s1", 42, "/w", &srv), `{"t":"opened","id":"s1","pid":42,"cwd":"/w","mcp":{"command":"/bin/m","args":[],"env":[{"name":"AGENTD_MCP_SOCKET","value":"/s"},{"name":"AGENTD_MCP_TOKEN","value":"tok"}]}}`},
		{NewError("", "a < b & c"), `{"t":"error","id":"","message":"a < b & c"}`},
		{NewFrame("s1", agents.FrameExit, "", 0), `{"t":"frame","id":"s1","kind":"exit","data":"","code":0}`},
		{NewMcp("s1", `{"id":1}`), `{"t":"mcp","id":"s1","line":"{\"id\":1}"}`},
	}
	for _, c := range cases {
		if got := Encode(c.msg); got != c.want {
			t.Errorf("Encode = %s; want %s", got, c.want)
		}
		if !json.Valid([]byte(Encode(c.msg))) {
			t.Errorf("not JSON: %s", Encode(c.msg))
		}
	}
}
