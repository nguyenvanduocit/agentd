package pipeline

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// machine is a user's computer: a HOME and an AGENTD_HOME of its own, a fake Node.js, no service touched.
type machine struct {
	home       string
	agentdHome string
	config     string
	fakeBin    string
	path       string // PATH of the processes run on it
}

func newMachine(t *testing.T) *machine {
	t.Helper()
	dir := t.TempDir()
	m := &machine{home: filepath.Join(dir, "home"), agentdHome: filepath.Join(dir, "agentd-home"), config: filepath.Join(dir, "xdg-config"), fakeBin: filepath.Join(dir, "bin")}
	for _, d := range []string{m.home, m.fakeBin, filepath.Join(dir, "tmp")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// The adapters of the agents run on Node.js and install with npm: install.sh asks for a Node >= 20 with npm.
	m.fakeTool(t, "node", "echo v24.0.0")
	m.fakeTool(t, "npm", "echo 10.0.0")
	m.path = m.fakeBin + string(os.PathListSeparator) + os.Getenv("PATH")
	return m
}

func (m *machine) fakeTool(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(m.fakeBin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// uname makes the machine another computer: `uname -s` and `uname -m` answer as given.
func (m *machine) uname(t *testing.T, system, machineName string) {
	t.Helper()
	real, err := exec.LookPath("uname")
	if err != nil {
		t.Skip("no uname")
	}
	script := `case "$1" in -s) echo "` + system + `" ;; -m) echo "` + machineName + `" ;; *) exec ` + real + ` "$@" ;; esac`
	m.fakeTool(t, "uname", script)
}

// pair is what `agentd pair` leaves: the daemon's config file, so install.sh needs no code.
func (m *machine) pair(t *testing.T) {
	t.Helper()
	file := filepath.Join(m.config, "agentd", "daemon.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (m *machine) env() []string {
	return []string{
		"PATH=" + m.path, "HOME=" + m.home, "AGENTD_HOME=" + m.agentdHome, "XDG_CONFIG_HOME=" + m.config,
		"XDG_STATE_HOME=" + filepath.Join(m.home, "xdg-state"), "TMPDIR=" + filepath.Join(filepath.Dir(m.home), "tmp"),
		// Nothing is downloaded from nodejs.org here.
		"AGENTD_NODE_DIST=http://127.0.0.1:1/never",
		// The agents are fakes: nothing installs an adapter from the network.
		"AGENTD_AGENT_CLAUDE=true", "AGENTD_AGENT_CODEX=true",
	}
}

// install runs the command a user pastes: `curl <origin>/latest/download/install.sh | bash -s -- args`.
func (m *machine) install(t *testing.T, origin string, args ...string) (string, int) {
	t.Helper()
	script := `set -o pipefail; curl -fsSL "$0/latest/download/install.sh" | bash -s -- "$@"`
	return run(t, m.env(), "bash", append([]string{"-c", script, origin}, args...)...)
}

// installScript runs a copy of install.sh from a file.
func (m *machine) installScript(t *testing.T, script string, args ...string) (string, int) {
	t.Helper()
	return run(t, m.env(), "bash", append([]string{script}, args...)...)
}

// launcher runs ~/.agentd/bin/agentd, the launcher install.sh put in place.
func (m *machine) launcher(t *testing.T, args ...string) (string, int) {
	t.Helper()
	return run(t, m.env(), filepath.Join(m.agentdHome, "bin", "agentd"), args...)
}

// entries lists AGENTD_HOME: empty when install.sh left nothing behind.
func (m *machine) entries(t *testing.T) []string {
	t.Helper()
	list, err := os.ReadDir(m.agentdHome)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range list {
		names = append(names, e.Name())
	}
	return names
}

func TestInstallLaysOutTheEngineOfThisMachine(t *testing.T) {
	r := buildRelease(t, pipelineVersion)
	engine := hostEngine(t)
	m := newMachine(t)
	m.pair(t)

	out, code := m.install(t, r.url, "--no-service")
	if code != 0 {
		t.Fatalf("install.sh exited %d:\n%s", code, out)
	}
	for _, want := range []string{"using Node.js v24.0.0", "downloading daemon " + pipelineVersion, "no service was installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "installing Node.js") {
		t.Errorf("Node.js was downloaded although the machine has one:\n%s", out)
	}

	versionDir := filepath.Join(m.agentdHome, "daemon", pipelineVersion)
	tarball := readTarball(t, r.tgz)
	installed := filepath.Join(versionDir, "agentd")
	info, err := os.Stat(installed)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
		t.Fatalf("%s is not a regular mode 755 file: %v %v", installed, info, err)
	}
	if !bytes.Equal(mustRead(t, installed), tarball["package/"+engine].body) {
		t.Errorf("daemon/%s/agentd is not %s of the tarball", pipelineVersion, engine)
	}
	if link, err := os.Readlink(filepath.Join(m.agentdHome, "daemon", "current")); err != nil || link != pipelineVersion {
		t.Errorf("current -> %q, %v; want %s", link, err, pipelineVersion)
	}
	var inVersionDir []string
	list, err := os.ReadDir(versionDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range list {
		inVersionDir = append(inVersionDir, e.Name())
	}
	if strings.Join(inVersionDir, " ") != "agentd launcher.sh package.json tray" {
		t.Errorf("the version directory holds %v: the other platforms' engines must not be there", inVersionDir)
	}
	launcher := mustRead(t, filepath.Join(m.agentdHome, "bin", "agentd"))
	if !bytes.HasPrefix(launcher, []byte("#!/bin/sh")) || !bytes.Equal(launcher, tarball["package/launcher.sh"].body) {
		t.Error("~/.agentd/bin/agentd is not the launcher of the tarball")
	}
	for _, name := range m.entries(t) {
		if strings.HasPrefix(name, ".install.") {
			t.Errorf("install.sh left %s behind", name)
		}
		if name == "node" {
			t.Error("Node.js was installed under AGENTD_HOME")
		}
	}
	if n := r.downloaded(); n != 1 {
		t.Errorf("the tarball was downloaded %d times, want 1", n)
	}

	// The installed binary, and the launcher that runs it, are this release.
	if v, code := run(t, m.env(), installed, "version"); code != 0 || strings.TrimSpace(v) != pipelineVersion {
		t.Errorf("agentd version: exit %d, %q", code, v)
	}
	if v, code := m.launcher(t, "version"); code != 0 || strings.TrimSpace(v) != pipelineVersion {
		t.Errorf("launcher version: exit %d, %q", code, v)
	}

	// A second run installs nothing again.
	out, code = m.install(t, r.url, "--no-service")
	if code != 0 || !strings.Contains(out, "daemon "+pipelineVersion+" is already installed") {
		t.Errorf("second run: exit %d:\n%s", code, out)
	}
	if n := r.downloaded(); n != 1 {
		t.Errorf("the second run downloaded the tarball again (%d)", n)
	}
}

// What the server serves as latest.json is what a daemon reads, and the baked key verifies it (build_test.go checks the file).
func TestTheServerServesLatestJSONAndInstallShOfTheLatestRelease(t *testing.T) {
	r := buildRelease(t, pipelineVersion)
	for _, file := range []string{"latest.json", "install.sh"} {
		resp, err := http.Get(r.url + "/latest/download/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var body bytes.Buffer
		_, _ = body.ReadFrom(resp.Body)
		_ = resp.Body.Close()
		want := mustRead(t, filepath.Join(r.dist, "download", "v"+pipelineVersion, file))
		if resp.StatusCode != http.StatusOK || !bytes.Equal(body.Bytes(), want) {
			t.Errorf("GET /latest/download/%s: %d, %d bytes", file, resp.StatusCode, body.Len())
		}
	}
}

func TestInstallPicksTheEngineOfThePlatformUnameNames(t *testing.T) {
	r := buildRelease(t, pipelineVersion)
	tarball := readTarball(t, r.tgz)
	for _, c := range []struct{ system, machine, engine string }{
		{"Darwin", "arm64", "agentd-darwin-arm64"},
		{"Darwin", "x86_64", "agentd-darwin-amd64"},
		{"Linux", "aarch64", "agentd-linux-arm64"},
		{"Linux", "x86_64", "agentd-linux-amd64"},
	} {
		t.Run(c.system+" "+c.machine, func(t *testing.T) {
			m := newMachine(t)
			m.pair(t)
			m.uname(t, c.system, c.machine)
			if out, code := m.install(t, r.url, "--no-service"); code != 0 {
				t.Fatalf("install.sh exited %d:\n%s", code, out)
			}
			got := mustRead(t, filepath.Join(m.agentdHome, "daemon", pipelineVersion, "agentd"))
			if !bytes.Equal(got, tarball["package/"+c.engine].body) {
				t.Errorf("the installed agentd is not %s", c.engine)
			}
		})
	}
}

func TestInstallRefusesWhatItCannotInstallAndWritesNothing(t *testing.T) {
	r := buildRelease(t, pipelineVersion)
	before := r.downloaded()
	for _, c := range []struct {
		name   string
		setup  func(t *testing.T, m *machine)
		args   []string
		expect string
	}{
		{"Windows", func(t *testing.T, m *machine) { m.uname(t, "MINGW64_NT-10.0", "x86_64") }, []string{"--no-service"}, "use WSL"},
		{"another operating system", func(t *testing.T, m *machine) { m.uname(t, "FreeBSD", "amd64") }, []string{"--no-service"}, "FreeBSD is not supported"},
		{"a processor with no build", func(t *testing.T, m *machine) { m.uname(t, "Linux", "riscv64") }, []string{"--no-service"}, "riscv64 is not supported"},
		{"a pairing code of the wrong shape", func(*testing.T, *machine) {}, []string{"--code", "x; touch pwned"}, "pairing code looks wrong"},
		{"no code and no saved pairing", func(*testing.T, *machine) {}, []string{"--no-service"}, "not paired yet"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newMachine(t)
			c.setup(t, m)
			out, code := m.install(t, r.url, c.args...)
			if code == 0 || !strings.Contains(out, c.expect) {
				t.Fatalf("exit %d, want a refusal naming %q:\n%s", code, c.expect, out)
			}
			if got := m.entries(t); len(got) != 0 {
				t.Errorf("install.sh left %v", got)
			}
		})
	}
	if r.downloaded() != before {
		t.Error("a refused install downloaded the tarball")
	}
}

// tampered serves a release's tarball from another origin: a copy of install.sh is pointed there, with the sha256 the copy
// should expect.
func tampered(t *testing.T, r *release, tarball []byte, sha string) (script string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(tarball) }))
	t.Cleanup(srv.Close)
	text := string(mustRead(t, filepath.Join(r.dist, "download", "v"+r.version, "install.sh")))
	text = strings.Replace(text, "TARBALL_URL='"+r.url, "TARBALL_URL='"+srv.URL, 1)
	text = regexp.MustCompile(`(?m)^SHA256='[0-9a-f]+'`).ReplaceAllString(text, "SHA256='"+sha+"'")
	script = filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(script, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
	return script
}

func TestInstallRefusesATarballWhoseSha256IsNotTheBakedOne(t *testing.T) {
	r := buildRelease(t, pipelineVersion)
	good := mustRead(t, r.tgz)
	bad := append(append([]byte{}, good...), 0)
	m := newMachine(t)
	m.pair(t)
	out, code := m.installScript(t, tampered(t, r, bad, sha256Hex(good)), "--no-service")
	if code == 0 || !strings.Contains(out, "corrupt or was changed") || !strings.Contains(out, "Nothing was installed") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := m.entries(t); len(got) != 0 {
		t.Errorf("install.sh left %v", got)
	}
}

func TestInstallRefusesATarballWithoutTheEngineOfThisMachine(t *testing.T) {
	r := buildRelease(t, pipelineVersion)
	engine := hostEngine(t)
	other := "agentd-linux-riscv64"
	tgz := tarGz(t, map[string][]byte{other: []byte("x"), "launcher.sh": []byte("#!/bin/sh\n"), "package.json": []byte("{}")})
	m := newMachine(t)
	m.pair(t)
	out, code := m.installScript(t, tampered(t, r, tgz, sha256Hex(tgz)), "--no-service")
	if code == 0 || !strings.Contains(out, "has no "+engine) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := m.entries(t); len(got) != 0 {
		t.Errorf("install.sh left %v", got)
	}
}

// Pairing is a step of the install: a code the app does not take ends it with a message and a non-zero exit code.
func TestInstallEndsWhenPairingFails(t *testing.T) {
	r := buildRelease(t, pipelineVersion)
	hostEngine(t)
	m := newMachine(t)
	out, code := m.install(t, r.url, "--no-service", "--app", "http://127.0.0.1:1", "--code", "TESTCODE")
	if code == 0 || !strings.Contains(out, "pairing this machine with http://127.0.0.1:1") || !strings.Contains(out, "pairing failed") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, name := range m.entries(t) {
		if strings.HasPrefix(name, ".install.") {
			t.Errorf("install.sh left %s behind", name)
		}
	}
}

// A machine without Node.js gets a pinned one from nodejs.org, checked against its SHASUMS256.txt: here a local mirror
// serves a SHASUMS256.txt the archive does not match.
func TestInstallRefusesANodeArchiveThatDoesNotMatchItsSums(t *testing.T) {
	r := buildRelease(t, pipelineVersion)
	hostEngine(t)
	script := string(mustRead(t, filepath.Join(r.dist, "download", "v"+pipelineVersion, "install.sh")))
	version := regexp.MustCompile(`(?m)^NODE_VERSION='([^']+)'`).FindStringSubmatch(script)
	if version == nil {
		t.Fatal("install.sh has no NODE_VERSION")
	}
	os_ := runtime.GOOS
	arch := map[string]string{"arm64": "arm64", "amd64": "x64"}[runtime.GOARCH]
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "SHASUMS256.txt") {
			_, _ = w.Write([]byte(strings.Repeat("0", 64) + "  node-v" + version[1] + "-" + os_ + "-" + arch + ".tar.gz\n"))
			return
		}
		_, _ = w.Write([]byte("not the archive that was summed"))
	}))
	defer mirror.Close()

	m := newMachine(t)
	m.pair(t)
	m.path = narrowPath(t) // no node, no npm
	env := m.env()
	env = append(env, "AGENTD_NODE_DIST="+mirror.URL)
	out, code := run(t, env, "bash", "-c", `set -o pipefail; curl -fsSL "$0/latest/download/install.sh" | bash -s -- --no-service`, r.url)
	if code == 0 || !strings.Contains(out, "Node.js download is corrupt") || !strings.Contains(out, "Nothing was installed") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if got := m.entries(t); len(got) != 0 {
		t.Errorf("install.sh left %v", got)
	}
}

// narrowPath is a PATH of symlinks to what install.sh needs and nothing else: no node, no npm.
func narrowPath(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	for _, tool := range strings.Fields("bash sh curl tar gzip mktemp cut awk rm mv mkdir chmod cat env dirname sed grep head tail tr ps kill sleep hostname id ls shasum sha256sum perl ldd uname readlink cp date basename ln wc find") {
		if found, err := exec.LookPath(tool); err == nil {
			if err := os.Symlink(found, filepath.Join(bin, tool)); err != nil {
				t.Fatal(err)
			}
		}
	}
	return bin
}

// What the install tests leave out, because it needs what a test must not touch or reach:
//   - the service step (launchd or systemd --user, and the restart loop that runs a daemon): it starts a daemon of this
//     machine's user;
//   - a successful pairing and the daemon's hello to an app: an app server is needed, and daemon/cli and daemon/link
//     test them against one;
//   - the Node.js download from nodejs.org (about 50 MB): the network.
func TestInstallStepsThatNeedTheMachineOrTheNetworkAreSkipped(t *testing.T) {
	t.Skip("service install, a successful pairing against an app, and the real Node.js download are not run here")
}
