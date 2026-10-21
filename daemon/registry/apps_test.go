package registry

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/cli"
	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

type appsRig struct {
	host        home.Host
	out, errOut *bytes.Buffer
	log         *proc.Logger
	file        string
}

func newAppsRig(t *testing.T, content string) appsRig {
	t.Helper()
	dir := t.TempDir()
	h := home.Host{Platform: "linux", Env: home.Env{"XDG_CONFIG_HOME": dir}, Home: "/nowhere"}
	file := home.ConfigFile(h)
	if content != "" {
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return appsRig{host: h, out: out, errOut: errOut, log: proc.NewLogger(out, errOut), file: file}
}

// onDay2 is the day after the grants of these tests were made.
var onDay2 = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

const twoAppsOneBrowser = `{"apps":[{"server":"https://a.example","token":"ta"},{"server":"https://b.example","token":"tb"}],` +
	`"browsers":[{"origin":"https://c.example","grantedAt":"2026-10-19T00:00:00.000Z"}]}`

func TestRunAppsListsEveryAppAndBrowserGrantWithoutTokens(t *testing.T) {
	rig := newAppsRig(t, twoAppsOneBrowser)
	exit, err := RunAppsAt(cli.Apps{Action: cli.AppsList}, rig.host, rig.log, onDay2)
	if err != nil || exit != 0 {
		t.Fatalf("RunApps = %d, %v", exit, err)
	}
	want := "https://a.example  app, any folder\nhttps://b.example  app, any folder\nhttps://c.example  browser, granted 2026-10-19, expires 2026-11-18, any folder\n"
	if rig.out.String() != want {
		t.Errorf("output = %q, want %q", rig.out.String(), want)
	}
}

func TestRunAppsListOfNothingPairedSaysNothingIsConnected(t *testing.T) {
	for name, content := range map[string]string{"no file": "", "an empty registry": `{"apps":[]}`} {
		rig := newAppsRig(t, content)
		exit, err := RunApps(cli.Apps{Action: cli.AppsList}, rig.host, rig.log)
		if want := "Nothing is connected. An app asks to connect from its own page.\n"; err != nil || exit != 0 || rig.out.String() != want || rig.errOut.Len() != 0 {
			t.Errorf("%s: RunApps = %d, %v, stdout %q, stderr %q", name, exit, err, rig.out.String(), rig.errOut.String())
		}
	}
}

func TestRunAppsRemovesOneOriginAndSaysSoOnlyOnce(t *testing.T) {
	rig := newAppsRig(t, twoAppsOneBrowser)
	exit, err := RunApps(cli.Apps{Action: cli.AppsRemove, Origin: "https://a.example"}, rig.host, rig.log)
	if err != nil || exit != 0 {
		t.Fatalf("RunApps = %d, %v", exit, err)
	}
	if want := "disconnected https://a.example; a running daemon ends its sessions within a few seconds\n"; rig.out.String() != want {
		t.Errorf("output = %q, want %q", rig.out.String(), want)
	}
	saved, err := Load(rig.file)
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, saved.Apps, []App{appB})
	wantEqual(t, saved.Browsers, []BrowserGrant{{Origin: "https://c.example", GrantedAt: "2026-10-19T00:00:00.000Z"}})

	exit, err = RunApps(cli.Apps{Action: cli.AppsRemove, Origin: "https://a.example"}, rig.host, rig.log)
	if err != nil || exit != 1 {
		t.Fatalf("second remove = %d, %v, want exit 1", exit, err)
	}
	if !strings.Contains(rig.errOut.String(), "not connected to https://a.example") {
		t.Errorf("stderr = %q", rig.errOut.String())
	}
}

func TestRunAppsRemovesABrowserGrant(t *testing.T) {
	rig := newAppsRig(t, twoAppsOneBrowser)
	if exit, err := RunApps(cli.Apps{Action: cli.AppsRemove, Origin: "https://c.example/"}, rig.host, rig.log); err != nil || exit != 0 {
		t.Fatalf("RunApps = %d, %v", exit, err)
	}
	saved, err := Load(rig.file)
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, saved.Browsers, []BrowserGrant{})
	wantEqual(t, saved.Apps, []App{appA, appB})
}

