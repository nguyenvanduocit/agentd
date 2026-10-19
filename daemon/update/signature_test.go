package update

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"strings"
	"testing"
)

const sigSHA = "abababababababababababababababababababababababababababababababab"

type keyPair struct {
	priv ed25519.PrivateKey
	key  string // the public key as the binary bakes it in: base64 of the SPKI DER
}

func newKeyPair(t testing.TB) keyPair {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return keyPair{priv: priv, key: base64.StdEncoding.EncodeToString(der)}
}

// sign is what scripts/sign-daemon.ts does: the base64 Ed25519 signature of ReleaseMessage.
func (k keyPair) sign(version, sha256 string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(k.priv, ReleaseMessage(version, sha256)))
}

func (k keyPair) signed(version, sha256 string) Signed {
	return Signed{Version: version, SHA256: sha256, Signature: k.sign(version, sha256)}
}

func TestSignatureProblem(t *testing.T) {
	release := newKeyPair(t)
	t.Run("the release key's signature of the version and sha256 verifies", func(t *testing.T) {
		if got := SignatureProblem(release.key, release.signed("2.0.0", sigSHA)); got != "" {
			t.Error(got)
		}
	})
	t.Run("a signature does not carry over to another sha256 (another tarball)", func(t *testing.T) {
		s := release.signed("2.0.0", sigSHA)
		s.SHA256 = strings.Repeat("cd", 32)
		if got := SignatureProblem(release.key, s); !strings.Contains(got, "does not match") {
			t.Error(got)
		}
	})
	t.Run("a signature does not carry over to another version", func(t *testing.T) {
		s := release.signed("2.0.0", sigSHA)
		s.Version = "2.0.1"
		if got := SignatureProblem(release.key, s); !strings.Contains(got, "does not match") {
			t.Error(got)
		}
	})
	t.Run("a missing or malformed signature is refused", func(t *testing.T) {
		good := release.sign("2.0.0", sigSHA)
		for _, sig := range []string{"", "AAAA", strings.Repeat("A", 88), good[:86] + "="} {
			if got := SignatureProblem(release.key, Signed{Version: "2.0.0", SHA256: sigSHA, Signature: sig}); got == "" {
				t.Errorf("%q verified", sig)
			}
		}
	})
	t.Run("a signature by another key is refused", func(t *testing.T) {
		if got := SignatureProblem(release.key, newKeyPair(t).signed("2.0.0", sigSHA)); !strings.Contains(got, "does not match") {
			t.Error(got)
		}
	})
	t.Run("a daemon built without a release key refuses every offer", func(t *testing.T) {
		if got := SignatureProblem("", release.signed("2.0.0", sigSHA)); !strings.Contains(got, "without a release key") {
			t.Error(got)
		}
	})
	t.Run("a baked key that is not an Ed25519 public key refuses every offer", func(t *testing.T) {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{base64.StdEncoding.EncodeToString(der), "bm90IGEga2V5"} {
			if got := SignatureProblem(key, release.signed("2.0.0", sigSHA)); !strings.Contains(got, "not an Ed25519 public key") {
				t.Errorf("%q: %s", key, got)
			}
		}
	})
}

// A signature made by scripts/sign-daemon.ts (`sign <key> 2.0.0 <sha256>`) verifies here: the two sides agree on
// the bytes. Ed25519 is deterministic, so the vector never changes.
func TestSignatureFromSignDaemonScript(t *testing.T) {
	const (
		key = "MCowBQYDK2VwAyEAYCTlYfCAVBX0YYKuaT7w/BrX+1PVg75ppSVQ3McmdeA="
		sig = "ViNQmmEsf0FVd/+Y/TPk0kdlV1zKksP5pOnQsL0QmyCuWb9s7zaQHZbEt81mfPnxDCkl5xp3l1MDhmC850arAQ=="
	)
	if got := SignatureProblem(key, Signed{Version: "2.0.0", SHA256: sigSHA, Signature: sig}); got != "" {
		t.Error(got)
	}
	if got := SignatureProblem(key, Signed{Version: "2.0.1", SHA256: sigSHA, Signature: sig}); got == "" {
		t.Error("the signature of 2.0.0 verified 2.0.1")
	}
}

func TestReleaseMessage(t *testing.T) {
	if got := string(ReleaseMessage("2.0.0", sigSHA)); got != "agentd\n2.0.0\n"+sigSHA+"\n" {
		t.Errorf("%q", got)
	}
}

func TestDecodeSignature(t *testing.T) {
	good := newKeyPair(t).sign("2.0.0", sigSHA)
	if sig, ok := DecodeSignature(good); !ok || len(sig) != 64 {
		t.Error("a good signature was refused")
	}
	for name, bad := range map[string]string{
		"unpadded":    good[:len(good)-2],
		"a space":     " " + good,
		"a newline":   good + "\n",
		"63 bytes":    base64.StdEncoding.EncodeToString(make([]byte, 63)),
		"65 bytes":    base64.StdEncoding.EncodeToString(make([]byte, 65)),
		"empty":       "",
		"not base 64": "!!!!",
	} {
		if _, ok := DecodeSignature(bad); ok {
			t.Errorf("%s: accepted", name)
		}
	}
}
