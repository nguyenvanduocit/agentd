package pagetools

import "encoding/json"

// McpSocketEnv is the daemon's socket for page bridges, in the environment of `agentd-mcp --page`.
const McpSocketEnv = "AGENTD_MCP_SOCKET"

// McpTokenEnv is the token of one page session, in the environment of `agentd-mcp --page`.
const McpTokenEnv = "AGENTD_MCP_TOKEN"

// PageMcpServer is the stdio MCP server a page passes to session/new: the pages' wrapper `command` (it runs
// `mcp --page`), by absolute path as the ACP schema asks, so it starts whatever PATH the agent gives its MCP servers.
// The socket and the token are in Env, not in Args: any OS user can read a process's arguments, only its own user its
// environment.
func PageMcpServer(command, socket, token string) McpServer {
	return McpServer{
		Command: command,
		Args:    []string{},
		Env: []EnvVar{
			{Name: McpSocketEnv, Value: socket},
			{Name: McpTokenEnv, Value: token},
		},
	}
}

// BridgeHello is the first line a bridge sends on the daemon's socket: the token of its session.
func BridgeHello(token string) string {
	return stringify(struct {
		Token string `json:"token"`
	}{token})
}

// ParseBridgeHello is the token of a bridge's first line; false when the line is not a hello.
func ParseBridgeHello(line string) (string, bool) {
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) != nil {
		return "", false
	}
	token, ok := m["token"].(string)
	return token, ok && token != ""
}

// BridgeAnswer is what a bridge reads in the daemon's first line: ok, or the daemon's reason for refusing.
type BridgeAnswer struct {
	OK      bool
	Message string
}

// BridgeOK is the daemon's answer to a hello it accepts. The page's lines follow it.
func BridgeOK() string {
	return `{"ok":true}`
}

// BridgeRefusal is the daemon's answer to a hello it refuses, with the reason.
func BridgeRefusal(reason string) string {
	return stringify(struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}{false, reason})
}

// ParseBridgeAnswer reads the daemon's first line; a line that is not an answer is a refusal that says so.
func ParseBridgeAnswer(line string) BridgeAnswer {
	var m map[string]any
	if json.Unmarshal([]byte(line), &m) == nil {
		ok, isBool := m["ok"].(bool)
		if isBool && ok {
			return BridgeAnswer{OK: true}
		}
		if message, isString := m["message"].(string); isBool && isString {
			return BridgeAnswer{Message: message}
		}
	}
	return BridgeAnswer{Message: "the daemon answered something else: " + line}
}
