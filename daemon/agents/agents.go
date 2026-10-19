package agents

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// AgentCommand is a program and its arguments.
type AgentCommand struct {
	Command string
	Args    []string
}

// AgentStatus is whether the machine can start an agent now, and the agent's name as a user reads it.
type AgentStatus struct {
	Name string
	// Title is the agent's name as a user reads it ("Claude Code"): apps show it as it is.
	Title  string
	Ready  bool
	Reason string
}

// AgentName is an agent the daemon runs: an adapter's agent, or a program on the user's PATH.
type AgentName string

const Grok AgentName = "grok"

// AgentNames are the agents, in the order the daemon lists them.
var AgentNames = []AgentName{AgentName(Claude), AgentName(Codex), Grok}

var titles = map[AgentName]string{AgentName(Claude): "Claude Code", AgentName(Codex): "Codex", Grok: "Grok"}

// IsAgentName reports whether name is one of AgentNames.
func IsAgentName(name string) bool {
	return slices.Contains(AgentNames, AgentName(name))
}

// SeamVariable is the TEST SEAM AGENTD_AGENT_<NAME> (for example AGENTD_AGENT_CLAUDE="/path/fake-agent --flag"): it
// replaces the agent's command line entirely and marks the agent ready. Tests and e2e use it to run a fake agent; it
// is not a user feature, and the server cannot set it.
func SeamVariable(name AgentName) string {
	return "AGENTD_AGENT_" + strings.ToUpper(string(name))
}

// grok is the one agent that runs a program from the user's PATH; claude and codex run an adapter the daemon installs.
const (
	grokProgram = "grok"
	grokMissing = "grok not found in PATH"
)

func grokArgs() []string {
	return []string{"agent", "--always-approve", "stdio"}
}

// SplitCommand splits a command line into words. Single and double quotes group characters; there are no escapes. A
// word is a run of plain characters and quoted spans, so --flag="a b" is one word, and ” is one empty word.
func SplitCommand(line string) ([]string, error) {
	var words []string
	var word strings.Builder
	inWord := false
	flush := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '"' || r == '\'':
			end := slices.Index(rs[i+1:], r)
			if end < 0 {
				return nil, fmt.Errorf("unterminated quote in command line: %s", line)
			}
			word.WriteString(string(rs[i+1 : i+1+end]))
			inWord = true
			i += end + 1
		case unicode.IsSpace(r):
			flush()
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	flush()
	return words, nil
}

// AdapterLookup is the adapter's state as the agent sees it: installed (ready), being installed, or failed; false when
// nothing is known of it.
type AdapterLookup func(name AdapterName) (AdapterState, bool)

// Finder finds a program on PATH.
type Finder func(program string) (string, bool)

func seamed(name AgentName, env home.Env) bool {
	return strings.TrimSpace(env[SeamVariable(name)]) != ""
}

// Command is the command that starts `name`: the seam's, or `node <adapter's bin>` for an installed adapter, or the
// program on PATH. `node` is the Node.js the adapters run on.
func Command(name AgentName, env home.Env, adapter AdapterLookup, node string) (AgentCommand, error) {
	if override := env[SeamVariable(name)]; strings.TrimSpace(override) != "" {
		words, err := SplitCommand(override)
		if err != nil {
			return AgentCommand{}, err
		}
		if len(words) == 0 {
			return AgentCommand{}, fmt.Errorf("%s holds no command", SeamVariable(name))
		}
		return AgentCommand{Command: words[0], Args: words[1:]}, nil
	}
	if name == Grok {
		return AgentCommand{Command: grokProgram, Args: grokArgs()}, nil
	}
	state, ok := adapter(AdapterName(name))
	if !ok || state.Kind != AdapterReady {
		return AgentCommand{}, fmt.Errorf("the %s adapter is not installed", name)
	}
	return AgentCommand{Command: node, Args: []string{state.Bin}}, nil
}

// Status is whether the machine can start `name` now, and if not, why.
func Status(name AgentName, env home.Env, find Finder, adapter AdapterLookup) AgentStatus {
	status := func(reason string) AgentStatus {
		return AgentStatus{Name: string(name), Title: titles[name], Ready: reason == "", Reason: reason}
	}
	if seamed(name, env) {
		return status("")
	}
	if name == Grok {
		if _, ok := find(grokProgram); ok {
			return status("")
		}
		return status(grokMissing)
	}
	state, ok := adapter(AdapterName(name))
	switch {
	case ok && state.Kind == AdapterReady:
		return status("")
	case ok && state.Kind == AdapterFailed:
		return status(state.Reason)
	}
	return status(InstallingReason(Adapters[AdapterName(name)]))
}

// OpenRefusal is why `open` for this agent is refused, or false when it can start. The server sees this text.
func OpenRefusal(name AgentName, env home.Env, find Finder, adapter AdapterLookup) (string, bool) {
	status := Status(name, env, find, adapter)
	if status.Ready {
		return "", false
	}
	if !seamed(name, env) && name != Grok {
		if state, ok := adapter(AdapterName(name)); !ok || state.Kind != AdapterFailed {
			return fmt.Sprintf("the %s adapter is still installing on this machine", name), true
		}
	}
	return fmt.Sprintf("agent %s is not ready: %s", name, status.Reason), true
}

// AllStatuses is the status of every agent, in AgentNames order.
func AllStatuses(env home.Env, find Finder, adapter AdapterLookup) []AgentStatus {
	statuses := make([]AgentStatus, len(AgentNames))
	for i, n := range AgentNames {
		statuses[i] = Status(n, env, find, adapter)
	}
	return statuses
}
