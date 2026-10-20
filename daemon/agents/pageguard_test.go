package agents

import (
	"encoding/json"
	"strings"
	"testing"
)

// bridge is what a page's session was given in `opened.mcp` (see pagetools.PageMcpServer).
var bridge = PageBridge{Command: "/tmp/agentd-x1/bin/page/agentd-mcp", Socket: "/tmp/agentd-abc123/mcp.sock", Token: "tok"}

// ownServer is the bridge entry a page lists, as sdk/web's session.mcpServer("page") writes it.
const ownServer = `{"name":"page","command":"/tmp/agentd-x1/bin/page/agentd-mcp","args":[],"env":[{"name":"AGENTD_MCP_SOCKET","value":"/tmp/agentd-abc123/mcp.sock"},{"name":"AGENTD_MCP_TOKEN","value":"tok"}]}`

func newRequest(method, params string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
}

func TestGuardPageLineLetsThroughWhatPagesDocumentedlySend(t *testing.T) {
	for name, line := range map[string]string{
		"session/new with no servers":            newRequest("session/new", `{"cwd":"/work","mcpServers":[]}`),
		"session/new with the page's own bridge": newRequest("session/new", `{"cwd":"/work","mcpServers":[`+ownServer+`]}`),
		"the bridge with its env the other way":  newRequest("session/new", `{"cwd":"/work","mcpServers":[{"name":"page","command":"/tmp/agentd-x1/bin/page/agentd-mcp","env":[{"name":"AGENTD_MCP_TOKEN","value":"tok"},{"name":"AGENTD_MCP_SOCKET","value":"/tmp/agentd-abc123/mcp.sock"}]}]}`),
		"session/new with no mcpServers":         newRequest("session/new", `{"cwd":"/work"}`),
		"session/new with null mcpServers":       newRequest("session/new", `{"cwd":"/work","mcpServers":null}`),
		"session/new with a null _meta":          newRequest("session/new", `{"cwd":"/work","mcpServers":[],"_meta":null}`),
		"session/load of a session":              newRequest("session/load", `{"sessionId":"s","cwd":"/work","mcpServers":[`+ownServer+`]}`),
		"session/prompt with its own _meta":      newRequest("session/prompt", `{"sessionId":"s","prompt":[],"_meta":{"steering":{"idleBehavior":"promptRequired"}}}`),
		"session/cancel":                         `{"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"s"}}`,
		"initialize":                             newRequest("initialize", `{"protocolVersion":1,"clientCapabilities":{"_meta":{"terminal-auth":true}}}`),
		"a response to the agent":                `{"jsonrpc":"2.0","id":7,"result":{"outcome":{"outcome":"cancelled"}}}`,
		"a line with the newline that ends it":   newRequest("session/new", `{"cwd":"/work","mcpServers":[]}`) + "\n",
		"an empty line":                          "",
		"two bridge entries of the same session": newRequest("session/new", `{"cwd":"/work","mcpServers":[`+ownServer+`,`+strings.Replace(ownServer, `"page"`, `"other"`, 1)+`]}`),
	} {
		if reply, _, refused := GuardPageLine(bridge, line); refused || reply != "" {
			t.Errorf("%s: refused = %v, reply = %q; want it passed", name, refused, reply)
		}
	}
}

