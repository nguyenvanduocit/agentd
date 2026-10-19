package link

import (
	"testing"
)

// shape is a packet as a test writes it.
type shape struct {
	kind byte
	id   int
	data string
}

func TestParsePacket(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ns   string
		want shape
		ok   bool
		fail bool
	}{
		{"connect answer", `0/agentd,{"sid":"x"}`, "/agentd", shape{kind: '0', id: -1, data: `{"sid":"x"}`}, true, false},
		{"connect refusal", `4/agentd,{"message":"unauthorized"}`, "/agentd", shape{kind: '4', id: -1, data: `{"message":"unauthorized"}`}, true, false},
		{"disconnect", `1/agentd,`, "/agentd", shape{kind: '1', id: -1}, true, false},
		{"disconnect with no comma", `1/agentd`, "/agentd", shape{kind: '1', id: -1}, true, false},
		{"event without an id", `2/agentd,["close","s1"]`, "/agentd", shape{kind: '2', id: -1, data: `["close","s1"]`}, true, false},
		{"event with an id", `2/agentd,17["open","s1","claude"]`, "/agentd", shape{kind: '2', id: 17, data: `["open","s1","claude"]`}, true, false},
		{"ack", `3/agentd,0[{"protocol":1}]`, "/agentd", shape{kind: '3', id: 0, data: `[{"protocol":1}]`}, true, false},
		{"main namespace event", `2["x"]`, "/", shape{kind: '2', id: -1, data: `["x"]`}, true, false},
		{"another namespace", `2/other,["x"]`, "/agentd", shape{}, false, false},
		{"binary event", `5-/agentd,["x"]`, "/agentd", shape{}, false, true},
		{"empty", ``, "/agentd", shape{}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := parsePacket(tc.in, tc.ns)
			if (err != nil) != tc.fail || ok != tc.ok {
				t.Fatalf("parsePacket(%q) = ok %v, err %v", tc.in, ok, err)
			}
			if ok && (got.kind != tc.want.kind || got.id != tc.want.id || string(got.data) != tc.want.data) {
				t.Fatalf("parsePacket(%q) = %q %d %q, want %q %d %q", tc.in, got.kind, got.id, got.data, tc.want.kind, tc.want.id, tc.want.data)
			}
		})
	}
}

func TestEncodePacket(t *testing.T) {
	cases := []struct {
		name   string
		kind   byte
		ns     string
		id     int
		values []any
		want   string
	}{
		{"event", '2', "/agentd", -1, []any{"frame", "s1", "out", "x", 0}, `42/agentd,["frame","s1","out","x",0]`},
		{"event with an id", '2', "/agentd", 3, []any{"hello", map[string]any{"a": 1}}, `42/agentd,3["hello",{"a":1}]`},
		{"ack", '3', "/agentd", 5, []any{true}, `43/agentd,5[true]`},
		{"ack of nothing", '3', "/agentd", 5, nil, `43/agentd,5null`},
		{"main namespace", '2', "/", -1, []any{"x"}, `42["x"]`},
		{"html is not escaped", '2', "/agentd", -1, []any{"x", "<a href=\"b\">&</a>"}, `42/agentd,["x","<a href=\"b\">&</a>"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := encodePacket(tc.kind, tc.ns, tc.id, tc.values)
			if err != nil || got != tc.want {
				t.Fatalf("encodePacket = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
	if _, err := encodePacket('2', "/agentd", -1, []any{"x", make(chan int)}); err == nil {
		t.Fatal("a value JSON cannot carry was encoded")
	}
}

func TestRefusalMessage(t *testing.T) {
	cases := map[string]string{
		`{"message":"unauthorized","data":{"code":"unauthorized"}}`: "unauthorized",
		`{"message":"unavailable"}`:                                 "unavailable",
		`"plain"`:                                                   "plain",
		`{"data":1}`:                                                `{"data":1}`,
	}
	for in, want := range cases {
		if got := refusalMessage([]byte(in)); got != want {
			t.Errorf("refusalMessage(%s) = %q, want %q", in, got, want)
		}
	}
}
