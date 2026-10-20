package pipeline

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/update"
)

const pipelineVersion = "0.0.0-pipeline"

func TestBuildPacksTheEngineOfEveryPlatform(t *testing.T) {
	r := buildRelease(t, pipelineVersion)
	entries := readTarball(t, r.tgz)

	var files []string
	for name, e := range entries {
		if !e.dir {
			files = append(files, name)
		}
	}
	slices.Sort(files)
	want := []string{
		"package/agentd-darwin-amd64", "package/agentd-darwin-arm64", "package/agentd-linux-amd64", "package/agentd-linux-arm64",
		"package/launcher.sh", "package/package.json", "package/tray/darwin-arm64/agentd-tray",
	}
	if !slices.Equal(files, want) {
		t.Fatalf("the tarball holds %v, want %v", files, want)
	}
	for _, name := range want {
		if name != "package/package.json" && entries[name].mode != 0o755 {
			t.Errorf("%s has mode %o, want 755", name, entries[name].mode)
		}
	}
	if !entries["package/tray"].dir {
		t.Error("package/tray is not a directory")
	}
	if want := mustRead(t, filepath.Join(rootDir, "daemon", "launcher.sh")); !bytes.Equal(entries["package/launcher.sh"].body, want) {
		t.Error("package/launcher.sh is not daemon/launcher.sh")
	}

	// package.json: what the updater checks is the version; there is no bin and no engines.
	var pkg map[string]any
	if err := json.Unmarshal(entries["package/package.json"].body, &pkg); err != nil {
		t.Fatal(err)
	}
	if len(pkg) != 3 || pkg["name"] != "agentd" || pkg["version"] != pipelineVersion || pkg["description"] == "" {
		t.Errorf("package.json is %v, want {name, version, description}", pkg)
	}
}

func TestBuildWritesALatestJSONTheBakedKeyVerifies(t *testing.T) {
	r := buildRelease(t, pipelineVersion)
	dir := filepath.Join(r.dist, "download", "v"+pipelineVersion)
	var raw map[string]string
	if err := json.Unmarshal(mustRead(t, filepath.Join(dir, "latest.json")), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 4 || raw["version"] != pipelineVersion || raw["url"] != "/download/v"+pipelineVersion+"/agentd-"+pipelineVersion+".tgz" {
		t.Fatalf("latest.json is %v, want {version, url, sha256, signature}", raw)
	}
	if want := sha256Hex(mustRead(t, r.tgz)); raw["sha256"] != want {
		t.Fatalf("latest.json carries sha256 %s, the tarball's is %s", raw["sha256"], want)
	}
	offer := r.offer(t)
	signed := update.Signed{Version: offer.Version, SHA256: offer.SHA256, Signature: offer.Signature}
	if problem := update.SignatureProblem(r.key, signed); problem != "" {
		t.Fatalf("the baked key refuses latest.json: %s", problem)
	}
	signed.SHA256 = strings.Repeat("0", 64)
	if update.SignatureProblem(r.key, signed) == "" {
		t.Fatal("the signature carried over to another sha256")
	}
	if got := string(mustRead(t, filepath.Join(r.dist, "latest"))); got != pipelineVersion+"\n" {
		t.Fatalf("<dist>/latest is %q", got)
	}
}

func TestBuildFillsInstallSh(t *testing.T) {
	r := buildRelease(t, pipelineVersion)
	text := string(mustRead(t, filepath.Join(r.dist, "download", "v"+pipelineVersion, "install.sh")))
	if strings.Contains(text, "@VERSION@") || strings.Contains(text, "@SHA256@") || strings.Contains(text, "@TARBALL_URL@") || strings.Contains(text, "@INSTALL_URL@") {
		t.Error("install.sh still holds a placeholder")
	}
	sha := r.offer(t).SHA256
	for _, line := range []string{
		"VERSION='" + pipelineVersion + "'",
		"SHA256='" + sha + "'",
		"TARBALL_URL='" + r.url + "/download/v" + pipelineVersion + "/agentd-" + pipelineVersion + ".tgz'",
		"curl -fsSL " + r.url + "/latest/download/install.sh | bash",
	} {
		if !strings.Contains(text, line) {
			t.Errorf("install.sh lacks %q", line)
		}
	}
	info, err := os.Stat(filepath.Join(r.dist, "download", "v"+pipelineVersion, "install.sh"))
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("install.sh is not mode 755: %v %v", info, err)
	}
}

