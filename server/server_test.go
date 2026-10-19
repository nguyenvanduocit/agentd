package server

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const publicURL = "http://127.0.0.1:7100"

// goodSignature has the shape of an Ed25519 signature (64 bytes, canonical base64); the server only passes it on.
var goodSignature = base64.StdEncoding.EncodeToString(make([]byte, 64))

func mustWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// tgz is an npm-layout tarball whose package/package.json is `manifest`.
func tgz(t *testing.T, manifest map[string]any) []byte {
	t.Helper()
	pkg, _ := json.Marshal(manifest)
	var raw strings.Builder
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(pkg))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(pkg); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return []byte(raw.String())
}

// release writes a daemon release into dist and returns its manifest.
func release(t *testing.T, dist, version string, body []byte) manifest {
	t.Helper()
	sum := sha256Hex(body)
	m := manifest{Version: version, URL: "/releases/agentd-" + version + ".tgz", SHA256: sum, Signature: goodSignature}
	mustWrite(t, filepath.Join(dist, "releases", "agentd-"+version+".tgz"), body)
	raw, _ := json.Marshal(m)
	mustWrite(t, filepath.Join(dist, "releases", "latest.json"), raw)
	return m
}

func get(t *testing.T, h http.Handler, path string, host string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if host != "" {
		req.Host = host
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newHandler(t *testing.T, dist string) http.Handler {
	t.Helper()
	h, err := Handler(Config{Dist: dist, PublicURL: publicURL})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHandlerRefusesAPublicURLThatIsNotAnOrigin(t *testing.T) {
	for _, bad := range []string{"", "mindchains.example.com", "https://a.b/path", "https://a.b'; rm -rf /", "ftp://a.b"} {
		if _, err := Handler(Config{Dist: t.TempDir(), PublicURL: bad}); err == nil {
			t.Errorf("accepted public URL %q", bad)
		}
	}
}

func TestHealthz(t *testing.T) {
	if rec := get(t, newHandler(t, t.TempDir()), "/healthz", ""); rec.Code != 200 {
		t.Fatalf("healthz: %d", rec.Code)
	}
}

func TestLatestManifest(t *testing.T) {
	dist := t.TempDir()
	h := newHandler(t, dist)
	if rec := get(t, h, "/releases/latest.json", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no release yet: %d", rec.Code)
	}
	want := release(t, dist, "1.2.3", []byte("tarball"))
	rec := get(t, h, "/releases/latest.json", "")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("latest: %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	var got manifest
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got != want {
		t.Fatalf("manifest %+v (%v), want %+v", got, err, want)
	}
}

func TestLatestManifestThatDoesNotMatchTheShapeIsRefused(t *testing.T) {
	for name, m := range map[string]manifest{
		"url of another version": {Version: "1.0.0", URL: "/releases/agentd-2.0.0.tgz", SHA256: strings.Repeat("a", 64), Signature: goodSignature},
		"sha256 not hex":         {Version: "1.0.0", URL: "/releases/agentd-1.0.0.tgz", SHA256: "xyz", Signature: goodSignature},
		"signature too short":    {Version: "1.0.0", URL: "/releases/agentd-1.0.0.tgz", SHA256: strings.Repeat("a", 64), Signature: "AAAA"},
		"quote in the version":   {Version: "1.0';x", URL: "/releases/agentd-1.0';x.tgz", SHA256: strings.Repeat("a", 64), Signature: goodSignature},
		"path in the version":    {Version: "../x", URL: "/releases/agentd-../x.tgz", SHA256: strings.Repeat("a", 64), Signature: goodSignature},
	} {
		dist := t.TempDir()
		raw, _ := json.Marshal(m)
		mustWrite(t, filepath.Join(dist, "releases", "latest.json"), raw)
		h := newHandler(t, dist)
		for _, path := range []string{"/releases/latest.json", "/install.sh"} {
			if rec := get(t, h, path, ""); rec.Code != http.StatusInternalServerError {
				t.Errorf("%s %s: %d, want 500", name, path, rec.Code)
			}
		}
	}
}

func TestInstallScriptBakesInThePublicURLAndTheBuild(t *testing.T) {
	dist := t.TempDir()
	h := newHandler(t, dist)
	if rec := get(t, h, "/install.sh", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no release yet: %d", rec.Code)
	}
	m := release(t, dist, "1.2.3", []byte("tarball"))
	rec := get(t, h, "/install.sh", "evil.example") // the Host header must not reach the script
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/x-shellscript") || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("install.sh: %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
	body := rec.Body.String()
	for _, want := range []string{"ORIGIN='" + publicURL + "'", "VERSION='1.2.3'", "TARBALL_URL='" + publicURL + m.URL + "'", "SHA256='" + m.SHA256 + "'", "--app <origin> --code <code>"} {
		if !strings.Contains(body, want) {
			t.Errorf("install.sh lacks %q", want)
		}
	}
	if strings.Contains(body, "evil.example") {
		t.Error("the request's Host reached the script")
	}
	if !strings.HasPrefix(body, "#!/usr/bin/env bash") {
		t.Errorf("not a bash script: %.40q", body)
	}
}

func TestInstallScriptFollowsANewRelease(t *testing.T) {
	dist := t.TempDir()
	h := newHandler(t, dist)
	release(t, dist, "1.0.0", []byte("one"))
	if !strings.Contains(get(t, h, "/install.sh", "").Body.String(), "VERSION='1.0.0'") {
		t.Fatal("first release not served")
	}
	release(t, dist, "1.1.0", []byte("two-longer"))
	if !strings.Contains(get(t, h, "/install.sh", "").Body.String(), "VERSION='1.1.0'") {
		t.Fatal("new release not served at once")
	}
}

func TestTarball(t *testing.T) {
	dist := t.TempDir()
	h := newHandler(t, dist)
	body := []byte("the daemon bytes")
	release(t, dist, "1.2.3", body)
	rec := get(t, h, "/releases/agentd-1.2.3.tgz", "")
	if rec.Code != 200 || rec.Body.String() != string(body) || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("tarball: %d %q %q", rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"))
	}
	for _, path := range []string{"/releases/agentd-9.9.9.tgz", "/releases/latest.json.tgz", "/releases/agentd-..%2f..%2fetc.tgz", "/releases/agentd-1.2.3.tgz/x", "/releases/agentd-.tgz"} {
		if rec := get(t, h, path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, rec.Code)
		}
	}
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
