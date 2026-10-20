package gateway

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAcpLine(t *testing.T) {
	for _, c := range []struct {
		name    string
		in      string
		line    string
		problem string
	}{
		{"a message", `{"a":1}`, `{"a":1}`, ""},
		{"one trailing newline", "{\"a\":1}\n", `{"a":1}`, ""},
		{"one trailing CRLF", "{\"a\":1}\r\n", `{"a":1}`, ""},
		{"empty", "", "", ""},
		{"only a newline", "\n", "", ""},
		{"a newline inside", "{\"a\":1}\n{\"b\":2}", "", "a message is one line"},
		{"two trailing newlines", "{\"a\":1}\n\n", "", "a message is one line"},
		{"a carriage return inside", "{\"a\":\r1}", "", "a message is one line"},
		{"a leading newline", "\n{\"a\":1}", "", "a message is one line"},
		{"not UTF-8", "{\xff}", "", "a message is UTF-8"},
	} {
		line, problem := acpLine([]byte(c.in))
		if line != c.line || problem != c.problem {
			t.Errorf("%s: acpLine(%q) = %q, %q; want %q, %q", c.name, c.in, line, problem, c.line, c.problem)
		}
	}
}

func TestRouteAcp(t *testing.T) {
	q := func(s string) url.Values { v, _ := url.ParseQuery(s); return v }
	for _, c := range []struct {
		path   string
		query  string
		agent  string
		routed acpRouted
	}{
		{"/acp/claude", "", "claude", acpNamed},
		{"/acp/codex", "", "codex", acpNamed},
		{"/acp/grok", "", "grok", acpNamed},
		{"/acp", "agent=claude", "claude", acpNamed},
		{"/acp/claude", "agent=claude", "claude", acpNamed},
		{"/acp", "", "", acpMissing},
		{"/acp/", "", "", acpMissing},
		{"/acp/claude", "agent=codex", "", acpConflict},
		{"/acp", "agent=claude&agent=codex", "", acpConflict},
		{"/acp/nosuch", "", "", acpUnknown},
		{"/acp", "agent=nosuch", "", acpUnknown},
		{"/acp/CLAUDE", "", "", acpUnknown},
		{"/acp/claude/extra", "", "", acpUnknown},
		{"/acp/cla%75de", "", "", acpUnknown},
		{"/acp/claude%2f..", "", "", acpUnknown},
		{"/acp//claude", "", "", acpUnknown},
	} {
		agent, routed := routeAcp(c.path, q(c.query))
		if agent != c.agent || routed != c.routed {
			t.Errorf("routeAcp(%q, %q) = %q, %v; want %q, %v", c.path, c.query, agent, routed, c.agent, c.routed)
		}
	}
}

func TestAcpOrigin(t *testing.T) {
	h := func(values ...string) http.Header {
		header := http.Header{}
		for _, v := range values {
			header.Add("Origin", v)
		}
		return header
	}
	for _, c := range []struct {
		name   string
		header http.Header
		origin string
		local  bool
		ok     bool
	}{
		{"none is a program on this computer", h(), "", true, true},
		{"a page", h("https://app.example.com"), "https://app.example.com", false, true},
		{"a loopback page", h("http://localhost:5173"), "http://localhost:5173", false, true},
		{"null", h("null"), "", false, false},
		{"empty", h(""), "", false, false},
		{"not canonical", h("https://App.example.com/"), "", false, false},
		{"plain http on the internet", h("http://example.com"), "", false, false},
		{"twice", h("https://a.example.com", "https://a.example.com"), "", false, false},
		{"twice, different", h("https://a.example.com", "https://b.example.com"), "", false, false},
	} {
		origin, local, ok := acpOrigin(c.header)
		if origin != c.origin || local != c.local || ok != c.ok {
			t.Errorf("%s: acpOrigin = %q, %v, %v; want %q, %v, %v", c.name, origin, local, ok, c.origin, c.local, c.ok)
		}
	}
}

func TestNewConnectionID(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		id := newConnectionID()
		if !uuidV4.MatchString(id) || seen[id] {
			t.Fatalf("id %q is not a fresh version 4 uuid", id)
		}
		seen[id] = true
	}
}

func TestAcpExitReason(t *testing.T) {
	for _, c := range []struct {
		code int
		tail string
		want string
	}{
		{0, "", "agent exited with code 0"},
		{3, "", "agent exited with code 3"},
		{1, "fatal: no credentials", "agent exited with code 1: fatal: no credentials"},
		{143, "ignored on a clean exit?", "agent exited with code 143: ignored on a clean exit?"},
		{0, "something on stderr", "agent exited with code 0"},
	} {
		if got := acpExitReason(c.code, c.tail); got != c.want {
			t.Errorf("acpExitReason(%d, %q) = %q, want %q", c.code, c.tail, got, c.want)
		}
	}
	long := acpExitReason(1, strings.Repeat("é", 200))
	if len(long) > 123 || !utf8.ValidString(long) {
		t.Errorf("a close reason must be at most 123 bytes of UTF-8: %d bytes, valid %v", len(long), utf8.ValidString(long))
	}
	if got := clipReason(strings.Repeat("a", 200), 123); len(got) != 123 {
		t.Errorf("clip = %d bytes", len(got))
	}
	if got := clipReason("ééé", 5); got != "éé" {
		t.Errorf("clip on a rune boundary = %q", got)
	}
}
