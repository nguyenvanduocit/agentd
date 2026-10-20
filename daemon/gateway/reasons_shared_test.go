package gateway

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/registry"
)

// sharedReasons is contract/testdata/page-reasons.json, which the page SDKs test themselves against too.
type sharedReasons struct {
	Reasons []struct {
		Reason string `json:"reason"`
		Legacy string `json:"legacy"`
		Text   string `json:"text"`
	} `json:"reasons"`
	ClientReasons []struct {
		Reason string `json:"reason"`
		Text   string `json:"text"`
	} `json:"clientReasons"`
}

func loadSharedReasons(t *testing.T) sharedReasons {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("..", "..", "contract", "testdata", "page-reasons.json"))
	if err != nil {
		t.Fatal(err)
	}
	var shared sharedReasons
	if err := json.Unmarshal(text, &shared); err != nil {
		t.Fatal(err)
	}
	return shared
}

func TestTheSharedFileNamesExactlyTheReasonsTheGatewayCanSend(t *testing.T) {
	shared := loadSharedReasons(t)
	var inFile []string
	for _, e := range shared.Reasons {
		inFile = append(inFile, e.Reason)
		if e.Text == "" {
			t.Errorf("%s has no sentence", e.Reason)
		}
		legacy, refines := LegacyOf(Reason(e.Reason))
		if string(legacy) != e.Legacy || refines != (e.Legacy != "") {
			t.Errorf("%s: the file says it refines %q, the gateway says %q (%v)", e.Reason, e.Legacy, legacy, refines)
		}
	}
	for _, e := range shared.Reasons {
		if e.Legacy != "" && !slices.Contains(inFile, e.Legacy) {
			t.Errorf("%s refines %q, which the file does not list", e.Reason, e.Legacy)
		}
	}
	var sends []string
	for _, r := range Reasons() {
		sends = append(sends, string(r))
	}
	slices.Sort(inFile)
	slices.Sort(sends)
	if !slices.Equal(inFile, sends) {
		t.Errorf("the gateway can send %v, the shared file lists %v: change both, and the SDKs' tests will tell you what else", sends, inFile)
	}
	for _, c := range shared.ClientReasons {
		if slices.Contains(sends, c.Reason) {
			t.Errorf("%s is a reason of the client's own, yet the gateway sends it", c.Reason)
		}
	}
}

