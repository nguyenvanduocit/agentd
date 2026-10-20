package pipeline

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/update"
)

// The release key is a set of two: daemon/release.pub, and daemon/release-backup.pub when the file exists. Every engine
// of a release bakes the set in, and a release signed by either key is one that every daemon carrying the set takes.
// These tests run the real build-daemon.sh in a copy of the repository whose two key files are throwaway keys, so the
// release private key is not needed and the script has no way to name other keys than the repository's.

// pipelineKey is a throwaway signing key: its file, its public half as daemon/release.pub holds it, and as the
// daemons bake it in.
type pipelineKey struct{ file, pem, b64 string }

func runOK(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, code := run(t, cleanEnv(), name, args...)
	if code != 0 {
		t.Fatalf("%s %v exited %d: %s", name, args, code, out)
	}
	return out
}

func newPipelineKey(t *testing.T) pipelineKey {
	t.Helper()
	signingKey(t) // builds the signer
	file := filepath.Join(t.TempDir(), "key")
	runOK(t, signer.bin, "keygen", file)
	return pipelineKey{
		file: file,
		pem:  runOK(t, signer.bin, "public", file),
		b64:  strings.TrimSpace(runOK(t, signer.bin, "public-b64", file)),
	}
}

// sign is the signature a release of `version` and tarball `sha` gets from the key.
func (k pipelineKey) sign(t *testing.T, version, sha string) string {
	t.Helper()
	return strings.TrimSpace(runOK(t, signer.bin, "sign", k.file, version, sha))
}