func TestRunAppsUsesTheConfigFileOfTheFlag(t *testing.T) {
	rig := newAppsRig(t, "")
	cfg := filepath.Join(t.TempDir(), "mine.json")
	if err := os.WriteFile(cfg, []byte(twoAppsOneBrowser), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RunApps(cli.Apps{Action: cli.AppsList, Config: &cfg}, rig.host, rig.log); err != nil || !strings.Contains(rig.out.String(), "https://a.example  app, any folder") {
		t.Errorf("err = %v, output %q", err, rig.out.String())
	}
}

func TestRunAppsFailsOnABrokenRegistryAndABadOrigin(t *testing.T) {
	rig := newAppsRig(t, "{oops")
	if _, err := RunApps(cli.Apps{Action: cli.AppsList}, rig.host, rig.log); err == nil || !strings.Contains(err.Error(), rig.file+" is damaged") {
		t.Errorf("error = %v", err)
	}
	rig = newAppsRig(t, twoAppsOneBrowser)
	if _, err := RunApps(cli.Apps{Action: cli.AppsRemove, Origin: "nonsense"}, rig.host, rig.log); err == nil || !strings.Contains(err.Error(), "is not a URL") {
		t.Errorf("error = %v", err)
	}
}

func TestRunAppsListsTheFoldersOfAGrant(t *testing.T) {
	rig := newAppsRig(t, `{"apps":[{"server":"https://a.example","token":"ta","folders":["/work/a","/work/b"]}],`+
		`"browsers":[{"origin":"https://c.example","grantedAt":"2026-10-19T00:00:00.000Z","folders":["/work/c"]}]}`)
	if _, err := RunAppsAt(cli.Apps{Action: cli.AppsList}, rig.host, rig.log, onDay2); err != nil {
		t.Fatal(err)
	}
	want := "https://a.example  app, in /work/a, /work/b\nhttps://c.example  browser, granted 2026-10-19, expires 2026-11-18, in /work/c\n"
	if rig.out.String() != want {
		t.Errorf("output = %q, want %q", rig.out.String(), want)
	}
}

func TestRunAppsSaysWhenAPageGrantHasLapsed(t *testing.T) {
	rig := newAppsRig(t, twoAppsOneBrowser)
	lapsed := time.Date(2026, 11, 18, 0, 0, 1, 0, time.UTC)
	if _, err := RunAppsAt(cli.Apps{Action: cli.AppsList}, rig.host, rig.log, lapsed); err != nil {
		t.Fatal(err)
	}
	want := "https://a.example  app, any folder\nhttps://b.example  app, any folder\nhttps://c.example  browser, expired 2026-11-18, any folder\n"
	if rig.out.String() != want {
		t.Errorf("output = %q, want %q", rig.out.String(), want)
	}
}

func TestRunAppsListsAGrantWithoutAReadableTimeAsExpired(t *testing.T) {
	rig := newAppsRig(t, `{"apps":[],"browsers":[{"origin":"https://c.example","grantedAt":"yesterday"}]}`)
	if _, err := RunAppsAt(cli.Apps{Action: cli.AppsList}, rig.host, rig.log, onDay2); err != nil {
		t.Fatal(err)
	}
	if want := "https://c.example  browser, expired, any folder\n"; rig.out.String() != want {
		t.Errorf("output = %q, want %q", rig.out.String(), want)
	}
}

// A lapsed grant is still in the file; the way to forget it is the way to forget any other.
func TestRunAppsRemovesALapsedBrowserGrant(t *testing.T) {
	rig := newAppsRig(t, twoAppsOneBrowser)
	lapsed := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	exit, err := RunAppsAt(cli.Apps{Action: cli.AppsRemove, Origin: "https://c.example"}, rig.host, rig.log, lapsed)
	if err != nil || exit != 0 {
		t.Fatalf("RunApps = %d, %v; stderr %q", exit, err, rig.errOut.String())
	}
	saved, err := Load(rig.file)
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, saved.Browsers, []BrowserGrant{})
}

func TestRunAppsListsTheNameAndEndOfEachGrant(t *testing.T) {
	rig := newAppsRig(t, `{"apps":[`+
		`{"server":"https://a.example","token":"ta","name":"AIOcean","expiresAt":"2026-10-25T00:00:00Z"},`+
		`{"server":"https://b.example","token":"tb","name":"Notes","expiresAt":"never"},`+
		`{"server":"https://d.example","token":"td","expiresAt":"2026-10-20T00:00:00Z"}],`+
		`"browsers":[{"origin":"https://c.example","grantedAt":"2026-10-19T00:00:00.000Z","name":"Docs","expiresAt":"2026-10-21T00:00:00Z"}]}`)
	if _, err := RunAppsAt(cli.Apps{Action: cli.AppsList}, rig.host, rig.log, onDay2); err != nil {
		t.Fatal(err)
	}
	want := "AIOcean (https://a.example)  app, expires 2026-10-25, any folder\n" +
		"Notes (https://b.example)  app, no end, any folder\n" +
		"https://d.example  app, expired 2026-10-20, any folder\n" +
		"Docs (https://c.example)  browser, granted 2026-10-19, expires 2026-10-21, any folder\n"
	if rig.out.String() != want {
		t.Errorf("output = %q, want %q", rig.out.String(), want)
	}
}

// A lapsed app is still in the file; the way to forget it is the way to forget any other.
func TestRunAppsRemovesALapsedApp(t *testing.T) {
	rig := newAppsRig(t, `{"apps":[{"server":"https://a.example","token":"ta","expiresAt":"2026-10-20T00:00:00Z"}]}`)
	exit, err := RunAppsAt(cli.Apps{Action: cli.AppsRemove, Origin: "https://a.example"}, rig.host, rig.log, onDay2)
	if err != nil || exit != 0 {
		t.Fatalf("RunApps = %d, %v; stderr %q", exit, err, rig.errOut.String())
	}
	saved, err := Load(rig.file)
	if err != nil {
		t.Fatal(err)
	}
	wantEqual(t, saved.Apps, []App{})
}
