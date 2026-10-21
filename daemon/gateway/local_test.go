package gateway

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
	"github.com/nguyenvanduocit/agentd/daemon/home"
)

func TestHostAllowedOnlyTheLoopbackNamesWithTheGatewaysOwnPort(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1:47615", true},
		{"localhost:47615", true},
		{"LOCALHOST:47615", true},
		{"evil.example", false},
		{"evil.example:47615", false},
		{"127.0.0.1:1", false},
		{"127.0.0.1", false},
		{"", false},
	}
	for _, c := range cases {
		if got := HostAllowed(c.host, 47615); got != c.want {
			t.Errorf("HostAllowed(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

func TestOriginOfAcceptsHTTPSOriginsAndHTTPOnALoopbackHost(t *testing.T) {
	for _, ok := range []string{"https://mind.aiocean.io", "http://localhost:5173", "http://127.0.0.1:3000", "http://[::1]:3000"} {
		if got, accepted := OriginOf(ok); !accepted || got != ok {
			t.Errorf("OriginOf(%q) = %q, %v; want it as it is", ok, got, accepted)
		}
	}
}

func TestOriginOfRejectsWhatIsNotAnOriginOrNotTrusted(t *testing.T) {
	bad := []string{
		"", "null", "https://a.b/x", "https://a.b/", "http://evil.example", "ftp://a.b", "chrome-extension://abc", "nonsense",
		"HTTPS://a.b", "https://A.B", "https://u@a.b", "https://a.b:443", "https://a.b:0443", "https://a.b:99999", "https://a.b?x=1",
		"https://münchen.de",
	}
	for _, header := range bad {
		if got, accepted := OriginOf(header); accepted {
			t.Errorf("OriginOf(%q) = %q, want it refused", header, got)
		}
	}
}

func TestCORSHeadersReflectTheOriginAndVaryOnIt(t *testing.T) {
	h := CORSHeaders("https://a.b", http.Header{})
	if h["Access-Control-Allow-Origin"] != "https://a.b" || h["Vary"] != "Origin" {
		t.Errorf("headers = %v", h)
	}
	if _, set := h["Access-Control-Allow-Private-Network"]; set {
		t.Error("Private Network Access answered without being asked")
	}
}

func TestCORSHeadersAnswerAPrivateNetworkAccessPreflight(t *testing.T) {
	req := http.Header{}
	req.Set("access-control-request-private-network", "true")
	if h := CORSHeaders("https://a.b", req); h["Access-Control-Allow-Private-Network"] != "true" {
		t.Errorf("headers = %v", h)
	}
}

func TestParseConnect(t *testing.T) {
	good := map[string]ConnectRequest{
		`{"mode":"server","code":"ABCD","name":"AIOcean"}`: {Mode: consent.ModeServer, Code: "ABCD", Name: "AIOcean"},
		`{"mode":"browser","name":"Docs"}`:                 {Mode: consent.ModeBrowser, Name: "Docs"},
	}
	for body, want := range good {
		if got, ok := ParseConnect([]byte(body)); !ok || got != want {
			t.Errorf("ParseConnect(%s) = %v, %v; want %v", body, got, ok, want)
		}
	}
	// Server mode needs a code and names no server: the app is the page's own origin, so a page cannot connect the
	// daemon to an origin that is not its own. Every connect names the app.
	for _, body := range []string{
		`{"mode":"server","name":"A"}`, `{"mode":"server","code":"","name":"A"}`, `{"mode":"server","code":5,"name":"A"}`,
		`{"mode":"server","code":"ABCD","server":"https://api.x","name":"A"}`, `{"mode":"server","code":"ABCD","server":null,"name":"A"}`,
		`{"mode":"root","name":"A"}`, `null`, `"x"`, `[]`, ``, `{`, `{"mode":"browser","name":"A"} x`,
		`{"mode":"browser"}`, `{"mode":"server","code":"ABCD"}`,
		`{"mode":"browser","name":""}`, `{"mode":"browser","name":"   "}`, `{"mode":"browser","name":5}`, `{"mode":"browser","name":null}`,
		`{"mode":"browser","name":"\u0000\u202e"}`,
	} {
		if got, ok := ParseConnect([]byte(body)); ok {
			t.Errorf("ParseConnect(%s) = %v, want malformed", body, got)
		}
	}
}

// The name goes to a window and a menu; the app chooses it, so what could hide or reshape text is cut out of it.
func TestCleanName(t *testing.T) {
	long := strings.Repeat("é", maxNameRunes+10)
	for in, want := range map[string]string{
		"AIOcean":         "AIOcean",
		"  AI \t Ocean\n": "AI Ocean",
		"A\u0000B\u0007C": "ABC",
		"safe\u202eevil":  "safeevil", // a right-to-left override reorders what follows it
		"emoji 🚀 ok":      "emoji 🚀 ok",
		"日本語アプリ":          "日本語アプリ",
		long:              strings.Repeat("é", maxNameRunes),
		"\u200b":          "",
		"   ":             "",
	} {
		if got := CleanName(in); got != want {
			t.Errorf("CleanName(%q) = %q, want %q", in, got, want)
		}
	}
}

type clock struct{ now time.Time }

func (c *clock) read() time.Time { return c.now }

func TestCooldownBlocksAnOriginForThePeriodAndOnlyThatOrigin(t *testing.T) {
	c := &clock{now: time.Unix(1, 0)}
	cd := NewCooldown(30*time.Second, c.read)
	if cd.Blocked("https://a.b") {
		t.Fatal("blocked before block()")
	}
	cd.Block("https://a.b")
	if !cd.Blocked("https://a.b") || cd.Blocked("https://c.d") {
		t.Fatal("block() does not hold for its origin only")
	}
	c.now = c.now.Add(29*time.Second + 999*time.Millisecond)
	if !cd.Blocked("https://a.b") {
		t.Fatal("not blocked 1 ms before the end")
	}
	c.now = c.now.Add(time.Millisecond)
	if cd.Blocked("https://a.b") {
		t.Fatal("still blocked at the end")
	}
}

func TestGatewayPort(t *testing.T) {
	for env, want := range map[string]int{"": DefaultPort, "0": 0, "53100": 53100, "65526": 65526} {
		got, err := GatewayPort(home.Env{"AGENTD_LOCAL_PORT": env})
		if err != nil || got != want {
			t.Errorf("GatewayPort(%q) = %d, %v; want %d", env, got, err, want)
		}
	}
	if got, err := GatewayPort(home.Env{}); err != nil || got != DefaultPort {
		t.Errorf("GatewayPort of no variable = %d, %v", got, err)
	}
	// Not a port, or no room for the 10-port range.
	for _, bad := range []string{"abc", "-1", "70000", "65530", "1.5"} {
		_, err := GatewayPort(home.Env{"AGENTD_LOCAL_PORT": bad})
		if err == nil || !strings.Contains(err.Error(), "AGENTD_LOCAL_PORT") {
			t.Errorf("GatewayPort(%q) error = %v", bad, err)
		}
	}
}

func TestLockoutThreeRefusalsWithinTenMinutesLockEveryDialogAndAnAllowClearsThem(t *testing.T) {
	c := &clock{now: time.Unix(0, 0)}
	l := NewLockout(3, 10*time.Minute, c.read)
	l.Strike()
	l.Strike()
	if l.Locked() {
		t.Fatal("locked after two")
	}
	l.Strike()
	if !l.Locked() {
		t.Fatal("not locked after three")
	}
	l.Reset()
	if l.Locked() {
		t.Fatal("locked after an Allow")
	}
	c.now = c.now.Add(time.Millisecond)
	l.Strike()
	l.Strike()
	l.Strike()
	if !l.Locked() {
		t.Fatal("not locked after three more")
	}
	c.now = c.now.Add(10 * time.Minute)
	if l.Locked() {
		t.Fatal("still locked when the oldest is ten minutes old")
	}
}

func TestLockoutOldRefusalsDoNotCount(t *testing.T) {
	c := &clock{now: time.Unix(0, 0)}
	l := NewLockout(3, 10*time.Minute, c.read)
	l.Strike()
	l.Strike()
	c.now = c.now.Add(10*time.Minute + time.Millisecond)
	l.Strike()
	if l.Locked() {
		t.Fatal("locked by two refusals out of the window")
	}
}
