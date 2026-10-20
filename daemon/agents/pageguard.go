package agents

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
)

// PageBridge is the one MCP server a page may list for a session: agentd's own bridge to the page's tools. Command is
// the absolute path of the pages' wrapper, Socket and Token the values that go in its environment (the `opened.mcp` of
// the session). Zero when the daemon has no relay socket: the page then lists none.
type PageBridge struct {
	Command string
	Socket  string
	Token   string
}

const (
	msgPageMcpServers = "a page may list only agentd's own tool bridge in mcpServers"
	msgPageMeta       = "a page may not set _meta on a session request: the agent reads its own options from it (its program, hooks, tools and MCP servers)"
	msgPageParams     = "the parameters of this request are not an object, so agentd cannot check what it asks the agent for"
	msgPageLine       = "the line is not one JSON-RPC message (a single JSON object on one line), so agentd cannot check what it asks the agent for"
)

// pageOptionMethods are the requests whose params the Claude adapter reads agent options from, in `_meta`
// (claude-agent-acp 0.86.0, dist/acp-agent.js:7015, 7099, 7117, 7128-7130 for a session, :1544 for authenticate).
var pageOptionMethods = []string{"session/new", "session/load", "session/resume", "session/fork", "authenticate"}

// pageEnv are the variables the bridge of a session has, and only those.
const (
	pageEnvSocket = "AGENTD_MCP_SOCKET"
	pageEnvToken  = "AGENTD_MCP_TOKEN"
)

// GuardPageLine is the check of a line a page sends to the agent of its session: the page is the ACP client, and it
// must not name, in the lines that start or resume a session, a program for the agent to run or options for it to
// take. Of every `session/...` request that carries `mcpServers`, those may be empty or hold only the session's own
// bridge, exactly as `opened.mcp` gave it (no other key, no argument, no other variable, no http or sse server); of
// `session/new`, `session/load`, `session/resume`, `session/fork` and `authenticate`, `_meta` must be absent: the Claude
// adapter takes the program it runs, its hooks, tools, environment and its own `mcpServers` from it.
// refused is true then; why says what was refused, in words for the page's developer; and reply is the JSON-RPC error
// for the page, empty when the line has no id to answer (a notification, or a line that is not a request): the caller
// tells the page why in its own error message then.
//
// It reads a line as the agent's decoder does (see GuardLine): exact keys, the last of two equal keys wins, and a line
// with a newline inside it, or that is not a JSON object, is refused. It runs before GuardLine, which keeps the folders.
// It is for a page: an app's server is the user's own choice and drives its sessions as it likes.
func GuardPageLine(bridge PageBridge, line string) (reply, why string, refused bool) {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return "", "", false
	}
	if strings.ContainsAny(line, "\r\n") {
		return "", msgPageLine, true
	}
	var msg map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &msg) != nil || msg == nil {
		return "", msgPageLine, true
	}
	var method string
	if raw, ok := msg["method"]; ok && json.Unmarshal(raw, &method) != nil {
		return "", msgPageLine, true
	}
	options := slices.Contains(pageOptionMethods, method)
	if !options && !strings.HasPrefix(method, "session/") {
		return "", "", false
	}
	var params map[string]json.RawMessage
	if raw := msg["params"]; len(raw) > 0 && !isNull(raw) {
		if json.Unmarshal(raw, &params) != nil || params == nil {
			return refusePage(msg["id"], msgPageParams)
		}
	}
	if meta, ok := params["_meta"]; options && ok && !isNull(meta) {
		return refusePage(msg["id"], msgPageMeta)
	}
	if servers, ok := params["mcpServers"]; ok && !ownBridgeOnly(bridge, servers) {
		return refusePage(msg["id"], msgPageMcpServers)
	}
	return "", "", false
}

// refusePage is refuse with the reason also given apart from the reply.
func refusePage(id json.RawMessage, why string) (reply, reason string, refused bool) {
	reply, refused = refuse(id, why)
	return reply, why, refused
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// ownBridgeOnly is whether `mcpServers` is null, empty, or a list of entries that all are the session's bridge.
func ownBridgeOnly(bridge PageBridge, servers json.RawMessage) bool {
	if isNull(servers) {
		return true
	}
	var list []json.RawMessage
	if json.Unmarshal(servers, &list) != nil {
		return false
	}
	for _, entry := range list {
		if !isOwnBridge(bridge, entry) {
			return false
		}
	}
	return true
}

// isOwnBridge is whether the entry is `PageMcpServer(bridge)` with a name of the page's choice and nothing else.
func isOwnBridge(bridge PageBridge, entry json.RawMessage) bool {
	var e map[string]json.RawMessage
	if json.Unmarshal(entry, &e) != nil || e == nil || bridge.Command == "" || bridge.Token == "" {
		return false
	}
	for key := range e {
		if key != "name" && key != "command" && key != "args" && key != "env" {
			return false
		}
	}
	var name, command string
	if json.Unmarshal(e["name"], &name) != nil || json.Unmarshal(e["command"], &command) != nil || command != bridge.Command {
		return false
	}
	if args, ok := e["args"]; ok && !isNull(args) {
		var list []json.RawMessage
		if json.Unmarshal(args, &list) != nil || len(list) != 0 {
			return false
		}
	}
	var env []map[string]json.RawMessage
	if json.Unmarshal(e["env"], &env) != nil || len(env) != 2 {
		return false
	}
	have := map[string]string{}
	for _, v := range env {
		if len(v) != 2 {
			return false
		}
		var key, value string
		if json.Unmarshal(v["name"], &key) != nil || json.Unmarshal(v["value"], &value) != nil {
			return false
		}
		if _, twice := have[key]; twice {
			return false
		}
		have[key] = value
	}
	return have[pageEnvSocket] == bridge.Socket && have[pageEnvToken] == bridge.Token && len(have) == 2
}
