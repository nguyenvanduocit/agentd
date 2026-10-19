package tray

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/pagemcp"
)

func decode(line string, v any) error { return json.Unmarshal([]byte(line), v) }

var claude = pagemcp.AgentInfo{Name: "claude", Title: "Claude Code", Ready: true, Reason: ""}

var baseFacts = Facts{Version: "1", GatewayPort: 47615, Updates: true, Updatable: true}

func TestNewStateIsTheStateMessageTheTrayReads(t *testing.T) {
	got := NewState(Facts{Version: "1.2.3", Apps: []string{"https://a"}, Browsers: []string{"http://localhost:5173"}, Sessions: 2, Managed: true, GatewayPort: 47615, LogFile: "/h/logs/daemon.log", Agents: []pagemcp.AgentInfo{claude}, Updates: false, Updatable: true})
	want := State{T: "state", Version: "1.2.3", Apps: []string{"https://a"}, Pages: []string{"http://localhost:5173"}, Sessions: 2, Managed: true, GatewayOff: false, Log: true, Agents: []pagemcp.AgentInfo{claude}, Updates: false, Updatable: true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestNewStateSaysWhenNoPortWasFreeForTheGateway(t *testing.T) {
	f := baseFacts
	f.GatewayPort = 0
	if !NewState(f).GatewayOff {
		t.Fatal("gatewayOff is false")
	}
}

func TestNewStateSaysWhenThereIsNoLogFileToOpen(t *testing.T) {
	if NewState(baseFacts).Log {
		t.Fatal("log is true: the daemon writes to its terminal")
	}
}

func TestStateLineHasTheFieldsTheTrayReadsInOrder(t *testing.T) {
	// Lists are [] and never null: tray/menu.go ranges over them, and an app may not.
	want := `{"t":"state","version":"1","apps":[],"pages":[],"sessions":0,"managed":false,"gatewayOff":false,"log":false,"agents":[],"updates":true,"updatable":true}`
	if got := NewState(baseFacts).line(); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestStateLineLeavesHTMLCharactersAsTheyAre(t *testing.T) {
	f := baseFacts
	f.Apps = []string{"https://a.example/?x=1&y=<2>"}
	if got := NewState(f).line(); !strings.Contains(got, `"https://a.example/?x=1&y=<2>"`) {
		t.Fatalf("escaped: %s", got)
	}
}

func TestAnAgentCarriesOnlyWhatTheWindowShows(t *testing.T) {
	line := NewState(Facts{Agents: []pagemcp.AgentInfo{claude}}).line()
	var m struct{ Agents []map[string]any }
	if err := decode(line, &m); err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for k := range m.Agents[0] {
		keys[k] = true
	}
	if !reflect.DeepEqual(keys, map[string]bool{"name": true, "title": true, "ready": true, "reason": true}) {
		t.Fatalf("keys %v", keys)
	}
}

func TestParseLineReadsWhatAUserCanChooseInTheMenuAndTheSettingsWindow(t *testing.T) {
	cases := map[string]Action{
		`{"t":"action","id":"logs"}`:                                   {Kind: ActionLogs},
		`{"t":"action","id":"restart"}`:                                {Kind: ActionRestart},
		`{"t":"action","id":"stop"}`:                                   {Kind: ActionStop},
		`{"t":"action","id":"disconnect:https://a.example"}`:           {Kind: ActionDisconnect, Origin: "https://a.example"},
		`{"t":"action","id":"updates:on"}`:                             {Kind: ActionUpdates, On: true},
		`{"t":"action","id":"updates:off"}`:                            {Kind: ActionUpdates, On: false},
		`{"id":"stop","t":"action","extra":1}`:                         {Kind: ActionStop},
		`{"t":"action","id":"disconnect:http://localhost:5173"}`:       {Kind: ActionDisconnect, Origin: "http://localhost:5173"},
		`{"t":"action","id":"disconnect:HTTPS://A.EXAMPLE"}`:           {},
		`{"t":"action","id":"disconnect:https://a.example:443"}`:       {},
		`{"t":"action","id":"disconnect:https://a.example/with/path"}`: {},
	}
	for line, want := range cases {
		got, ok := ParseLine(line)
		if want.Kind == "" {
			// Only an origin in its serialized form is one; the rest is refused.
			if ok {
				t.Errorf("%s: got %+v, want none", line, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%s: got %+v %v, want %+v", line, got, ok, want)
		}
	}
}

func TestParseLineIgnoresAnythingElse(t *testing.T) {
	bad := []string{`{"t":"action","id":"format-disk"}`, `{"t":"action","id":"updates:"}`, `{"t":"action","id":"updates:yes"}`, `{"t":"action","id":"settings"}`, `{"t":"action","id":"disconnect:"}`, `{"t":"action","id":"disconnect:ftp://x"}`, `{"t":"action","id":"disconnect:http://evil.example"}`, `{"t":"state"}`, `{"id":"stop"}`, `{"t":"action","id":7}`, `{"t":"action"}`, `null`, `[]`, `7`, "oops", ""}
	for _, line := range bad {
		if got, ok := ParseLine(line); ok {
			t.Errorf("%q: got %+v, want none", line, got)
		}
	}
}

func TestBinaryIsUnderTrayOsArchBesideTheEngineWithExeOnWindows(t *testing.T) {
	cases := []struct{ dir, goos, goarch, want string }{
		{"/h/daemon/1.0.0", "darwin", "arm64", "/h/daemon/1.0.0/tray/darwin-arm64/agentd-tray"},
		{"/h/daemon/1.0.0", "darwin", "amd64", "/h/daemon/1.0.0/tray/darwin-amd64/agentd-tray"},
		{"/h/daemon/1.0.0", "linux", "amd64", "/h/daemon/1.0.0/tray/linux-amd64/agentd-tray"},
		{"/h/daemon/1.0.0", "linux", "arm64", "/h/daemon/1.0.0/tray/linux-arm64/agentd-tray"},
		{`C:\h\daemon\1.0.0`, "windows", "amd64", `C:\h\daemon\1.0.0\tray\windows-amd64\agentd-tray.exe`},
		{`C:\h\daemon\1.0.0\`, "windows", "amd64", `C:\h\daemon\1.0.0\tray\windows-amd64\agentd-tray.exe`},
	}
	for _, c := range cases {
		if got := Binary(c.dir, c.goos, c.goarch); got != c.want {
			t.Errorf("Binary(%q, %s, %s) = %q, want %q", c.dir, c.goos, c.goarch, got, c.want)
		}
	}
}

func TestAPlatformWithoutABuildHasNoTray(t *testing.T) {
	for _, c := range [][3]string{{"/d", "freebsd", "amd64"}, {"/d", "linux", "386"}, {`C:\d`, "windows", "arm64"}} {
		if got := Binary(c[0], c[1], c[2]); got != "" {
			t.Errorf("Binary(%v) = %q, want none", c, got)
		}
	}
}

func TestOpenCommandOpensAFileWithThePlatformsOwnOpener(t *testing.T) {
	cases := []struct {
		goos, file string
		want       Command
	}{
		{"darwin", "/l/daemon.log", Command{File: "open", Args: []string{"/l/daemon.log"}}},
		{"linux", "/l/daemon.log", Command{File: "xdg-open", Args: []string{"/l/daemon.log"}}},
		{"windows", `C:\l\daemon.log`, Command{File: "cmd", Args: []string{"/c", "start", "", `C:\l\daemon.log`}}},
	}
	for _, c := range cases {
		if got := OpenCommand(c.goos, c.file); !reflect.DeepEqual(got, c.want) {
			t.Errorf("OpenCommand(%s) = %+v, want %+v", c.goos, got, c.want)
		}
	}
}

func TestUIFdIsTheDescriptorAnAppThatStartedTheDaemonTalksToItOn(t *testing.T) {
	cases := []struct {
		env home.Env
		fd  int
		ok  bool
	}{{home.Env{}, 0, false}, {home.Env{"AGENTD_UI_FD": ""}, 0, false}, {home.Env{"AGENTD_UI_FD": "3"}, 3, true}, {home.Env{"AGENTD_UI_FD": "7"}, 7, true}}
	for _, c := range cases {
		fd, ok, err := UIFd(c.env)
		if err != nil || fd != c.fd || ok != c.ok {
			t.Errorf("UIFd(%v) = %d, %v, %v; want %d, %v", c.env, fd, ok, err, c.fd, c.ok)
		}
	}
}

func TestUIFdRefusesWhatIsNotADescriptorOfItsOwn(t *testing.T) {
	for _, bad := range []string{"0", "1", "2", "-3", "3.5", "x"} {
		if _, _, err := UIFd(home.Env{"AGENTD_UI_FD": bad}); err == nil || !strings.Contains(err.Error(), "AGENTD_UI_FD") {
			t.Errorf("%q: err %v, want one naming AGENTD_UI_FD", bad, err)
		}
	}
}

func TestFileIsTheSeamThePackagedTrayOrNone(t *testing.T) {
	exists := func(p string) bool { return p == "/d/tray/linux-amd64/agentd-tray" }
	cases := []struct {
		name, seam, dir, want string
	}{
		{"off starts none", "off", "/d", ""},
		{"a path is the tray", "/x/tray", "/d", "/x/tray"},
		{"the packaged one", "", "/d", "/d/tray/linux-amd64/agentd-tray"},
		{"the packaged one is not there", "", "/e", ""},
	}
	for _, c := range cases {
		env := home.Env{}
		if c.seam != "" {
			env["AGENTD_TRAY"] = c.seam
		}
		if got := File(env, c.dir, "linux", "amd64", exists); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
	if got := File(home.Env{}, "/d", "freebsd", "amd64", func(string) bool { return true }); got != "" {
		t.Errorf("a platform without a build: got %q", got)
	}
}