// Every engine is a static binary of its platform that carries the version, the release key and the releases page.
func TestEveryEngineIsOfItsPlatformAndBakesTheRelease(t *testing.T) {
	r := buildRelease(t, pipelineVersion)
	entries := readTarball(t, r.tgz)
	for _, c := range []struct{ os, arch string }{{"darwin", "arm64"}, {"darwin", "amd64"}, {"linux", "arm64"}, {"linux", "amd64"}} {
		name := "agentd-" + c.os + "-" + c.arch
		body := entries["package/"+name].body
		switch c.os {
		case "darwin":
			f, err := macho.NewFile(bytes.NewReader(body))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if want := map[string]macho.Cpu{"arm64": macho.CpuArm64, "amd64": macho.CpuAmd64}[c.arch]; f.Cpu != want {
				t.Errorf("%s is for cpu %v", name, f.Cpu)
			}
		case "linux":
			f, err := elf.NewFile(bytes.NewReader(body))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if want := map[string]elf.Machine{"arm64": elf.EM_AARCH64, "amd64": elf.EM_X86_64}[c.arch]; f.Machine != want {
				t.Errorf("%s is for machine %v", name, f.Machine)
			}
		}
		for what, text := range map[string]string{"version": pipelineVersion, "release key": r.key, "releases page": r.url} {
			if !bytes.Contains(body, []byte(text)) {
				t.Errorf("%s does not carry the %s %q", name, what, text)
			}
		}
	}
}

