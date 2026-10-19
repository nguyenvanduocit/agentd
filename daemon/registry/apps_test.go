package registry

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

const twoAppsOneBrowser = `{"apps":[{"server":"https://a.example","token":"ta"},{"server":"https://b.example","token":"tb"}],` +
	`"browsers":[{"origin":"https://c.example","grantedAt":"2026-10-19T00:00:00.000Z"}]}`

func TestRunAppsListsEveryAppAndBrowserGrantWithoutTokens(t *testing.T) {
	rig := newAppsRig(t, twoAppsOneBrowser)
	exit, err := RunApps(cli.Apps{Action: cli.AppsList}, rig.host, rig.log)
	if err != nil || exit != 0 {
		t.Fatalf("RunApps = %d, %v", exit, err)
	}
	want := "https://a.example  app\nhttps://b.example  app\nhttps://c.example  browser, granted 2026-10-19\n"
	if rig.out.String() != want {
		t.Errorf("output = %q, want %q", rig.out.String(), want)
	}
}

func TestRunAppsListOfNothingPairedPrintsNothing(t *testing.T) {
	rig := newAppsRig(t, "")
	exit, err := RunApps(cli.Apps{Action: cli.AppsList}, rig.host, rig.log)
	if err != nil || exit != 0 || rig.out.Len() != 0 {
		t.Errorf("RunApps = %d, %v, output %q", exit, err, rig.out.String())
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
	if _, err := RunApps(cli.Apps{Action: cli.AppsList, Config: &cfg}, rig.host, rig.log); err != nil || !strings.Contains(rig.out.String(), "https://a.example  app") {
		t.Errorf("err = %v, output %q", err, rig.out.String())
	}
}

func TestRunAppsFailsOnABrokenRegistryAndABadOrigin(t *testing.T) {
	rig := newAppsRig(t, "{oops")
	if _, err := RunApps(cli.Apps{Action: cli.AppsList}, rig.host, rig.log); err == nil || !strings.Contains(err.Error(), rig.file+" is not valid JSON") {
		t.Errorf("error = %v", err)
	}
	rig = newAppsRig(t, twoAppsOneBrowser)
	if _, err := RunApps(cli.Apps{Action: cli.AppsRemove, Origin: "nonsense"}, rig.host, rig.log); err == nil || !strings.Contains(err.Error(), "is not a URL") {
		t.Errorf("error = %v", err)
	}
}
