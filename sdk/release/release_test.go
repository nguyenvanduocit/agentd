package release

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// real is latest.json of the published release v0.2.0, signed by the release key.
func real(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/latest-0.2.0.json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestAPublishedReleaseVerifies(t *testing.T) {
	var o Offer
	if err := json.Unmarshal(real(t), &o); err != nil {
		t.Fatal(err)
	}
	if err := check(o, []ed25519.PublicKey{key(t)}); err != nil {
		t.Fatalf("the v0.2.0 offer: %v", err)
	}
}

func TestAnOfferTheReleaseKeyDidNotSignIsRefused(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sign := func(version, sha string) string {
		return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte("agentd\n"+version+"\n"+sha+"\n")))
	}
	sha := strings.Repeat("a", 64)
	good := Offer{Version: "1.2.3", URL: "/download/v1.2.3/agentd-1.2.3.tgz", SHA256: sha, Signature: sign("1.2.3", sha)}
	if err := check(good, []ed25519.PublicKey{pub}); err != nil {
		t.Fatalf("a signed offer: %v", err)
	}
	for name, o := range map[string]Offer{
		"another version":    {Version: "9.9.9", SHA256: sha, Signature: good.Signature},
		"another tarball":    {Version: "1.2.3", SHA256: strings.Repeat("b", 64), Signature: good.Signature},
		"no signature":       {Version: "1.2.3", SHA256: sha},
		"a short signature":  {Version: "1.2.3", SHA256: sha, Signature: base64.StdEncoding.EncodeToString([]byte("short"))},
		"not base64":         {Version: "1.2.3", SHA256: sha, Signature: "!!!"},
		"no version":         {Version: "", SHA256: sha, Signature: sign("", sha)},
		"signed by another":  {Version: "1.2.3", SHA256: sha, Signature: good.Signature},
		"a version with a /": {Version: "1/2", SHA256: sha, Signature: sign("1/2", sha)},
	} {
		k := pub
		if name == "signed by another" {
			k = key(t)
		}
		if err := check(o, []ed25519.PublicKey{k}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// releases is a stand-in for GitHub's releases page: latest/download/latest.json redirects into the release, as
// GitHub does, and the release's file is whatever body() says.
func releases(t *testing.T, body func() (int, []byte)) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /latest/download/latest.json", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/download/v0.2.0/latest.json", http.StatusFound)
	})
	mux.HandleFunc("GET /download/v0.2.0/latest.json", func(w http.ResponseWriter, _ *http.Request) {
		code, b := body()
		w.WriteHeader(code)
		_, _ = w.Write(b)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchFollowsGitHubsRedirectAndChecksTheSignature(t *testing.T) {
	srv := releases(t, func() (int, []byte) { return http.StatusOK, real(t) })
	o, err := Fetch(t.Context(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if o.Version != "0.2.0" {
		t.Fatalf("version = %q, want 0.2.0", o.Version)
	}
}

func TestFetchRefusesWhatIsNotASignedOffer(t *testing.T) {
	var tampered map[string]string
	if err := json.Unmarshal(real(t), &tampered); err != nil {
		t.Fatal(err)
	}
	tampered["version"] = "9.9.9"
	forged, _ := json.Marshal(tampered)
	for name, reply := range map[string]struct {
		code int
		body []byte
	}{
		"not found":     {http.StatusNotFound, []byte("Not Found")},
		"not JSON":      {http.StatusOK, []byte("<html>")},
		"a forged one":  {http.StatusOK, forged},
		"too big to be": {http.StatusOK, append(real(t), make([]byte, maxOffer)...)},
	} {
		srv := releases(t, func() (int, []byte) { return reply.code, reply.body })
		if o, err := Fetch(t.Context(), srv.Client(), srv.URL); err == nil {
			t.Errorf("%s: got %+v, want an error", name, o)
		}
	}
}

func TestWatchKeepsTheLastVersionThatVerified(t *testing.T) {
	var broken atomic.Bool
	release := make(chan struct{})
	srv := releases(t, func() (int, []byte) {
		<-release
		if broken.Load() {
			return http.StatusInternalServerError, nil
		}
		return http.StatusOK, real(t)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	l := Watch(ctx, srv.Client(), srv.URL, 20*time.Millisecond)
	if got := l.Version(); got != "" {
		t.Fatalf("before the first read: %q, want empty", got)
	}
	close(release)
	waitVersion(t, l, "0.2.0")
	broken.Store(true)
	time.Sleep(100 * time.Millisecond)
	if got := l.Version(); got != "0.2.0" {
		t.Fatalf("after failed reads: %q, want 0.2.0 still", got)
	}
}

func waitVersion(t *testing.T, l *Latest, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for l.Version() != want {
		if time.Now().After(deadline) {
			t.Fatalf("version = %q, want %q", l.Version(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestOlderOrdersPlainReleasesOnly(t *testing.T) {
	for _, c := range []struct {
		version, than string
		want          bool
	}{
		{"0.1.9", "0.2.0", true},
		{"0.9.0", "0.10.0", true},
		{"1.0.0", "1.0.1", true},
		{"0.2.0", "0.2.0", false},
		{"0.10.0", "0.9.0", false},
		{"0.0.0-dev", "0.2.0", false},
		{"0.2.0", "", false},
		{"", "0.2.0", false},
	} {
		if got := Older(c.version, c.than); got != c.want {
			t.Errorf("Older(%q, %q) = %v, want %v", c.version, c.than, got, c.want)
		}
	}
}

// key is the release key (keys/release.pub) as check takes it.
func key(t *testing.T) ed25519.PublicKey {
	t.Helper()
	keys, err := releaseKeys()
	if err != nil || len(keys) == 0 {
		t.Fatalf("releaseKeys = %d keys, %v", len(keys), err)
	}
	return keys[0]
}
