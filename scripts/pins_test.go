package scripts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/check-release.sh and scripts/pins.sh make the SDK's promise ("a published file never changes") a check. They
// run here in a repository of their own, with a tag and a few files, so no release is built.

// rootDir is the repository.
var rootDir = func() string {
	dir, err := filepath.Abs("..")
	if err != nil {
		panic(err)
	}
	return dir
}()

func needReleaseTools(t *testing.T) {
	t.Helper()
	for _, tool := range []string{"git", "node", "perl", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
}

// in runs a command in dir and returns its combined output and exit code.
func in(t *testing.T, dir, name string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exit):
		return string(out), exit.ExitCode()
	}
	t.Fatalf("%s %v: %v", name, args, err)
	return "", -1
}

func write(t *testing.T, dir, rel, text string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func packageJSON(version string) string {
	return `{"name":"@agentd/client","version":"` + version + `"}` + "\n"
}

// releaseRepo is a git repository with the two scripts of the real one in scripts/.
func releaseRepo(t *testing.T) string {
	t.Helper()
	needReleaseTools(t)
	dir := t.TempDir()
	for _, name := range []string{"check-release.sh", "pins.sh"} {
		body, err := os.ReadFile(filepath.Join(rootDir, "scripts", name))
		if err != nil {
			t.Fatal(err)
		}
		write(t, dir, filepath.Join("scripts", name), string(body))
		if err := os.Chmod(filepath.Join(dir, "scripts", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if out, code := in(t, dir, "git", "init", "-q", "-b", "main"); code != 0 {
		t.Fatalf("git init: %s", out)
	}
	return dir
}

func commitAll(t *testing.T, dir, message string) {
	t.Helper()
	if out, code := in(t, dir, "git", "add", "-A"); code != 0 {
		t.Fatalf("git add: %s", out)
	}
	if out, code := in(t, dir, "git", "commit", "-q", "-m", message); code != 0 {
		t.Fatalf("git commit: %s", out)
	}
}

func checkSDK(t *testing.T, dir string) (string, int) {
	t.Helper()
	return in(t, dir, "bash", "scripts/check-release.sh", "sdk")
}

func TestTheSDKRuleHasNothingToCompareWithBeforeTheFirstTag(t *testing.T) {
	dir := releaseRepo(t)
	write(t, dir, "sdk/web/package.json", packageJSON("0.1.0"))
	write(t, dir, "sdk/web/src/a.ts", "export const a = 1\n")
	commitAll(t, dir, "first")
	if out, code := checkSDK(t, dir); code != 0 || !strings.Contains(out, "no release tag") {
		t.Errorf("exit %d: %s", code, out)
	}
}

func TestTheSDKRuleRefusesChangedSourcesUnderTheVersionOfTheLastRelease(t *testing.T) {
	dir := releaseRepo(t)
	// The last release carries the SDK under its old name, sdk/client.
	write(t, dir, "sdk/client/package.json", packageJSON("0.5.0"))
	write(t, dir, "sdk/client/src/a.ts", "export const a = 1\n")
	write(t, dir, "sdk/client/src/a.test.ts", "// a test\n")
	commitAll(t, dir, "release")
	if out, code := in(t, dir, "git", "tag", "v1.0.0"); code != 0 {
		t.Fatalf("git tag: %s", out)
	}

	// Renamed to sdk/web, nothing else changed: the compiled SDK is the same, so the version may stay.
	if out, code := in(t, dir, "git", "mv", "sdk/client", "sdk/web"); code != 0 {
		t.Fatalf("git mv: %s", out)
	}
	commitAll(t, dir, "rename")
	if out, code := checkSDK(t, dir); code != 0 || !strings.Contains(out, "version 0.5.0 is fine") {
		t.Errorf("a rename alone: exit %d: %s", code, out)
	}

	// Only a test changed: the compiled SDK is the same.
	write(t, dir, "sdk/web/src/a.test.ts", "// another test\n")
	commitAll(t, dir, "test")
	if out, code := checkSDK(t, dir); code != 0 {
		t.Errorf("a test alone: exit %d: %s", code, out)
	}

	// A source changed and the version did not: the file agentd-client-0.5.0.tgz of the release would no longer be this SDK.
	write(t, dir, "sdk/web/src/a.ts", "export const a = 2\n")
	commitAll(t, dir, "change")
	out, code := checkSDK(t, dir)
	if code != 1 || !strings.Contains(out, "still version 0.5.0: bump it") || !strings.Contains(out, "v1.0.0") {
		t.Errorf("a source changed under the old version: exit %d: %s", code, out)
	}

	// An uncommitted change counts too, and so does a new file.
	write(t, dir, "sdk/web/package.json", packageJSON("0.6.0"))
	if out, code := checkSDK(t, dir); code != 0 || !strings.Contains(out, "0.5.0 -> 0.6.0") {
		t.Errorf("a bumped version: exit %d: %s", code, out)
	}
	write(t, dir, "sdk/web/package.json", packageJSON("0.5.0"))
	write(t, dir, "sdk/web/src/b.ts", "export const b = 1\n")
	if _, code := checkSDK(t, dir); code != 1 {
		t.Errorf("a new source under the old version: exit %d, want 1", code)
	}
}

// The sources of several files are compared by content, whatever order their hashes and their names fall in: an SDK
// that did not change since the last release is no change, which a single source file cannot show.
func TestTheSDKRuleSeesUnchangedSourcesOfSeveralFilesAsUnchanged(t *testing.T) {
	dir := releaseRepo(t)
	write(t, dir, "sdk/web/package.json", packageJSON("0.5.0"))
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "sub/h"} {
		write(t, dir, "sdk/web/src/"+name+".ts", "export const "+filepath.Base(name)+" = 1\n")
	}
	commitAll(t, dir, "release")
	if out, code := in(t, dir, "git", "tag", "v1.0.0"); code != 0 {
		t.Fatalf("git tag: %s", out)
	}
	write(t, dir, "docs.md", "a doc\n")
	commitAll(t, dir, "doc")
	if out, code := checkSDK(t, dir); code != 0 || !strings.Contains(out, "version 0.5.0 is fine") {
		t.Errorf("sources unchanged since the tag: exit %d: %s", code, out)
	}
}

const pinnedReadme = "install: bun add https://github.com/nguyenvanduocit/agentd/releases/download/v0.6.0/agentd-client-0.5.0.tgz\n" +
	"template: bun add https://github.com/nguyenvanduocit/agentd/releases/download/v<version>/agentd-client-<sdk version>.tgz\n" +
	"go: go get github.com/nguyenvanduocit/agentd@v0.6.0 and go get github.com/nguyenvanduocit/agentd/sdk/goclient@v0.6.0\n"

func TestThePinsOfADocAndAnExampleNameTheReleaseOrTheCheckSaysWhichDoNot(t *testing.T) {
	dir := releaseRepo(t)
	write(t, dir, "sdk/web/package.json", packageJSON("0.6.0"))
	write(t, dir, "README.md", pinnedReadme)
	write(t, dir, "sdk/web/README.md", "no pin here\n")
	write(t, dir, "examples/minimal-app/page/package.json",
		`{"dependencies":{"@agentd/client":"https://github.com/nguyenvanduocit/agentd/releases/download/v0.6.0/agentd-client-0.5.0.tgz"}}`+"\n")
	commitAll(t, dir, "pins")

	out, code := in(t, dir, "bash", "scripts/check-release.sh", "pins", "0.7.0")
	if code != 1 {
		t.Fatalf("stale pins: exit %d: %s", code, out)
	}
	for _, want := range []string{
		"README.md:1 pins releases/download/v0.6.0/agentd-client-0.5.0.tgz, the release being cut is v0.7.0 with SDK 0.6.0",
		"README.md:3 pins github.com/nguyenvanduocit/agentd@v0.6.0",
		"README.md:3 pins github.com/nguyenvanduocit/agentd/sdk/goclient@v0.6.0",
		"examples/minimal-app/page/package.json:1 pins",
		"run: scripts/pins.sh 0.7.0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the check does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "README.md:2") {
		t.Errorf("a placeholder was taken for a pin:\n%s", out)
	}

	// The script that moves them leaves a repository the check accepts, and a placeholder as it was.
	if out, code := in(t, dir, "bash", "scripts/pins.sh", "0.7.0"); code != 0 {
		t.Fatalf("pins.sh: exit %d: %s", code, out)
	}
	readme, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	wantReadme := "install: bun add https://github.com/nguyenvanduocit/agentd/releases/download/v0.7.0/agentd-client-0.6.0.tgz\n" +
		"template: bun add https://github.com/nguyenvanduocit/agentd/releases/download/v<version>/agentd-client-<sdk version>.tgz\n" +
		"go: go get github.com/nguyenvanduocit/agentd@v0.7.0 and go get github.com/nguyenvanduocit/agentd/sdk/goclient@v0.7.0\n"
	if string(readme) != wantReadme {
		t.Errorf("README.md after pins.sh:\n%s\nwant:\n%s", readme, wantReadme)
	}
	if out, code := in(t, dir, "bash", "scripts/check-release.sh", "pins", "0.7.0"); code != 0 {
		t.Errorf("after pins.sh the check still fails: exit %d: %s", code, out)
	}
	// Idempotent, and the SDK version can be named.
	if out, code := in(t, dir, "bash", "scripts/pins.sh", "0.7.0", "0.6.0"); code != 0 {
		t.Errorf("pins.sh a second time: exit %d: %s", code, out)
	}
	if out, code := in(t, dir, "bash", "scripts/pins.sh", "0.8.0", "0.7.0"); code != 1 {
		t.Errorf("pins.sh for SDK 0.7.0 while sdk/web says 0.6.0: exit %d: %s", code, out)
	}
}

func TestPinsShRefusesAVersionThatIsNotPlain(t *testing.T) {
	dir := releaseRepo(t)
	write(t, dir, "sdk/web/package.json", packageJSON("0.6.0"))
	for _, bad := range []string{"0.7", "v0.7.0", "0.7.0-rc1", "0.7.0; rm -rf ~"} {
		if out, code := in(t, dir, "bash", "scripts/pins.sh", bad); code != 1 || !strings.Contains(out, "is not x.y.z") {
			t.Errorf("pins.sh %q: exit %d: %s", bad, code, out)
		}
		if out, code := in(t, dir, "bash", "scripts/check-release.sh", "pins", bad); code != 1 || !strings.Contains(out, "is not x.y.z") {
			t.Errorf("check-release.sh pins %q: exit %d: %s", bad, code, out)
		}
	}
}
