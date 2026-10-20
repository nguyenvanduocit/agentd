package agents

import (
	"strings"
	"testing"
)

// MCP-over-ACP (`mcp/message`) is unstable, no adapter implements it (claude-agent-acp advertises mcpCapabilities
// {http, sse}, codex-acp {acp: false}; both answer -32601 to the method), and the schemas disagree: acp-go-sdk 0.13.5
// carries {connectionId, method, params} with mcp/connect and mcp/disconnect, the RFD's current revision and the
// TypeScript SDK {serverId, requestId, method, params} without them. The folder guard therefore does not treat the
// inner params as opaque: a key named like a folder under them is read as a folder, so a tool argument called `cwd`
// that points outside the folders is refused instead of ignored. These tests pin that choice; the day an adapter
// advertises mcpCapabilities.acp and the shape settles, loosening it is a decision to make here, with this file and
// the README sentence changed together.
func TestTheFolderGuardReadsTheInnerParamsOfMcpMessageToo(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	folders := []string{root}
	shapes := map[string]string{
		"acp-go-sdk 0.13.5 (connectionId)":    `"connectionId":"c1"`,
		"the RFD today (serverId, requestId)": `"serverId":"s1","requestId":"r1"`,
	}
	for name, ids := range shapes {
		message := func(inner string) string {
			return `{"jsonrpc":"2.0","id":9,"method":"mcp/message","params":{` + ids + `,"method":"tools/call","params":` + inner + `}}`
		}
		t.Run(name, func(t *testing.T) {
			if reply, refused := GuardLine(folders, message(`{"name":"run","arguments":{"cwd":"`+out+`"}}`), nil); !refused || !strings.Contains(reply, `"id":9`) || !strings.Contains(reply, "outside the folders") {
				t.Errorf("a nested cwd outside the folders: refused = %v, reply = %q; want the request answered with the folder error", refused, reply)
			}
			if reply, refused := GuardLine(folders, message(`{"name":"run","arguments":{"cwd":"`+root+`"}}`), nil); refused || reply != "" {
				t.Errorf("a nested cwd inside the folders: refused = %v, reply = %q; want it passed", refused, reply)
			}
			if reply, refused := GuardLine(folders, message(`{"name":"run","arguments":{"x":1}}`), nil); refused || reply != "" {
				t.Errorf("a payload that names no folder: refused = %v, reply = %q; want it passed", refused, reply)
			}
			if reply, refused := GuardLine(folders, message(`null`), nil); refused || reply != "" {
				t.Errorf("a payload with no params: refused = %v, reply = %q; want it passed", refused, reply)
			}
			for _, key := range folderKeys {
				line := `{"jsonrpc":"2.0","id":9,"method":"mcp/message","params":{` + ids + `,"` + key + `":"` + out + `","method":"x"}}`
				if _, refused := GuardLine(folders, line, nil); !refused {
					t.Errorf("a %s on the envelope itself, outside the folders, passed", key)
				}
			}
		})
	}
}

// Whether the inner params are scanned must never depend on something the client can write: a request that is not
// mcp/message and only carries a wrapper that looks like one is held to the same rule.
func TestAFolderKeyHiddenInAnMcpMessageLookingWrapperIsStillRefused(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	folders := []string{root}
	wrapper := `{"method":"mcp/message","connectionId":"c1","params":{"cwd":"` + out + `"}}`
	for name, line := range map[string]string{
		"a wrapper under an unknown key":               `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"` + root + `","mcpServers":[],"wrapped":` + wrapper + `}}`,
		"a wrapper in a list":                          `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"` + root + `","mcpServers":[],"wrapped":[` + wrapper + `]}}`,
		"a wrapper under _meta":                        `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"` + root + `","mcpServers":[],"_meta":{"claudeCode":{"options":` + wrapper + `}}}}`,
		"a method written like a message":              `{"jsonrpc":"2.0","id":1,"method":"mcp/messages","params":{"connectionId":"c1","method":"x","params":{"cwd":"` + out + `"}}}`,
		"the method in capitals":                       `{"jsonrpc":"2.0","id":1,"method":"MCP/message","params":{"connectionId":"c1","method":"x","params":{"cwd":"` + out + `"}}}`,
		"a session request whose params say it is mcp": `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"s","method":"mcp/message","params":{"cwd":"` + out + `"}}}`,
	} {
		if _, refused := GuardLine(folders, line, nil); !refused {
			t.Errorf("%s passed: %s", name, line)
		}
	}
}

