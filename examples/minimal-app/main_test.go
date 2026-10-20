package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/sdk/goserver"
)

// -agentd-ports pins the ports the page looks for a daemon on, for a second daemon beside your own.
func TestPortsFlag(t *testing.T) {
	for in, want := range map[string][]int{
		"":              nil, // the SDK's own ten, 47615-47624
		"47700-47702":   {47700, 47701, 47702},
		"5000":          {5000},
		" 47700-47701 ": {47700, 47701},
	} {
		got, err := parsePorts(in)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("parsePorts(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"x", "5-3", "0-5", "1-70000", "47700-47900", "47700-", "-47700", "1,2"} {
		if got, err := parsePorts(in); err == nil {
			t.Errorf("parsePorts(%q) = %v, want an error", in, got)
		}
	}
}

func TestConfigTellsThePageWhereToLookForAgentd(t *testing.T) {
	for _, c := range []struct {
		ports []int
		want  string
	}{
		{nil, `{"agentdUrl":"http://127.0.0.1:7100"}`},
		{[]int{47700, 47701}, `{"agentdUrl":"http://127.0.0.1:7100","ports":[47700,47701]}`},
	} {
		mux := http.NewServeMux()
		(&app{server: goserver.New(goserver.Options{Tokens: goserver.NewMemoryTokens()}), agentdURL: "http://127.0.0.1:7100", ports: c.ports}).routes(mux)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/config", nil))
		var got, want any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%q: %v", rec.Body.String(), err)
		}
		_ = json.NewDecoder(strings.NewReader(c.want)).Decode(&want)
		if rec.Code != http.StatusOK || !reflect.DeepEqual(got, want) {
			t.Errorf("ports %v: /api/config = %d %s, want %s", c.ports, rec.Code, strings.TrimSpace(rec.Body.String()), c.want)
		}
	}
}

// The page's Remove button relies on this: an unknown computer is 404 with a sentence in {"error": ...}, which is how
// it tells "already gone" from a failure.
func TestRemovingAnUnknownComputerIs404WithASentence(t *testing.T) {
	mux := http.NewServeMux()
	(&app{server: goserver.New(goserver.Options{Tokens: goserver.NewMemoryTokens()})}).routes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/daemons/no-such-computer", nil))
	var body struct{ Error string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%q: %v", rec.Body.String(), err)
	}
	if rec.Code != http.StatusNotFound || body.Error == "" {
		t.Errorf("DELETE of an unknown computer = %d %s, want 404 with an error sentence", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
}
