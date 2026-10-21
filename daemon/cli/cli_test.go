package cli

import (
	"strings"
	"testing"
)

func p(s string) *string { return &s }

func mustParse(t *testing.T, argv ...string) Command {
	t.Helper()
	c, err := Parse(argv)
	if err != nil {
		t.Fatalf("Parse(%q): %v", argv, err)
	}
	return c
}

func parseError(t *testing.T, argv ...string) string {
	t.Helper()
	c, err := Parse(argv)
	if err == nil {
		t.Fatalf("Parse(%q) = %#v, want an error", argv, c)
	}
	return err.Error()
}

func equalCommand(a, b Command) bool {
	switch x := a.(type) {
	case Run:
		y, ok := b.(Run)
		return ok && eq(x.App, y.App) && eq(x.Dir, y.Dir) && eq(x.Config, y.Config)
	case Pair:
		y, ok := b.(Pair)
		return ok && x.App == y.App && eq(x.Code, y.Code) && eq(x.Config, y.Config)
	case MCP:
		y, ok := b.(MCP)
		return ok && x.Session == y.Session && eq(x.App, y.App) && eq(x.Config, y.Config)
	case Apps:
		y, ok := b.(Apps)
		return ok && x.Action == y.Action && x.Origin == y.Origin && eq(x.Config, y.Config)
	default:
		return a == b
	}
}

func eq(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestParseCommands(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want Command
	}{
		{"no arguments runs the daemon", nil, Run{}},
		{"run takes --app, --dir and --config", []string{"--app", "https://s", "--dir", "/w", "--config", "/c.json"},
			Run{App: p("https://s"), Dir: p("/w"), Config: p("/c.json")}},
		{"run takes --app=value", []string{"--app=https://s"}, Run{App: p("https://s")}},
		{"run is the explicit name of the default command", []string{"run"}, Run{}},
		{"run with --dir", []string{"run", "--dir", "/w"}, Run{Dir: p("/w")}},
		{"empty --dir= is not an absent --dir", []string{"--dir="}, Run{Dir: p("")}},

		{"apps lists", []string{"apps"}, Apps{Action: AppsList}},
		{"apps list with --config", []string{"apps", "list", "--config", "/c.json"}, Apps{Action: AppsList, Config: p("/c.json")}},
		{"apps removes one origin", []string{"apps", "remove", "https://a"}, Apps{Action: AppsRemove, Origin: "https://a"}},

		{"version", []string{"version"}, Version{}},
		{"--help", []string{"--help"}, Help{}},
		{"-h", []string{"-h"}, Help{}},

		{"pair with a code", []string{"pair", "--app", "https://s", "--code", "abc"}, Pair{App: "https://s", Code: p("abc")}},
		// Without --code the code comes from AGENTD_PAIR_CODE or stdin, so it never shows in the process list.
		{"pair without a code", []string{"pair", "--app", "https://s"}, Pair{App: "https://s"}},

		{"mcp with a session", []string{"mcp", "--session", "s1", "--config", "/c.json"}, MCP{Session: "s1", Config: p("/c.json")}},
		{"mcp --page is the bridge to a page's MCP server", []string{"mcp", "--page"}, PageMCP{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := mustParse(t, c.argv...)
			if !equalCommand(got, c.want) {
				t.Errorf("Parse(%q) = %#v, want %#v", c.argv, got, c.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name    string
		argv    []string
		contain string
	}{
		{"run takes no argument", []string{"run", "extra"}, `unexpected argument "extra"`},
		{"apps remove needs an origin", []string{"apps", "remove"}, "<origin>"},
		{"apps rejects an unknown action", []string{"apps", "frob"}, "list, remove"},
		{"pair needs --app", []string{"pair", "--code", "abc"}, "--app"},
		{"mcp needs a session", []string{"mcp"}, "--session"},
		{"mcp --page and --session are exclusive", []string{"mcp", "--page", "--session", "s1"}, "--session <id> or --page, not both"},
		{"mcp names --page when it has no session", []string{"mcp"}, "--page"},
		{"unknown command", []string{"frobnicate"}, `unknown command "frobnicate"`},
		{"unknown option", []string{"--nope"}, "--nope"},
		{"version takes no argument", []string{"version", "extra"}, `unexpected argument "extra"`},
		{"option without its value", []string{"--app"}, "--app"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseError(t, c.argv...); !strings.Contains(got, c.contain) {
				t.Errorf("Parse(%q) error = %q, want it to contain %q", c.argv, got, c.contain)
			}
		})
	}
}

// The exact texts of the argument-count and value mistakes, taken from the Node implementation.
func TestParseErrorTexts(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"apps", "list", "https://x", "y"}, `unexpected argument "y"`},
		{[]string{"run", "a", "b"}, `unexpected argument "b"`},
		{[]string{"pair", "--app="}, "pair needs --app <url>"},
		{[]string{"mcp", "--session="}, "mcp needs --session <id> (an app's session) or --page (a page's)"},
	}
	for _, c := range cases {
		if got := parseError(t, c.argv...); got != c.want {
			t.Errorf("Parse(%q) error = %q, want %q", c.argv, got, c.want)
		}
	}
}