// Every request method of the ACP schema (acp-go-sdk 0.13.5 constants_gen.go, AgentMethod*; the TypeScript SDK 1.7.0 has
// the same 23) in a well-formed request, in a folder-limited session: all pass, except that listing sessions needs a
// folder and a folder outside the grant is refused wherever a method carries one.
func TestEveryRequestMethodOfTheSchemaBehavesAsDocumentedUnderFolders(t *testing.T) {
	root, out := t.TempDir(), t.TempDir()
	folders := []string{root}
	type call struct{ method, inside, outside string }
	methods := []call{
		{"initialize", `{"protocolVersion":1}`, ""},
		{"authenticate", `{"methodId":"api-key"}`, ""},
		{"logout", `{}`, ""},
		{"mcp/message", `{"serverId":"s1","requestId":"r1","method":"notifications/message","params":{"data":"hi"}}`, ""},
		{"nes/accept", `{"sessionId":"s1","id":"n1"}`, ""},
		{"nes/close", `{"sessionId":"s1"}`, ""},
		{"nes/reject", `{"sessionId":"s1","id":"n1"}`, ""},
		{"nes/start", `{"sessionId":"s1","workspaceUri":"file://` + root + `"}`, ""},
		{"nes/suggest", `{"sessionId":"s1","uri":"file://` + root + `/a.go","position":{"line":1,"character":0}}`, ""},
		{"providers/disable", `{"id":"p"}`, ""},
		{"providers/list", `{}`, ""},
		{"providers/set", `{"id":"p","apiType":"anthropic","baseUrl":"https://example.com"}`, ""},
		{"session/cancel", `{"sessionId":"s1"}`, ""},
		{"session/close", `{"sessionId":"s1"}`, ""},
		{"session/delete", `{"sessionId":"s1"}`, ""},
		{"session/fork", `{"sessionId":"s1","cwd":"` + root + `"}`, `{"sessionId":"s1","cwd":"` + out + `"}`},
		{"session/list", `{"cwd":"` + root + `"}`, `{"cwd":"` + out + `"}`},
		{"session/load", `{"sessionId":"s1","cwd":"` + root + `","mcpServers":[]}`, `{"sessionId":"s1","cwd":"` + out + `","mcpServers":[]}`},
		{"session/new", `{"cwd":"` + root + `","mcpServers":[],"additionalDirectories":["` + root + `"]}`, `{"cwd":"` + root + `","mcpServers":[],"additionalDirectories":["` + out + `"]}`},
		{"session/prompt", `{"sessionId":"s1","prompt":[{"type":"text","text":"hi"}]}`, ""},
		{"session/resume", `{"sessionId":"s1","cwd":"` + root + `"}`, `{"sessionId":"s1","cwd":"` + out + `"}`},
		{"session/set_config_option", `{"sessionId":"s1","configId":"model","value":"x"}`, ""},
		{"session/set_mode", `{"sessionId":"s1","modeId":"plan"}`, ""},
	}
	if len(methods) != 23 {
		t.Fatalf("the table has %d methods; the schema has 23: add the new ones with a request that is well formed", len(methods))
	}
	for _, c := range methods {
		line := func(params string) string {
			return `{"jsonrpc":"2.0","id":1,"method":"` + c.method + `","params":` + params + `}`
		}
		if reply, refused := GuardLine(folders, line(c.inside), nil); refused || reply != "" {
			t.Errorf("%s with its folders inside the grant: refused = %v, reply = %q; want it passed", c.method, refused, reply)
		}
		if c.outside != "" {
			if reply, refused := GuardLine(folders, line(c.outside), nil); !refused || !strings.Contains(reply, "outside the folders") {
				t.Errorf("%s with a folder outside the grant: refused = %v, reply = %q; want the folder error", c.method, refused, reply)
			}
		}
	}
	for _, params := range []string{`{}`, `{"cwd":null}`} {
		if _, refused := GuardLine(folders, `{"jsonrpc":"2.0","id":1,"method":"session/list","params":`+params+`}`, nil); !refused {
			t.Errorf("session/list with %s passed: it would list the sessions of every project", params)
		}
	}
}