func TestTheEngineOfThisMachineReportsTheVersion(t *testing.T) {
	r := buildRelease(t, pipelineVersion)
	engine := hostEngine(t)
	bin := filepath.Join(t.TempDir(), "agentd")
	if err := os.WriteFile(bin, readTarball(t, r.tgz)["package/"+engine].body, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, code := run(t, cleanEnv(), bin, "version"); code != 0 || strings.TrimSpace(out) != pipelineVersion {
		t.Fatalf("agentd version: exit %d, %q", code, out)
	}
}

// releaseBuild runs scripts/build-daemon.sh on top of a clean RELEASE=1 environment; every case here stops before it
// builds an engine.
func releaseBuild(t *testing.T, env ...string) (string, int) {
	t.Helper()
	needPipeline(t)
	return run(t, cleanEnv(append([]string{"RELEASE=1", "VERSION=1.0.0"}, env...)...),
		"bash", filepath.Join(rootDir, "scripts", "build-daemon.sh"), t.TempDir())
}

func TestAReleaseBuildNeedsAVersionSomeoneChose(t *testing.T) {
	for _, version := range []string{"", "1.2", "1.0.0-rc1", "0.0.0-dev"} {
		out, code := releaseBuild(t, "VERSION="+version)
		if code != 1 || !strings.Contains(out, "needs VERSION=x.y.z") {
			t.Errorf("VERSION=%q: exit %d, %s", version, code, out)
		}
	}
}

func TestABuildRefusesAVersionTheDaemonOrTheServerWouldNotAccept(t *testing.T) {
	for _, version := range []string{"1.0_0", "../1", "current", "-1"} {
		out, code := releaseBuild(t, "VERSION="+version)
		if code != 1 || !strings.Contains(out, "is not a plain version") {
			t.Errorf("VERSION=%q: exit %d, %s", version, code, out)
		}
	}
}

func TestAReleaseBuildNeedsTheReleasePrivateKey(t *testing.T) {
	if out, code := releaseBuild(t); code != 1 || !strings.Contains(out, "needs DAEMON_SIGNING_KEY") {
		t.Errorf("exit %d, %s", code, out)
	}
	if out, code := releaseBuild(t, "DAEMON_SIGNING_KEY="+filepath.Join(t.TempDir(), "no-such-key")); code != 1 || !strings.Contains(out, "is not a readable file") {
		t.Errorf("a missing key file: exit %d, %s", code, out)
	}
}

func TestAReleaseBuildRefusesAKeyThatIsNotDaemonReleasePub(t *testing.T) {
	key, _ := signingKey(t)
	if out, code := releaseBuild(t, "DAEMON_SIGNING_KEY="+key); code != 1 || !strings.Contains(out, "is not daemon/release.pub") {
		t.Errorf("exit %d, %s", code, out)
	}
}

// A development build bakes in the address of the local stand-in as its releases page. Signed with the release key, it
// would be a newer version every installed daemon takes and then never updates from again.
func TestABuildThatIsNotAReleaseRefusesTheReleaseKey(t *testing.T) {
	needPipeline(t)
	key, _ := signingKey(t)
	pub, code := run(t, cleanEnv(), signer.bin, "public", key)
	if code != 0 {
		t.Fatalf("sign-daemon public exited %d: %s", code, pub)
	}
	releasePub := filepath.Join(t.TempDir(), "release.pub")
	if err := os.WriteFile(releasePub, []byte(strings.TrimSpace(pub)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, code := run(t, cleanEnv("VERSION=9.9.9", "DAEMON_SIGNING_KEY="+key, "RELEASE_PUB_FILE="+releasePub),
		"bash", filepath.Join(rootDir, "scripts", "build-daemon.sh"), t.TempDir())
	if code != 1 || !strings.Contains(out, "signs only RELEASE=1 builds") {
		t.Errorf("exit %d, %s", code, out)
	}
}

// RELEASE_PUB_FILE is the tests' way to name a key a development build must refuse; in a release it would be a way to
// name the wrong key as the right one, so a release reads daemon/release.pub only.
func TestAReleaseBuildIgnoresReleasePubFileFromTheEnvironment(t *testing.T) {
	needPipeline(t)
	key, _ := signingKey(t)
	pub, code := run(t, cleanEnv(), signer.bin, "public", key)
	if code != 0 {
		t.Fatalf("sign-daemon public exited %d: %s", code, pub)
	}
	stray := filepath.Join(t.TempDir(), "release.pub")
	if err := os.WriteFile(stray, []byte(strings.TrimSpace(pub)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, code := releaseBuild(t, "DAEMON_SIGNING_KEY="+key, "RELEASE_PUB_FILE="+stray); code != 1 || !strings.Contains(out, "is not daemon/release.pub") {
		t.Errorf("exit %d, %s", code, out)
	}
}

func TestABuildWithAReleaseURLThatIsNotAReleasesPageStops(t *testing.T) {
	needPipeline(t)
	key, _ := signingKey(t)
	for _, url := range []string{"http://host/a/../b", "ftp://host", "http://host/?q=1", "http://host/a b"} {
		out, code := run(t, cleanEnv("VERSION=1.0.0", "DAEMON_SIGNING_KEY="+key, "RELEASE_URL="+url),
			"bash", filepath.Join(rootDir, "scripts", "build-daemon.sh"), t.TempDir())
		if code != 1 || !strings.Contains(out, "is not a releases page") {
			t.Errorf("RELEASE_URL=%q: exit %d, %s", url, code, out)
		}
	}
}

func TestATrayDirThatIsNotADirectoryStopsTheBuild(t *testing.T) {
	key, _ := signingKey(t)
	out, code := run(t, cleanEnv("VERSION=1.0.0", "DAEMON_SIGNING_KEY="+key, "TRAY_DIR="+filepath.Join(t.TempDir(), "no-such-trays")),
		"bash", filepath.Join(rootDir, "scripts", "build-daemon.sh"), t.TempDir())
	if code != 1 || !strings.Contains(out, "is not a directory") {
		t.Fatalf("exit %d, %s", code, out)
	}
}

// scripts/build-tray.sh builds one program per platform, named for Go's os-arch, with .exe on Windows.
func TestBuildTrayMakesOneProgramPerPlatform(t *testing.T) {
	needPipeline(t)
	out := t.TempDir()
	if o, code := run(t, cleanEnv("TRAY_TARGETS=linux/amd64 windows/amd64"), "bash", filepath.Join(rootDir, "scripts", "build-tray.sh"), out); code != 0 {
		t.Fatalf("build-tray.sh exited %d: %s", code, o)
	}
	info, err := os.Stat(filepath.Join(out, "linux-amd64", "agentd-tray"))
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("linux-amd64/agentd-tray: %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(out, "windows-amd64", "agentd-tray.exe")); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(filepath.Join(out, "darwin-arm64")); err == nil {
		t.Error("a platform outside TRAY_TARGETS was built")
	}
}