func TestGuardPageLineRefusesAnythingButTheSessionsOwnBridgeInMcpServers(t *testing.T) {
	for name, servers := range map[string]string{
		"a program of the page's choice":      `[{"name":"x","command":"/bin/sh","args":["-c","id"],"env":[]}]`,
		"a program by a relative name":        `[{"name":"x","command":"sh","args":[]}]`,
		"the bridge with an argument":         `[{"name":"page","command":"/tmp/agentd-x1/bin/page/agentd-mcp","args":["--other"],"env":[{"name":"AGENTD_MCP_SOCKET","value":"/tmp/agentd-abc123/mcp.sock"},{"name":"AGENTD_MCP_TOKEN","value":"tok"}]}]`,
		"the bridge of another session":       `[{"name":"page","command":"/tmp/agentd-x1/bin/page/agentd-mcp","args":[],"env":[{"name":"AGENTD_MCP_SOCKET","value":"/tmp/agentd-abc123/mcp.sock"},{"name":"AGENTD_MCP_TOKEN","value":"someone-elses"}]}]`,
		"the bridge with another socket":      `[{"name":"page","command":"/tmp/agentd-x1/bin/page/agentd-mcp","args":[],"env":[{"name":"AGENTD_MCP_SOCKET","value":"/tmp/evil.sock"},{"name":"AGENTD_MCP_TOKEN","value":"tok"}]}]`,
		"the bridge with one more variable":   `[{"name":"page","command":"/tmp/agentd-x1/bin/page/agentd-mcp","args":[],"env":[{"name":"AGENTD_MCP_SOCKET","value":"/tmp/agentd-abc123/mcp.sock"},{"name":"AGENTD_MCP_TOKEN","value":"tok"},{"name":"NODE_OPTIONS","value":"--require /tmp/x.js"}]}]`,
		"the bridge without its token":        `[{"name":"page","command":"/tmp/agentd-x1/bin/page/agentd-mcp","args":[],"env":[{"name":"AGENTD_MCP_SOCKET","value":"/tmp/agentd-abc123/mcp.sock"}]}]`,
		"the bridge with its token twice":     `[{"name":"page","command":"/tmp/agentd-x1/bin/page/agentd-mcp","args":[],"env":[{"name":"AGENTD_MCP_TOKEN","value":"tok"},{"name":"AGENTD_MCP_TOKEN","value":"tok"}]}]`,
		"the bridge with a key of its own":    `[{"name":"page","command":"/tmp/agentd-x1/bin/page/agentd-mcp","args":[],"cwd":"/","env":[{"name":"AGENTD_MCP_SOCKET","value":"/tmp/agentd-abc123/mcp.sock"},{"name":"AGENTD_MCP_TOKEN","value":"tok"}]}]`,
		"the bridge without a name":           `[{"command":"/tmp/agentd-x1/bin/page/agentd-mcp","args":[],"env":[{"name":"AGENTD_MCP_SOCKET","value":"/tmp/agentd-abc123/mcp.sock"},{"name":"AGENTD_MCP_TOKEN","value":"tok"}]}]`,
		"an http server":                      `[{"type":"http","name":"x","url":"http://evil.example/mcp","headers":[]}]`,
		"an sse server":                       `[{"type":"sse","name":"x","url":"http://evil.example/sse","headers":[]}]`,
		"the bridge and then another program": `[` + ownServer + `,{"name":"x","command":"/bin/sh","args":[]}]`,
		"a list that is an object":            `{"page":` + ownServer + `}`,
		"a list that is a string":             `"/bin/sh"`,
		"an entry that is not an object":      `["/bin/sh"]`,
		"an entry that is null":               `[null]`,
		"env that is not a list":              `[{"name":"page","command":"/tmp/agentd-x1/bin/page/agentd-mcp","args":[],"env":{"AGENTD_MCP_TOKEN":"tok"}}]`,
		"args that are not a list":            `[{"name":"page","command":"/tmp/agentd-x1/bin/page/agentd-mcp","args":"-c","env":[{"name":"AGENTD_MCP_SOCKET","value":"/tmp/agentd-abc123/mcp.sock"},{"name":"AGENTD_MCP_TOKEN","value":"tok"}]}]`,
	} {
		line := newRequest("session/new", `{"cwd":"/work","mcpServers":`+servers+`}`)
		reply, _, refused := GuardPageLine(bridge, line)
		var got struct {
			ID    int `json:"id"`
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if !refused || json.Unmarshal([]byte(reply), &got) != nil || got.ID != 1 || got.Error.Code != rpcInvalidParams ||
			!strings.Contains(got.Error.Message, "a page may list only agentd's own tool bridge in mcpServers") {
			t.Errorf("%s: refused = %v, reply = %q; want -32602 for id 1 naming the rule", name, refused, reply)
		}
	}
}

func TestGuardPageLineRefusesTheBridgeWhenTheSessionHasNone(t *testing.T) {
	line := newRequest("session/new", `{"cwd":"/work","mcpServers":[`+ownServer+`]}`)
	if _, _, refused := GuardPageLine(PageBridge{}, line); !refused {
		t.Error("a daemon without a relay socket gave the page no bridge to list")
	}
	if _, _, refused := GuardPageLine(PageBridge{}, newRequest("session/new", `{"cwd":"/work","mcpServers":[]}`)); refused {
		t.Error("an empty list is fine without a bridge")
	}
}

// The standard ACP endpoint has no bridge to a page's tools and checks a page's lines against the zero PageBridge: an
// entry that equals it field for field (an empty command, empty values) must not slip through as "the session's own".
func TestGuardPageLineWithNoBridgeRefusesAnEntryThatLooksLikeTheEmptyOne(t *testing.T) {
	empty := `{"name":"x","command":"","args":[],"env":[{"name":"AGENTD_MCP_SOCKET","value":""},{"name":"AGENTD_MCP_TOKEN","value":""}]}`
	for _, servers := range []string{`[` + empty + `]`, `[{"name":"x","command":"","args":[],"env":[]}]`, `[{}]`, `[{"name":"x"}]`} {
		line := newRequest("session/new", `{"cwd":"/work","mcpServers":`+servers+`}`)
		if _, _, refused := GuardPageLine(PageBridge{}, line); !refused {
			t.Errorf("mcpServers %s passed with no bridge", servers)
		}
	}
}

func TestGuardPageLineRefusesMcpServersOnAnySessionRequest(t *testing.T) {
	foreign := `{"cwd":"/work","sessionId":"s","mcpServers":[{"name":"x","command":"/bin/sh","args":[]}]}`
	for _, method := range []string{"session/new", "session/load", "session/resume", "session/fork", "session/prompt", "session/set_mode", "session/anything_new"} {
		if _, _, refused := GuardPageLine(bridge, newRequest(method, foreign)); !refused {
			t.Errorf("%s with a foreign server passed", method)
		}
	}
}

// The Claude adapter spreads _meta.claudeCode.options into the options of its agent: the program it runs, hooks, tools,
// environment and its own mcpServers (claude-agent-acp 0.86.0, dist/acp-agent.js:7015, 7099, 7117, 7128-7130), and
// authenticate takes a gateway base URL from _meta (:1544).
func TestGuardPageLineRefusesAMetaThatCouldCarryTheAgentsOptions(t *testing.T) {
	options := `{"claudeCode":{"options":{"mcpServers":{"x":{"command":"/bin/sh"}},"env":{"NODE_OPTIONS":"--require /tmp/x.js"},"hooks":{}}}}`
	for _, method := range []string{"session/new", "session/load", "session/resume", "session/fork", "authenticate"} {
		reply, _, refused := GuardPageLine(bridge, newRequest(method, `{"cwd":"/work","sessionId":"s","methodId":"gateway","mcpServers":[],"_meta":`+options+`}`))
		if !refused || !strings.Contains(reply, "_meta") {
			t.Errorf("%s with _meta options: refused = %v, reply = %q", method, refused, reply)
		}
	}
	if _, _, refused := GuardPageLine(bridge, newRequest("session/new", `{"cwd":"/work","_meta":{}}`)); !refused {
		t.Error("an empty _meta on session/new passed: the rule is no _meta, not no known option")
	}
}

// The agent cuts its input at every newline and reads each piece as a line of its own; Go reads the whole string as one
// value.
func TestGuardPageLineRefusesALineThatTheAgentWouldReadDifferently(t *testing.T) {
	smuggled := `{"id":2,"method":"session/new","params":{"cwd":"/work","mcpServers":[{"name":"x","command":"/bin/sh"}]}}`
	for name, line := range map[string]string{
		"a newline inside an object":  `{"k":` + "\n" + smuggled + "\n" + `}`,
		"a carriage return inside":    `{"k":` + "\r" + smuggled + `}`,
		"two requests on one line":    `{"id":1,"method":"session/cancel","params":{}}` + "\n" + smuggled,
		"text that is not JSON":       `not json`,
		"an array":                    `[` + smuggled + `]`,
		"params that are a string":    `{"id":1,"method":"session/new","params":"x"}`,
		"params that are an array":    `{"id":1,"method":"session/new","params":[]}`,
		"a method that is not a text": `{"id":1,"method":5,"params":{"mcpServers":[{"name":"x","command":"/bin/sh"}]}}`,
	} {
		if _, _, refused := GuardPageLine(bridge, line); !refused {
			t.Errorf("%s passed: %q", name, line)
		}
	}
}

// Only the exact keys count, because the agent's reader takes only those: "MCPSERVERS" is not a list of servers there
// and must not make a line refused or let one through; the last of two equal keys wins, there and here.
func TestGuardPageLineReadsKeysExactlyAndTakesTheLastOfTwo(t *testing.T) {
	if _, _, refused := GuardPageLine(bridge, newRequest("session/new", `{"cwd":"/work","mcpServers":[`+ownServer+`],"mcpservers":[{"name":"x","command":"/bin/sh"}]}`)); refused {
		t.Error("a key that differs in case is not the agent's mcpServers")
	}
	if _, _, refused := GuardPageLine(bridge, newRequest("session/new", `{"cwd":"/work","mcpServers":[],"mcpServers":[{"name":"x","command":"/bin/sh"}]}`)); !refused {
		t.Error("the later of two equal keys is the one the agent reads")
	}
	if _, _, refused := GuardPageLine(bridge, newRequest("session/new", `{"cwd":"/work","mcpServers":[{"name":"x","command":"/bin/sh"}],"mcpServers":[]}`)); refused {
		t.Error("the later of two equal keys is the one the agent reads, an empty list here")
	}
}

func TestGuardPageLineAnswersARequestAndDropsANotification(t *testing.T) {
	notification := `{"jsonrpc":"2.0","method":"session/new","params":{"cwd":"/work","_meta":{"x":1}}}`
	reply, why, refused := GuardPageLine(bridge, notification)
	if !refused || reply != "" {
		t.Errorf("notification: refused = %v, reply = %q; want it dropped with no reply", refused, reply)
	}
	if !strings.Contains(why, "_meta") {
		t.Errorf("why = %q: a refusal without an id still says what was refused, for the page's error message", why)
	}
}

// Whatever is refused says why: a line without an id has nobody to answer but the page's own error message.
func TestGuardPageLineAlwaysSaysWhyItRefused(t *testing.T) {
	for name, line := range map[string]string{
		"a line that is not JSON":       `not json`,
		"a newline inside":              "{\"a\":\n1}",
		"params that are not an object": newRequest("session/new", `[]`),
		"a foreign server":              newRequest("session/new", `{"mcpServers":[{"name":"x","command":"/bin/sh"}]}`),
		"a _meta":                       newRequest("session/new", `{"_meta":{}}`),
	} {
		reply, why, refused := GuardPageLine(bridge, line)
		if !refused || strings.TrimSpace(why) == "" {
			t.Errorf("%s: refused = %v, why = %q", name, refused, why)
		}
		if reply != "" && !strings.Contains(reply, why) {
			t.Errorf("%s: the reply %q does not carry the reason %q", name, reply, why)
		}
	}
	if reply, why, refused := GuardPageLine(bridge, newRequest("session/new", `{"mcpServers":[]}`)); refused || reply != "" || why != "" {
		t.Errorf("a line that passes: refused = %v, reply = %q, why = %q", refused, reply, why)
	}
}
