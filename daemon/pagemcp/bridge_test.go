package pagemcp

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPageMcpServerIsThePagesBridgeByAbsolutePathWithNoArgumentsTheSocketAndTheTokenGoInEnv(t *testing.T) {
	got := PageMcpServer("/tmp/agentd-abc123/bin/page/agentd-mcp", "/tmp/agentd-abc123/mcp.sock", "tok")
	want := McpServer{
		Command: "/tmp/agentd-abc123/bin/page/agentd-mcp",
		Args:    []string{},
		Env: []EnvVar{
			{Name: McpSocketEnv, Value: "/tmp/agentd-abc123/mcp.sock"},
			{Name: McpTokenEnv, Value: "tok"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v; want %+v", got, want)
	}
	text, _ := json.Marshal(got)
	if want := `"args":[]`; !strings.Contains(string(text), want) {
		t.Errorf("%s lacks %s: a null args is not a list", text, want)
	}
}

func TestTheHelloCarriesTheTokenAndOnlyANonEmptyStringIsOne(t *testing.T) {
	if got, ok := ParseBridgeHello(BridgeHello("tok")); !ok || got != "tok" {
		t.Errorf("got %q, %v; want tok", got, ok)
	}
	for _, bad := range []string{`{"token":""}`, `{"token":5}`, `{}`, `null`, `tok`, ``} {
		if got, ok := ParseBridgeHello(bad); ok {
			t.Errorf("ParseBridgeHello(%q) = %q; want a refusal", bad, got)
		}
	}
}

func TestTheAnswerIsOkOrARefusalWithItsReasonAnythingElseIsARefusalThatSaysSo(t *testing.T) {
	cases := []struct {
		line string
		want BridgeAnswer
	}{
		{BridgeOK(), BridgeAnswer{OK: true}},
		{BridgeRefusal("no session has this token"), BridgeAnswer{Message: "no session has this token"}},
		{`{"jsonrpc":"2.0"}`, BridgeAnswer{Message: `the daemon answered something else: {"jsonrpc":"2.0"}`}},
		{`{"ok":false}`, BridgeAnswer{Message: `the daemon answered something else: {"ok":false}`}},
		{`null`, BridgeAnswer{Message: `the daemon answered something else: null`}},
		{`oops`, BridgeAnswer{Message: `the daemon answered something else: oops`}},
	}
	for _, c := range cases {
		if got := ParseBridgeAnswer(c.line); got != c.want {
			t.Errorf("ParseBridgeAnswer(%s) = %+v; want %+v", c.line, got, c.want)
		}
	}
}

func TestTheHandshakeLinesAreTheWireOnes(t *testing.T) {
	if got, want := BridgeHello("tok"), `{"token":"tok"}`; got != want {
		t.Errorf("hello %s; want %s", got, want)
	}
	if got, want := BridgeOK(), `{"ok":true}`; got != want {
		t.Errorf("ok %s; want %s", got, want)
	}
	if got, want := BridgeRefusal("no"), `{"ok":false,"message":"no"}`; got != want {
		t.Errorf("refusal %s; want %s", got, want)
	}
}