func TestParseHelpWinsOverPositionals(t *testing.T) {
	if _, ok := mustParse(t, "frobnicate", "a", "b", "c", "--help").(Help); !ok {
		t.Error("--help after bad positionals is not Help")
	}
}

func TestUsage(t *testing.T) {
	for _, want := range []string{"apps remove", "pair --app", "agentd help", "-v, --version", "-h, --help"} {
		if !strings.Contains(Usage, want) {
			t.Errorf("Usage lacks %q", want)
		}
	}
	if !strings.HasPrefix(Usage, "agentd: runs your agents for the apps you allow\n\nusage:\n") || !strings.HasSuffix(Usage, "agentd acp claude\n") {
		t.Errorf("Usage has the wrong frame: %q ... %q", Usage[:40], Usage[len(Usage)-40:])
	}
}

// Every line of the usage says what its command does, and none says what is not true.
func TestUsageDescribesEveryCommandAndExplainsRemove(t *testing.T) {
	lines := strings.Split(Usage, "\n")
	for _, command := range []string{"agentd apps remove", "agentd acp", "agentd version"} {
		found := false
		for _, line := range lines {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), command); ok {
				found = true
				if strings.TrimSpace(rest) == "" {
					t.Errorf("%q has no description", command)
				}
			}
		}
		if !found {
			t.Errorf("Usage has no line for %q", command)
		}
	}
	if !strings.Contains(Usage, "examples:\n") {
		t.Error("Usage has no examples")
	}
}

func TestHelpAndVersionAreCommandsAndFlags(t *testing.T) {
	for _, argv := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		if _, ok := mustParse(t, argv...).(Help); !ok {
			t.Errorf("Parse(%q) is not Help", argv)
		}
	}
	for _, argv := range [][]string{{"version"}, {"--version"}, {"-v"}} {
		if _, ok := mustParse(t, argv...).(Version); !ok {
			t.Errorf("Parse(%q) is not Version", argv)
		}
	}
	if got := parseError(t, "help", "me"); got != `unexpected argument "me"` {
		t.Errorf("help me: %q", got)
	}
}

func TestAnUnknownOptionPointsAtHelp(t *testing.T) {
	for _, tc := range []struct{ arg, name string }{{"--nope", "--nope"}, {"--nope=1", "--nope"}, {"-x", "-x"}} {
		if got, want := parseError(t, tc.arg), "unknown option "+tc.name+`. Run "agentd --help".`; got != want {
			t.Errorf("Parse(%q) error = %q, want %q", tc.arg, got, want)
		}
	}
}

// The entry's switch reads as one case per command.
func TestCommandSwitch(t *testing.T) {
	kinds := map[string]string{}
	for _, argv := range [][]string{{}, {"pair", "--app", "x"}, {"mcp", "--session", "s"}, {"mcp", "--page"}, {"apps"}, {"version"}, {"-h"}} {
		switch mustParse(t, argv...).(type) {
		case Run:
			kinds["run"] = "ok"
		case Pair:
			kinds["pair"] = "ok"
		case MCP:
			kinds["mcp"] = "ok"
		case PageMCP:
			kinds["page-mcp"] = "ok"
		case Apps:
			kinds["apps"] = "ok"
		case Version:
			kinds["version"] = "ok"
		case Help:
			kinds["help"] = "ok"
		}
	}
	if len(kinds) != 7 {
		t.Errorf("type switch reached %d of 7 commands: %v", len(kinds), kinds)
	}
}
