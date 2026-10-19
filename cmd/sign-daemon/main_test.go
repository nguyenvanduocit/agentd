package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nguyenvanduocit/agentd/daemon/update"
)

const (
	testVersion = "2.0.0"
	// The sha256 of "abc": the testdata signature covers it.
	testSHA = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
)

// The files of testdata were made by the TypeScript sign-daemon (scripts/sign-daemon.ts, now removed) from key.pem: this
// program must print the same bytes, and an Ed25519 signature is deterministic, so it is a byte comparison.
func golden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func do(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

func newKeyFile(t *testing.T) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "key")
	if _, stderr, code := do(t, "keygen", file); code != 0 {
		t.Fatalf("keygen: %d %s", code, stderr)
	}
	return file
}

func TestCommandsPrintTheBytesOfTheTypeScriptTool(t *testing.T) {
	key := filepath.Join("testdata", "key.pem")
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"public", []string{"public", key}, golden(t, "public.pem")},
		{"public-b64 has no newline", []string{"public-b64", key}, golden(t, "public.b64")},
		{"sign", []string{"sign", key, testVersion, testSHA}, golden(t, "sign-2.0.0.txt")},
	} {
		t.Run(c.name, func(t *testing.T) {
			stdout, stderr, code := do(t, c.args...)
			if code != 0 || stdout != c.want {
				t.Fatalf("exit %d, stdout %q, stderr %q; want %q", code, stdout, stderr, c.want)
			}
		})
	}
}

func TestTheReleaseKeyFileHasTheFormatPublicPrints(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "daemon", "release.pub"))
	if err != nil {
		t.Fatal(err)
	}
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 {
		t.Fatalf("daemon/release.pub is not one PUBLIC KEY block")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pub, ok := parsed.(ed25519.PublicKey)
	if !ok {
		t.Fatalf("daemon/release.pub holds %T", parsed)
	}
	// Re-encoding the key must give the file's bytes back: `public` prints this encoding.
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})); got != string(data) {
		t.Fatalf("re-encoded %q, file %q", got, data)
	}
}

// The release private key is git-ignored (.release-signing/): where it is present, its public half is the committed one.
func TestTheReleasePrivateKeyMatchesReleasePub(t *testing.T) {
	file := filepath.Join("..", "..", ".release-signing", "daemon-signing.key")
	if _, err := os.Stat(file); err != nil {
		t.Skip("no .release-signing/daemon-signing.key on this machine")
	}
	stdout, stderr, code := do(t, "public", file)
	if code != 0 {
		t.Fatal(stderr)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "daemon", "release.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if stdout != string(want) {
		t.Fatalf("public key of the release private key %q, daemon/release.pub %q", stdout, want)
	}
}

func TestSignatureVerifiesWithTheDaemonsCheck(t *testing.T) {
	key := newKeyFile(t)
	b64, _, _ := do(t, "public-b64", key)
	signature, _, code := do(t, "sign", key, testVersion, testSHA)
	if code != 0 {
		t.Fatal("sign failed")
	}
	signed := update.Signed{Version: testVersion, SHA256: testSHA, Signature: strings.TrimSuffix(signature, "\n")}
	if problem := update.SignatureProblem(b64, signed); problem != "" {
		t.Fatalf("the daemon refuses the signature: %s", problem)
	}
	signed.SHA256 = strings.Repeat("cd", 32)
	if update.SignatureProblem(b64, signed) == "" {
		t.Fatal("a signature carried over to another tarball")
	}
	if !strings.HasSuffix(signature, "\n") || len(signature) != 89 {
		t.Fatalf("signature output %q is not 88 characters and a newline", signature)
	}
}

func TestKeygenMakesAPrivateKeyOnceWithMode0600(t *testing.T) {
	file := newKeyFile(t)
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", info.Mode().Perm())
	}
	first, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(first), "-----BEGIN PRIVATE KEY-----\n") {
		t.Fatalf("not a PKCS8 PEM: %q", first)
	}
	if _, stderr, code := do(t, "keygen", file); code != 0 {
		t.Fatal(stderr)
	}
	if second, _ := os.ReadFile(file); !bytes.Equal(first, second) {
		t.Fatal("a second keygen replaced the key")
	}
	entries, _ := os.ReadDir(filepath.Dir(file))
	if len(entries) != 1 {
		t.Fatalf("keygen left %d files in the directory, want only the key", len(entries))
	}
}

func TestKeygenRacingBuildsEndWithOneKey(t *testing.T) {
	file := filepath.Join(t.TempDir(), "key")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := keygen(file); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, stderr, code := do(t, "public", file); code != 0 {
		t.Fatalf("the key is not whole: %s", stderr)
	}
	if entries, _ := os.ReadDir(filepath.Dir(file)); len(entries) != 1 {
		t.Fatalf("%d files left, want only the key", len(entries))
	}
}

func TestAKeyThatIsNotEd25519SignsNothing(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(rsaKey)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "rsa.pem")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"sign", file, testVersion, testSHA}, {"public", file}, {"public-b64", file}} {
		stdout, stderr, code := do(t, args...)
		if code != 1 || stdout != "" || !strings.Contains(stderr, "not Ed25519") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

func TestWrongUsageExitsTwo(t *testing.T) {
	for _, args := range [][]string{nil, {"keygen"}, {"sign", "k", "v"}, {"public", "k", "extra"}, {"nope", "k"}} {
		_, stderr, code := do(t, args...)
		if code != 2 || !strings.HasPrefix(stderr, "usage: sign-daemon") {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
}

func TestAMissingKeyFileFails(t *testing.T) {
	if _, stderr, code := do(t, "public", filepath.Join(t.TempDir(), "none")); code != 1 || stderr == "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
}

func TestPublicB64IsTheDERThePEMHolds(t *testing.T) {
	b64 := strings.TrimSpace(golden(t, "public.b64"))
	block, _ := pem.Decode([]byte(golden(t, "public.pem")))
	if base64.StdEncoding.EncodeToString(block.Bytes) != b64 {
		t.Fatal("public.b64 is not the DER of public.pem")
	}
}