// triggers makes the gateway answer with each Reason, through the real handler.
func triggers() map[Reason]func(t *testing.T) reply {
	browser := map[string]any{"mode": "browser"}
	denying := func() *world {
		w := newWorld()
		w.ask = func(consent.Question) consent.Answer { return consent.AnswerDeny }
		return w
	}
	return map[Reason]func(t *testing.T) reply{
		ReasonPeer: func(t *testing.T) reply {
			g := startGateway(t, newWorld().deps(), func(consent.Connection) ([]int, error) { return []int{-1}, nil })
			return call(t, g.Port, "GET", "/v1/status", callOpts{origin: page})
		},
		ReasonHost: func(t *testing.T) reply {
			g := startGateway(t, newWorld().deps(), nil)
			return call(t, g.Port, "GET", "/v1/status", callOpts{origin: page, host: "evil.example"})
		},
		ReasonOrigin: func(t *testing.T) reply {
			g := startGateway(t, newWorld().deps(), nil)
			return call(t, g.Port, "GET", "/v1/status", callOpts{})
		},
		ReasonNotFound: func(t *testing.T) reply {
			g := startGateway(t, newWorld().deps(), nil)
			return call(t, g.Port, "GET", "/v1/nothing", callOpts{origin: page})
		},
		ReasonError: func(t *testing.T) reply {
			deps := newWorld().deps()
			deps.Registry = func() (registry.Registry, error) { return registry.Registry{}, errors.New("daemon.json is damaged") }
			g := startGateway(t, deps, nil)
			return call(t, g.Port, "GET", "/v1/status", callOpts{origin: page})
		},
		ReasonMalformed: func(t *testing.T) reply {
			g := startGateway(t, newWorld().deps(), nil)
			return connectAs(t, g.Port, map[string]any{"mode": "server"}, page)
		},
		ReasonDenied: func(t *testing.T) reply {
			g := startGateway(t, denying().deps(), nil)
			return connectAs(t, g.Port, browser, page)
		},
		ReasonTimeout: func(t *testing.T) reply {
			w := newWorld()
			w.ask = func(consent.Question) consent.Answer { return consent.AnswerTimeout }
			g := startGateway(t, w.deps(), nil)
			return connectAs(t, g.Port, browser, page)
		},
		ReasonCooldown: func(t *testing.T) reply {
			g := startGateway(t, denying().deps(), nil)
			connectAs(t, g.Port, browser, page)
			return connectAs(t, g.Port, browser, page)
		},
		ReasonLocked: func(t *testing.T) reply {
			g := startGateway(t, denying().deps(), nil)
			for i := range 3 {
				connectAs(t, g.Port, browser, "http://localhost:"+strconv.Itoa(5171+i))
			}
			return connectAs(t, g.Port, browser, "http://localhost:5179")
		},
		ReasonBusy: func(t *testing.T) reply {
			w := newWorld()
			asking, release := make(chan struct{}, 1), make(chan struct{})
			w.ask = func(consent.Question) consent.Answer {
				asking <- struct{}{}
				<-release
				return consent.AnswerDeny
			}
			g := startGateway(t, w.deps(), nil)
			first := make(chan reply, 1)
			go func() { first <- connectAs(t, g.Port, browser, page) }()
			<-asking
			r := connectAs(t, g.Port, browser, "http://localhost:5179")
			close(release)
			<-first
			return r
		},
		ReasonNoDialog: func(t *testing.T) reply {
			w := newWorld()
			w.ask = func(consent.Question) consent.Answer { return consent.AnswerUnavailable }
			g := startGateway(t, w.deps(), nil)
			return connectAs(t, g.Port, browser, page)
		},
		ReasonNoFolder: func(t *testing.T) reply {
			w := newWorld()
			w.chosen = []string{}
			g := startGateway(t, w.deps(), nil)
			return connectAs(t, g.Port, browser, page)
		},
		ReasonPairing: func(t *testing.T) reply {
			deps := newWorld().deps()
			deps.ConnectServer = func(server, _ string, _ []string) error { return errors.New(server + " refused the code") }
			g := startGateway(t, deps, nil)
			return connectAs(t, g.Port, map[string]any{"mode": "server", "code": "WRONG"}, "https://app.example.com")
		},
		ReasonNotSaved: func(t *testing.T) reply {
			deps := newWorld().deps()
			deps.GrantBrowser = func(string, []string) error { return errors.New("disk full") }
			g := startGateway(t, deps, nil)
			return connectAs(t, g.Port, browser, page)
		},
	}
}

// Every Reason the gateway can send is reached through the handler, and arrives as its legacy `reason` with its own
// `code` when it refines one, as itself otherwise.
func TestEveryReasonArrivesAsItsLegacyReasonWithItsOwnCode(t *testing.T) {
	trigger := triggers()
	carriesText := map[Reason]bool{ReasonError: true, ReasonPairing: true, ReasonNotSaved: true} // the refusals that have an error to tell
	waits := map[Reason]bool{ReasonCooldown: true, ReasonLocked: true}
	for _, r := range Reasons() {
		t.Run(string(r), func(t *testing.T) {
			run, ok := trigger[r]
			if !ok {
				t.Fatalf("no way to make the gateway send %q: add one to triggers()", r)
			}
			got := run(t)
			legacy, refines := LegacyOf(r)
			wantReason, wantCode := r, Reason("")
			if refines {
				wantReason, wantCode = legacy, r
			}
			if got.body["reason"] != string(wantReason) {
				t.Errorf("reason = %v, want %q (body %v)", got.body["reason"], wantReason, got.body)
			}
			if code, has := got.body["code"]; has != refines || (refines && code != string(wantCode)) {
				t.Errorf("code = %v (present %v), want %q (body %v)", code, has, wantCode, got.body)
			}
			if got.body["granted"] == true {
				t.Errorf("a refusal that grants: %v", got.body)
			}
			if message, _ := got.body["message"].(string); carriesText[r] && message == "" {
				t.Errorf("no message, though the refusal has an error to tell: %v", got.body)
			}
			retry, has := got.body["retryAfter"].(float64)
			if has != waits[r] || (has && retry < 1) {
				t.Errorf("retryAfter = %v (present %v), want one only on %v (body %v)", retry, has, waits, got.body)
			}
			if (got.header.Get("Retry-After") != "") != waits[r] {
				t.Errorf("Retry-After = %q", got.header.Get("Retry-After"))
			}
		})
	}
}