// keySetRoot is a repository for build-daemon.sh to run in: its daemon/release.pub holds `primary` and its
// daemon/release-backup.pub holds `backup` (no such file when backup is ""); primary "" leaves release.pub out.
// Everything else is a link to the real repository, so the build compiles the real sources.
func keySetRoot(t *testing.T, primary, backup string) string {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"cmd", "contract", "internal", "sdk", "go.mod", "go.sum"} {
		if err := os.Symlink(filepath.Join(rootDir, name), filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	daemon := filepath.Join(root, "daemon")
	if err := os.Mkdir(daemon, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(rootDir, "daemon"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "release") && strings.HasSuffix(e.Name(), ".pub") {
			continue
		}
		if err := os.Symlink(filepath.Join(rootDir, "daemon", e.Name()), filepath.Join(daemon, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	for file, content := range map[string]string{"release.pub": primary, "release-backup.pub": backup} {
		if content == "" {
			continue
		}
		if err := os.WriteFile(filepath.Join(daemon, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	scripts := filepath.Join(root, "scripts")
	if err := os.Mkdir(scripts, 0o755); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(rootDir, "scripts", "build-daemon.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scripts, "build-daemon.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// build runs the copy's build-daemon.sh with `env` on top of a clean environment, into a directory of its own.
func buildIn(t *testing.T, root, out string, env ...string) (string, int) {
	t.Helper()
	return run(t, cleanEnv(append([]string{"VERSION=1.0.0", "TRAY_DIR=" + trayDir(t)}, env...)...),
		"bash", filepath.Join(root, "scripts", "build-daemon.sh"), out)
}

func TestAReleaseBuildBakesTheKeySetAndEitherKeyMayHaveSignedIt(t *testing.T) {
	needPipeline(t)
	primary, backup, stranger := newPipelineKey(t), newPipelineKey(t), newPipelineKey(t)
	root := keySetRoot(t, primary.pem, backup.pem)
	out := t.TempDir()

	// The primary key is lost: the release is signed with the backup key.
	if o, code := buildIn(t, root, out, "RELEASE=1", "DAEMON_SIGNING_KEY="+backup.file); code != 0 {
		t.Fatalf("a release signed by the backup key: exit %d\n%s", code, o)
	}
	dir := filepath.Join(out, "download", "v1.0.0")
	set := primary.b64 + "," + backup.b64
	for name, entry := range readTarball(t, filepath.Join(dir, "agentd-1.0.0.tgz")) {
		if strings.HasPrefix(name, "package/agentd-") && !entry.dir && !bytes.Contains(entry.body, []byte(set)) {
			t.Errorf("%s does not carry the key set %q", name, set)
		}
	}

	text, err := os.ReadFile(filepath.Join(dir, "latest.json"))
	if err != nil {
		t.Fatal(err)
	}
	offer, ok := update.ParseLatest(string(text))
	if !ok {
		t.Fatalf("latest.json is not an offer: %s", text)
	}
	signed := update.Signed{Version: offer.Version, SHA256: offer.SHA256, Signature: offer.Signature}
	if why := update.SignatureProblem(set, signed); why != "" {
		t.Errorf("a daemon with the baked set refuses the release the backup key signed: %s", why)
	}
	if why := update.SignatureProblem(backup.b64, signed); why != "" {
		t.Errorf("the backup key alone does not verify its own release: %s", why)
	}
	if why := update.SignatureProblem(primary.b64, signed); !strings.Contains(why, "does not match") {
		t.Errorf("the primary key verified a release the backup key signed: %q", why)
	}

	// The same baked set, the other key, a stranger, and a tarball that is not the one signed.
	if why := update.SignatureProblem(set, update.Signed{Version: offer.Version, SHA256: offer.SHA256, Signature: primary.sign(t, offer.Version, offer.SHA256)}); why != "" {
		t.Errorf("a daemon with the baked set refuses a release the primary key signed: %s", why)
	}
	if why := update.SignatureProblem(set, update.Signed{Version: offer.Version, SHA256: offer.SHA256, Signature: stranger.sign(t, offer.Version, offer.SHA256)}); !strings.Contains(why, "does not match") {
		t.Errorf("a daemon with the baked set took a release neither key signed: %q", why)
	}
	other := signed
	other.SHA256 = strings.Repeat("ab", 32)
	if why := update.SignatureProblem(set, other); !strings.Contains(why, "does not match") {
		t.Errorf("the backup key's signature carried over to another tarball: %q", why)
	}
}

func TestAReleaseBuildWithoutABackupKeyBakesTheReleaseKeyAlone(t *testing.T) {
	needPipeline(t)
	primary := newPipelineKey(t)
	root := keySetRoot(t, primary.pem, "")
	out := t.TempDir()
	if o, code := buildIn(t, root, out, "RELEASE=1", "DAEMON_SIGNING_KEY="+primary.file); code != 0 {
		t.Fatalf("exit %d\n%s", code, o)
	}
	for name, entry := range readTarball(t, filepath.Join(out, "download", "v1.0.0", "agentd-1.0.0.tgz")) {
		if !strings.HasPrefix(name, "package/agentd-") || entry.dir {
			continue
		}
		if !bytes.Contains(entry.body, []byte(primary.b64)) || bytes.Contains(entry.body, []byte(primary.b64+",")) {
			t.Errorf("%s does not carry the release key alone", name)
		}
	}
}

func TestAReleaseBuildRefusesAnUnusableBackupKeyOrASigningKeyOutsideTheSet(t *testing.T) {
	needPipeline(t)
	primary, backup, stranger := newPipelineKey(t), newPipelineKey(t), newPipelineKey(t)
	for _, c := range []struct {
		name            string
		primary, backup string
		signer          pipelineKey
		want            string
	}{
		{"a backup file that is not a key", primary.pem, "not a key\n", primary, "release-backup.pub is not an Ed25519 public key"},
		{"a backup file that is the primary key", primary.pem, primary.pem, primary, "release-backup.pub is the same key as daemon/release.pub"},
		{"a signing key that is in neither file", primary.pem, backup.pem, stranger, "is not daemon/release.pub"},
		{"no daemon/release.pub", "", backup.pem, backup, "is missing"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, code := buildIn(t, keySetRoot(t, c.primary, c.backup), t.TempDir(), "RELEASE=1", "DAEMON_SIGNING_KEY="+c.signer.file)
			if code != 1 || !strings.Contains(out, c.want) {
				t.Errorf("exit %d, want 1 and %q:\n%s", code, c.want, out)
			}
		})
	}
}

func TestABuildThatIsNotAReleaseRefusesBothKeysOfTheSet(t *testing.T) {
	needPipeline(t)
	primary, backup := newPipelineKey(t), newPipelineKey(t)
	for name, key := range map[string]pipelineKey{"the primary key": primary, "the backup key": backup} {
		root := keySetRoot(t, primary.pem, backup.pem)
		out, code := buildIn(t, root, t.TempDir(), "DAEMON_SIGNING_KEY="+key.file)
		if code != 1 || !strings.Contains(out, "signs only RELEASE=1 builds") {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
}
