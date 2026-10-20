package agents

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

func noPrograms(string) (string, bool)   { return "", false }
func everything(p string) (string, bool) { return "/bin/" + p, true }

func noAdapter(AdapterName) (AdapterState, bool) { return AdapterState{}, false }

func readyAdapter(name string) AdapterLookup {
	return func(AdapterName) (AdapterState, bool) {
		return AdapterState{Kind: AdapterReady, Bin: "/mc/adapters/" + name + "/1/node_modules/x/dist/index.js"}, true
	}
}

func inState(s AdapterState) AdapterLookup {
	return func(AdapterName) (AdapterState, bool) { return s, true }
}

func mustCommand(t *testing.T, name AgentName, env home.Env, adapter AdapterLookup, node string) AgentCommand {
	t.Helper()
	cmd, err := Command(name, env, adapter, node)
	if err != nil {
		t.Fatalf("Command(%s): %v", name, err)
	}
	return cmd
}

func commandError(t *testing.T, name AgentName, env home.Env, adapter AdapterLookup) string {
	t.Helper()
	_, err := Command(name, env, adapter, "node")
	if err == nil {
		t.Fatalf("Command(%s) has no error", name)
	}
	return err.Error()
}

func TestAgentTable(t *testing.T) {
	t.Run("names the three agents", func(t *testing.T) {
		if want := []AgentName{"claude", "codex", "grok"}; !slices.Equal(AgentNames, want) {
			t.Fatalf("got %v", AgentNames)
		}
		if !IsAgentName("claude") || IsAgentName("rm") || IsAgentName("toString") {
			t.Fatal("IsAgentName")
		}
	})

	t.Run("claude and codex run `node <installed adapter bin>`, grok runs from PATH", func(t *testing.T) {
		got := mustCommand(t, "claude", nil, readyAdapter("claude"), "/usr/bin/node")
		want := AgentCommand{Command: "/usr/bin/node", Args: []string{"/mc/adapters/claude/1/node_modules/x/dist/index.js"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v", got)
		}
		got = mustCommand(t, "grok", nil, noAdapter, "/usr/bin/node")
		want = AgentCommand{Command: "grok", Args: []string{"agent", "--always-approve", "stdio"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("no adapter command exists before the adapter is installed; npx is never the command", func(t *testing.T) {
		if msg := commandError(t, "codex", nil, noAdapter); msg != "Codex is not set up yet." {
			t.Fatal(msg)
		}
		if msg := commandError(t, "codex", nil, inState(AdapterState{Kind: AdapterInstalling})); !strings.Contains(msg, "not set up yet") {
			t.Fatal(msg)
		}
	})

	t.Run("a call returns its own copy of grok's arguments", func(t *testing.T) {
		mustCommand(t, "grok", nil, noAdapter, "node").Args[0] = "x"
		if got := mustCommand(t, "grok", nil, noAdapter, "node").Args; len(got) != 3 || got[0] != "agent" {
			t.Fatalf("got %v", got)
		}
	})
}

func TestPinnedVersionsFollowAgentPackageJSON(t *testing.T) {
	text, err := os.ReadFile("../../adapters/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	if err := json.Unmarshal(text, &pkg); err != nil {
		t.Fatal(err)
	}
	for _, name := range AdapterOrder {
		a := Adapters[name]
		if pkg.Dependencies[a.Pkg] != a.Version {
			t.Errorf("%s: adapters/package.json has %q, the table %q", a.Pkg, pkg.Dependencies[a.Pkg], a.Version)
		}
	}
}

func TestReadiness(t *testing.T) {
	t.Run("claude and codex are ready when installed, not ready with the install's reason otherwise", func(t *testing.T) {
		if got := Status("claude", nil, noPrograms, readyAdapter("claude")); got != (AgentStatus{Name: "claude", Title: "Claude Code", Ready: true}) {
			t.Fatalf("got %+v", got)
		}
		installing := AgentStatus{Name: "codex", Title: "Codex", Reason: "Codex is setting up (first run only, a few minutes)."}
		if got := Status("codex", nil, everything, noAdapter); got != installing {
			t.Fatalf("got %+v", got)
		}
		reason := "Codex could not be set up: offline. It will try again in 1 minute."
		failed := inState(AdapterState{Kind: AdapterFailed, Reason: reason})
		if got := Status("codex", nil, everything, failed); got != (AgentStatus{Name: "codex", Title: "Codex", Reason: reason}) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("every agent carries the title a user reads, in the daemon's order", func(t *testing.T) {
		var got [][2]string
		for _, s := range AllStatuses(nil, noPrograms, noAdapter) {
			got = append(got, [2]string{s.Name, s.Title})
		}
		want := [][2]string{{"claude", "Claude Code"}, {"codex", "Codex"}, {"grok", "Grok"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("grok needs grok", func(t *testing.T) {
		if got := Status("grok", nil, noPrograms, noAdapter); got != (AgentStatus{Name: "grok", Title: "Grok", Reason: "Grok is not installed on this computer."}) {
			t.Fatalf("got %+v", got)
		}
		has := func(p string) (string, bool) { return "/x/grok", p == "grok" }
		if !Status("grok", nil, has, noAdapter).Ready {
			t.Fatal("grok on PATH is ready")
		}
	})

	t.Run("the seam makes an agent ready whatever the machine has, and installs nothing", func(t *testing.T) {
		env := home.Env{SeamVariable("claude"): "/path/fake-agent"}
		if !Status("claude", env, noPrograms, noAdapter).Ready {
			t.Fatal("seamed claude is ready")
		}
		var ready []bool
		for _, s := range AllStatuses(env, noPrograms, noAdapter) {
			ready = append(ready, s.Ready)
		}
		if !reflect.DeepEqual(ready, []bool{true, false, false}) {
			t.Fatalf("got %v", ready)
		}
	})

	t.Run("an empty seam variable counts as unset", func(t *testing.T) {
		if Status("claude", home.Env{"AGENTD_AGENT_CLAUDE": "  "}, noPrograms, noAdapter).Ready {
			t.Fatal("blank seam is unset")
		}
		if msg := commandError(t, "claude", home.Env{"AGENTD_AGENT_CLAUDE": ""}, noAdapter); !strings.Contains(msg, "not set up yet") {
			t.Fatal(msg)
		}
	})
}

func TestOpenRefusal(t *testing.T) {
	refusal := func(name AgentName, env home.Env, find Finder, adapter AdapterLookup) string {
		reason, refused := OpenRefusal(name, env, find, adapter)
		if !refused {
			return "<none>"
		}
		return reason
	}

	t.Run("an adapter that is still installing says so, and nothing else", func(t *testing.T) {
		const want = "Codex is still setting up on this computer."
		if got := refusal("codex", nil, everything, noAdapter); got != want {
			t.Fatal(got)
		}
		if got := refusal("codex", nil, everything, inState(AdapterState{Kind: AdapterInstalling})); got != want {
			t.Fatal(got)
		}
	})

	t.Run("a failed install gives the reason; a ready agent gives none", func(t *testing.T) {
		if got := refusal("claude", nil, everything, inState(AdapterState{Kind: AdapterFailed, Reason: "Claude Code could not be set up: offline. It will try again in 1 minute."})); got != "Claude Code could not be set up: offline. It will try again in 1 minute." {
			t.Fatal(got)
		}
		if got := refusal("claude", nil, everything, readyAdapter("claude")); got != "<none>" {
			t.Fatal(got)
		}
		if got := refusal("grok", nil, noPrograms, noAdapter); got != "Grok is not installed on this computer." {
			t.Fatal(got)
		}
	})

	t.Run("the seam is never refused", func(t *testing.T) {
		if got := refusal("codex", home.Env{"AGENTD_AGENT_CODEX": "true"}, noPrograms, noAdapter); got != "<none>" {
			t.Fatal(got)
		}
	})
}

func TestTheTestSeam(t *testing.T) {
	t.Run("replaces the whole command line", func(t *testing.T) {
		got := mustCommand(t, "claude", home.Env{"AGENTD_AGENT_CLAUDE": "/path/fake-agent --flag"}, noAdapter, "node")
		if want := (AgentCommand{Command: "/path/fake-agent", Args: []string{"--flag"}}); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("variable names are per agent, upper case", func(t *testing.T) {
		if got := SeamVariable("grok"); got != "AGENTD_AGENT_GROK" {
			t.Fatal(got)
		}
	})

	t.Run("an empty quoted command stays an empty word: open then reports it as not found", func(t *testing.T) {
		if got := mustCommand(t, "claude", home.Env{"AGENTD_AGENT_CLAUDE": "''"}, noAdapter, "node").Command; got != "" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestSplitCommand(t *testing.T) {
	for _, c := range []struct {
		line  string
		words []string
	}{
		{"a b  c", []string{"a", "b", "c"}},
		{"  lead and trail  ", []string{"lead", "and", "trail"}},
		{"sh -c 'echo a b; sleep 1'", []string{"sh", "-c", "echo a b; sleep 1"}},
		{`node "/My Dir/index.js" --x`, []string{"node", "/My Dir/index.js", "--x"}},
		{`--flag="a b"`, []string{"--flag=a b"}},
		{`say "it's" 'a "b"'`, []string{"say", "it's", `a "b"`}},
		{"''", []string{""}},
		{"", nil},
	} {
		got, err := SplitCommand(c.line)
		if err != nil || !reflect.DeepEqual(got, c.words) {
			t.Errorf("%q: got %q, %v; want %q", c.line, got, err, c.words)
		}
	}

	for _, line := range []string{`echo "open`, `echo 'open`, `a "b' c`} {
		if _, err := SplitCommand(line); err == nil || !strings.Contains(err.Error(), "unterminated quote") {
			t.Errorf("%q has an unterminated quote, got %v", line, err)
		}
	}
}
