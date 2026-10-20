package cli

import (
	"strings"
	"testing"
)

func TestParseAcp(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want ACP
	}{
		{[]string{"acp"}, ACP{}},
		{[]string{"acp", "claude"}, ACP{Agent: "claude"}},
		{[]string{"acp", "--token"}, ACP{Token: true}},
		{[]string{"acp", "--rotate"}, ACP{Rotate: true}},
		{[]string{"acp", "--rotate", "claude"}, ACP{Agent: "claude", Rotate: true}},
		{[]string{"acp", "codex", "--rotate", "--token"}, ACP{Agent: "codex", Rotate: true, Token: true}},
	} {
		got, ok := mustParse(t, tc.argv...).(ACP)
		if !ok || got != tc.want {
			t.Errorf("Parse(%q) = %#v, want %#v", tc.argv, got, tc.want)
		}
	}
}

func TestParseAcpMistakes(t *testing.T) {
	if got := parseError(t, "acp", "claude", "codex"); got != `unexpected argument "codex"` {
		t.Errorf("two agents: %q", got)
	}
	if got := parseError(t, "acp", "--rotate=yes"); !strings.Contains(got, "does not take an argument") {
		t.Errorf("--rotate=yes: %q", got)
	}
}

func TestUsageHasTheAcpCommand(t *testing.T) {
	var line string
	for _, l := range strings.Split(Usage, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "agentd acp") && strings.Contains(l, "[--rotate]") {
			line = l
		}
	}
	if line == "" || len(strings.Fields(line)) < 8 {
		t.Fatalf("Usage has no described line for agentd acp: %q", line)
	}
	for _, want := range []string{"--rotate", "--token", "ACP"} {
		if !strings.Contains(Usage, want) {
			t.Errorf("Usage lacks %q", want)
		}
	}
	if !strings.Contains(Usage[strings.Index(Usage, "examples:"):], "agentd acp") {
		t.Error("the examples do not show agentd acp")
	}
	for _, jargon := range []string{"origin", "engine"} {
		if strings.Contains(strings.ToLower(line), jargon) {
			t.Errorf("the acp line says %q", jargon)
		}
	}
}

// --token and --rotate belong to `agentd acp`. Every other command used to accept and ignore them, so `agentd --token`
// started a daemon: they are a mistake there, and the message says so.
func TestTokenAndRotateOnlyWorkWithAcp(t *testing.T) {
	for _, argv := range [][]string{
		{"--token"},
		{"--rotate"},
		{"run", "--token"},
		{"run", "--rotate"},
		{"apps", "--token"},
		{"apps", "remove", "https://app.example.com", "--rotate"},
		{"service", "status", "--token"},
		{"pair", "--app", "https://app.example.com", "--code", "1", "--token"},
		{"mcp", "--session", "s", "--rotate"},
		{"help", "--token"},
		{"version", "--rotate"},
	} {
		want := `--token only works with "agentd acp"`
		if argv[len(argv)-1] == "--rotate" {
			want = `--rotate only works with "agentd acp"`
		}
		if got := parseError(t, argv...); got != want {
			t.Errorf("Parse(%q) error = %q, want %q", argv, got, want)
		}
	}
	if got := parseError(t, "--token", "--rotate"); got != `--token only works with "agentd acp"` {
		t.Errorf("both flags: %q", got)
	}
}

// What `agentd acp` and the options that were never meant for it still do.
func TestTokenAndRotateStillWorkWithAcpAndTheOtherOptionsStayWhereTheyWere(t *testing.T) {
	mustParse(t, "acp", "--token")
	mustParse(t, "acp", "claude", "--rotate")
	mustParse(t, "--version", "--token") // a request for the version is answered whatever else is said
	mustParse(t, "run", "--dir", "/x", "--follow")
}
