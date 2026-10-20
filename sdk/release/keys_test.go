package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The keys daemons check updates against are daemon/release.pub and, once the owner makes one, daemon/release-backup.pub.
// Each has a copy in keys/ (the files this package embeds), and the two directories must hold the same files.
func TestTheEmbeddedKeysAreTheOnesDaemonsCheck(t *testing.T) {
	daemon, err := filepath.Glob("../../daemon/release*.pub")
	if err != nil || len(daemon) == 0 {
		t.Fatalf("no daemon/release*.pub: %v", err)
	}
	for _, path := range daemon {
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join("keys", filepath.Base(path)))
		if err != nil {
			t.Fatalf("daemons trust %s but sdk/release/keys has no copy: %v (an offer signed by it would be refused here, and the update-pending hint would vanish)", path, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("sdk/release/keys/%s differs from %s: they must be the same key", filepath.Base(path), path)
		}
	}
	embedded, err := filepath.Glob("keys/*.pub")
	if err != nil {
		t.Fatal(err)
	}
	if len(embedded) != len(daemon) {
		t.Errorf("sdk/release/keys has %v and daemon/ has %v: the same files in both", embedded, daemon)
	}
	keys, err := releaseKeys()
	if err != nil || len(keys) != len(daemon) {
		t.Fatalf("releaseKeys = %d keys, %v; want %d", len(keys), err, len(daemon))
	}
}

// signed is an offer for version signed by priv, as cmd/sign-daemon signs one.
func signed(priv ed25519.PrivateKey, version string) Offer {
	sha := strings.Repeat("a", 64)
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("agentd\n"+version+"\n"+sha+"\n")))
	return Offer{Version: version, URL: "/download/v" + version + "/agentd-" + version + ".tgz", SHA256: sha, Signature: sig}
}

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// A release signed by the backup key is the way out of a lost primary key; the offer check must take it as daemons do.
func TestACheckAcceptsASignatureOfAnyKeyOfTheSet(t *testing.T) {
	pubA, privA := newKey(t)
	pubB, privB := newKey(t)
	_, privC := newKey(t)
	set := []ed25519.PublicKey{pubA, pubB}
	for name, c := range map[string]struct {
		offer Offer
		ok    bool
	}{
		"signed by the first key":  {signed(privA, "1.2.3"), true},
		"signed by the second key": {signed(privB, "1.2.3"), true},
		"signed by a stranger":     {signed(privC, "1.2.3"), false},
		"a version nobody signed":  {Offer{Version: "9.9.9", SHA256: strings.Repeat("a", 64), Signature: signed(privA, "1.2.3").Signature}, false},
	} {
		if err := check(c.offer, set); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok = %v", name, err, c.ok)
		}
	}
	if err := check(signed(privA, "1.2.3"), nil); err == nil {
		t.Error("an empty key set accepted an offer")
	}
}

func TestFetchTakesAnOfferSignedByTheBackupKey(t *testing.T) {
	pubA, _ := newKey(t)
	pubB, privB := newKey(t)
	body, _ := json.Marshal(signed(privB, "3.0.0"))
	srv := releases(t, func() (int, []byte) { return http.StatusOK, body })
	o, err := fetch(t.Context(), srv.Client(), srv.URL, []ed25519.PublicKey{pubA, pubB})
	if err != nil || o.Version != "3.0.0" {
		t.Fatalf("fetch = %+v, %v; want the backup-signed 3.0.0", o, err)
	}
	if _, err := fetch(t.Context(), srv.Client(), srv.URL, []ed25519.PublicKey{pubA}); err == nil {
		t.Fatal("the primary key alone accepted a backup-signed offer")
	}
}
