package update

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

const testSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// testSig is 64 bytes of 7: the shape of a signature, not one that verifies.
var testSig = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 64))

func offer(version string, edit ...func(*Offer)) Offer {
	o := Offer{Version: version, URL: TarballPath(version), SHA256: testSHA, Signature: testSig}
	for _, e := range edit {
		e(&o)
	}
	return o
}

func state(edit ...func(*State)) State {
	s := State{Running: "1.0.0"}
	for _, e := range edit {
		e(&s)
	}
	return s
}

func TestDecide(t *testing.T) {
	t.Run("an empty version: the server has nothing to update to", func(t *testing.T) {
		if d := Decide(state(), Offer{}); d.Kind != None {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("the running version needs nothing, whatever url and sha the server sends with it", func(t *testing.T) {
		if d := Decide(state(), Offer{Version: "1.0.0"}); d.Kind != None {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("a different version is an update", func(t *testing.T) {
		d := Decide(state(), offer("1.1.0"))
		if d.Kind != Install || d.Offer != offer("1.1.0") {
			t.Fatalf("got %+v", d)
		}
	})
	t.Run("an older release than the running one is refused, a replayed signed manifest included", func(t *testing.T) {
		cases := []struct {
			running, offered string
			want             Kind
		}{
			{"1.2.0", "1.1.0", Invalid},
			{"1.10.0", "1.9.0", Invalid},
			{"1.2.0", "1.10.0", Install},
		}
		for _, c := range cases {
			if d := Decide(state(func(s *State) { s.Running = c.running }), offer(c.offered)); d.Kind != c.want {
				t.Errorf("running %s, offered %s: got %+v", c.running, c.offered, d)
			}
		}
	})
	t.Run("versions that are not x.y.z (a dev build) are not ordered, so any signed offer may replace them", func(t *testing.T) {
		if d := Decide(state(func(s *State) { s.Running = "dev" }), offer("1.0.0")); d.Kind != Install {
			t.Errorf("got %+v", d)
		}
		if d := Decide(state(func(s *State) { s.Running = "1.2.0" }), offer("1.1.0-rc1")); d.Kind != Install {
			t.Errorf("got %+v", d)
		}
	})
	t.Run("a staged version waits; going back to the running version unstages", func(t *testing.T) {
		staged := func(s *State) { s.Staged = "1.1.0" }
		if d := Decide(state(staged), offer("1.1.0")); d.Kind != Waiting {
			t.Errorf("got %+v", d)
		}
		if d := Decide(state(staged), offer("1.0.0")); d.Kind != Unstage {
			t.Errorf("got %+v", d)
		}
		if d := Decide(state(staged), offer("1.2.0")); d.Kind != Install {
			t.Errorf("got %+v", d)
		}
	})
	t.Run("a rejected version is not tried again; another one is", func(t *testing.T) {
		rejected := func(s *State) { s.Rejected = "1.1.0" }
		if d := Decide(state(rejected), offer("1.1.0")); d.Kind != Rejected || d.Version != "1.1.0" {
			t.Errorf("got %+v", d)
		}
		if d := Decide(state(rejected), offer("1.2.0")); d.Kind != Install {
			t.Errorf("got %+v", d)
		}
	})
	t.Run("a reply that could reach outside the daemon directory or another host is invalid", func(t *testing.T) {
		for _, bad := range []Offer{
			offer("../evil"),
			offer("1.1.0/../../x"),
			offer(".hidden"),
			offer("1.1.0", func(o *Offer) { o.URL = "https://evil.example/releases/agentd-1.1.0.tgz" }),
			offer("1.1.0", func(o *Offer) { o.URL = "/releases/agentd-1.2.0.tgz" }),
			offer("1.1.0", func(o *Offer) { o.URL = TarballPath("1.1.0") + "?x=1" }),
			offer("1.1.0", func(o *Offer) { o.SHA256 = strings.Repeat("A", 64) }),
			offer("1.1.0", func(o *Offer) { o.SHA256 = "abc" }),
		} {
			if d := Decide(state(), bad); d.Kind != Invalid {
				t.Errorf("%+v: got %+v", bad, d)
			}
		}
	})
	t.Run("an offer without a signature of the right shape is invalid", func(t *testing.T) {
		sig := func(s string) Offer { return offer("1.1.0", func(o *Offer) { o.Signature = s }) }
		for _, bad := range []Offer{
			sig(""),
			sig(testSig[:len(testSig)-2]),
			sig(base64.StdEncoding.EncodeToString(make([]byte, 63))),
			sig(testSig + "\n"),
		} {
			if d := Decide(state(), bad); d.Kind != Invalid {
				t.Errorf("%q: got %+v", bad.Signature, d)
			}
		}
	})
	t.Run("the names of the daemon directory's own entries are not versions, in any case", func(t *testing.T) {
		for _, name := range []string{"current", "previous", "rejected", "crashes", "Current", "CRASHES"} {
			if d := Decide(state(), offer(name)); d.Kind != Invalid {
				t.Errorf("%s: got %+v", name, d)
			}
		}
		for _, name := range []string{"currently", "0.0.0-gcurrent"} {
			if d := Decide(state(), offer(name)); d.Kind != Install {
				t.Errorf("%s: got %+v", name, d)
			}
		}
	})
}

func TestParseVersionRecord(t *testing.T) {
	if v, ok := ParseVersionRecord("1.2.3\n"); !ok || v != "1.2.3" {
		t.Errorf("got %q %v", v, ok)
	}
	for _, bad := range []string{"", "../x", "a b", "current\n"} {
		if v, ok := ParseVersionRecord(bad); ok {
			t.Errorf("%q: got %q", bad, v)
		}
	}
}

func TestReleaseLayout(t *testing.T) {
	t.Run("the layout is GitHub Releases': a tarball under download/v<version>, the offer under latest/download", func(t *testing.T) {
		if got := TarballPath("1.2.3"); got != "/download/v1.2.3/agentd-1.2.3.tgz" {
			t.Error(got)
		}
		if got := LatestURL("https://github.com/o/r/releases"); got != "https://github.com/o/r/releases/latest/download/latest.json" {
			t.Error(got)
		}
		if got := LatestURL("http://127.0.0.1:7100"); got != "http://127.0.0.1:7100/latest/download/latest.json" {
			t.Error(got)
		}
	})
	t.Run("ParseLatest reads the four fields of latest.json", func(t *testing.T) {
		got, ok := ParseLatest(`{"version":"1.2.3","url":"` + TarballPath("1.2.3") + `","sha256":"` + testSHA + `","signature":"` + testSig + `"}`)
		if !ok || got != offer("1.2.3") {
			t.Errorf("got %+v %v", got, ok)
		}
	})
	t.Run("ParseLatest refuses what is not an offer: not JSON, a missing or non-string field, an array", func(t *testing.T) {
		for _, bad := range []string{
			"", "oops", "[]", "null",
			`{"version":"1.0.0"}`,
			`{"version":"1.0.0","url":"u","sha256":5,"signature":"s"}`,
			`{"version":"1.0.0","url":"u","sha256":"h"}`,
			`{"version":"1.0.0","url":"u","sha256":"h","signature":null}`,
		} {
			if got, ok := ParseLatest(bad); ok {
				t.Errorf("%q: got %+v", bad, got)
			}
		}
	})
}

func env(kv map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := kv[k]; return v, ok }
}

func TestReleaseBase(t *testing.T) {
	ok := func(e map[string]string, baked, want string) {
		t.Helper()
		got, err := ReleaseBase(env(e), baked)
		if err != nil || got != want {
			t.Errorf("env %v baked %q: got %q, %v; want %q", e, baked, got, err, want)
		}
	}
	fails := func(e map[string]string, baked, contains string) {
		t.Helper()
		got, err := ReleaseBase(env(e), baked)
		if err == nil || !strings.Contains(err.Error(), contains) {
			t.Errorf("env %v baked %q: got %q, %v; want an error with %q", e, baked, got, err, contains)
		}
	}
	t.Run("the environment replaces what was baked in at build time, and empty means no updates", func(t *testing.T) {
		ok(nil, "https://baked.example.com", "https://baked.example.com")
		ok(map[string]string{"AGENTD_RELEASE_URL": "https://env.example.com/"}, "https://baked.example.com", "https://env.example.com")
		ok(map[string]string{"AGENTD_RELEASE_URL": ""}, "https://baked.example.com", "")
		ok(nil, "", "")
	})
	t.Run("keeps the path of the releases page, without a trailing slash, query or fragment", func(t *testing.T) {
		ok(nil, "https://github.com/nguyenvanduocit/agentd/releases", "https://github.com/nguyenvanduocit/agentd/releases")
		ok(nil, "https://github.com/o/r/releases/?x=1#y", "https://github.com/o/r/releases")
		fails(nil, "https://github.com/o/../r", "path")
		fails(nil, "https://github.com/o/%2E%2e/r", "path")
		if _, err := ReleaseBase(env(nil), "https://github.com/o/r s"); err == nil {
			t.Error("a space in the path was accepted")
		}
	})
	t.Run("accepts https anywhere and http only on loopback: an update must not travel in the clear", func(t *testing.T) {
		ok(map[string]string{"AGENTD_RELEASE_URL": "http://127.0.0.1:7100"}, "", "http://127.0.0.1:7100")
		fails(map[string]string{"AGENTD_RELEASE_URL": "http://agentd.example.com"}, "", "https")
		fails(map[string]string{"AGENTD_RELEASE_URL": "nonsense"}, "", "not a URL")
	})
}

func TestRedirectTarget(t *testing.T) {
	t.Run("a redirect to https goes on, whichever host: the signature, not the host, is what is trusted", func(t *testing.T) {
		got, err := RedirectTarget("https://github.com/o/r/releases/latest/download/latest.json", "https://release-assets.githubusercontent.com/x?y=1")
		if err != nil || got != "https://release-assets.githubusercontent.com/x?y=1" {
			t.Error(got, err)
		}
		got, err = RedirectTarget("https://github.com/a/b", "/c/d")
		if err != nil || got != "https://github.com/c/d" {
			t.Error(got, err)
		}
	})
	t.Run("plain http goes on only inside the same origin (a development server), never to another host or port", func(t *testing.T) {
		got, err := RedirectTarget("http://127.0.0.1:7100/latest/download/latest.json", "/download/v1.0.0/latest.json")
		if err != nil || got != "http://127.0.0.1:7100/download/v1.0.0/latest.json" {
			t.Error(got, err)
		}
		for _, c := range [][2]string{
			{"http://127.0.0.1:7100/a", "http://127.0.0.1:7101/a"},
			{"https://github.com/a", "http://evil.example/a"},
		} {
			if got, err := RedirectTarget(c[0], c[1]); err == nil || !strings.Contains(err.Error(), "redirect") {
				t.Errorf("%v: got %q, %v", c, got, err)
			}
		}
	})
	t.Run("a redirect without a usable Location, or to another scheme, is refused", func(t *testing.T) {
		for _, bad := range []string{"", "ftp://x.example/a", "file:///etc/passwd", "javascript:alert(1)"} {
			if got, err := RedirectTarget("https://github.com/a", bad); err == nil || !strings.Contains(err.Error(), "redirect") {
				t.Errorf("%q: got %q, %v", bad, got, err)
			}
		}
	})
}
