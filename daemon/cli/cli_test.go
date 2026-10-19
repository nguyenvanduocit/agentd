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
	case Service:
		y, ok := b.(Service)
		return ok && x.Action == y.Action && x.Lines == y.Lines && x.Follow == y.Follow && eq(x.Dir, y.Dir)
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

		{"service install", []string{"service", "install"}, Service{Action: ServiceInstall, Lines: 100}},
		{"service uninstall", []string{"service", "uninstall"}, Service{Action: ServiceUninstall, Lines: 100}},
		{"service start", []string{"service", "start"}, Service{Action: ServiceStart, Lines: 100}},
		{"service restart", []string{"service", "restart"}, Service{Action: ServiceRestart, Lines: 100}},
		{"service status", []string{"service", "status"}, Service{Action: ServiceStatus, Lines: 100}},
		{"service logs -n -f", []string{"service", "logs", "-n", "20", "-f"}, Service{Action: ServiceLogs, Lines: 20, Follow: true}},
		{"service logs --lines=5", []string{"service", "logs", "--lines=5"}, Service{Action: ServiceLogs, Lines: 5}},
		{"service install --dir", []string{"service", "install", "--dir", "/work"}, Service{Action: ServiceInstall, Lines: 100, Dir: p("/work")}},

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
	servicePhrase := "install, uninstall, start, restart, status, logs"
	cases := []struct {
		name    string
		argv    []string
		contain string
	}{
		{"run takes no argument", []string{"run", "extra"}, `unexpected argument "extra"`},
		{"service needs an action", []string{"service"}, servicePhrase},
		{"service rejects an unknown action", []string{"service", "reboot"}, servicePhrase},
		{"service logs -n 0", []string{"service", "logs", "-n", "0"}, "positive"},
		{"service logs -n x", []string{"service", "logs", "-n", "x"}, "positive"},
		{"service install takes no argument", []string{"service", "install", "extra"}, `unexpected argument "extra"`},
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
		{[]string{"service", "logs", "-n", "1.5"}, `--lines "1.5" is not a positive number`},
		{[]string{"service", "logs", "--lines="}, `--lines "" is not a positive number`},
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

func TestParseServiceLinesRange(t *testing.T) {
	cases := []struct {
		lines string
		want  int
	}{{"1", 1}, {" 7 ", 7}, {"1e2", 100}, {"0x10", 16}, {"+5", 5}, {"5.0", 5}, {"9999999999999999999999", int(^uint(0) >> 1)}}
	for _, c := range cases {
		got := mustParse(t, "service", "logs", "--lines="+c.lines).(Service)
		if got.Lines != c.want {
			t.Errorf("--lines=%q = %d, want %d", c.lines, got.Lines, c.want)
		}
	}
	for _, bad := range []string{"", "-1", "0", "NaN", "Infinity", "1.5", "1_0", "0x", "abc", "1,5"} {
		if err := parseError(t, "service", "logs", "--lines="+bad); !strings.Contains(err, "positive") {
			t.Errorf("--lines=%q error = %q", bad, err)
		}
	}
}

func TestUsage(t *testing.T) {
	for _, want := range []string{"apps remove", "pair --app", "service install"} {
		if !strings.Contains(Usage, want) {
			t.Errorf("Usage lacks %q", want)
		}
	}
	if !strings.HasPrefix(Usage, "agentd: runs your agents for the apps you allow\n\nusage:\n") || !strings.HasSuffix(Usage, "service logs: keep printing\n") {
		t.Errorf("Usage has the wrong frame: %q ... %q", Usage[:40], Usage[len(Usage)-40:])
	}
}

// The entry's switch reads as one case per command.
func TestCommandSwitch(t *testing.T) {
	kinds := map[string]string{}
	for _, argv := range [][]string{{}, {"pair", "--app", "x"}, {"mcp", "--session", "s"}, {"mcp", "--page"}, {"apps"}, {"service", "status"}, {"version"}, {"-h"}} {
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
		case Service:
			kinds["service"] = "ok"
		case Version:
			kinds["version"] = "ok"
		case Help:
			kinds["help"] = "ok"
		}
	}
	if len(kinds) != 8 {
		t.Errorf("type switch reached %d of 8 commands: %v", len(kinds), kinds)
	}
}
