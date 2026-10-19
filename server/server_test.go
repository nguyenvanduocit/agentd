package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// release lays out one release the way scripts/build-daemon.sh does.
func release(t *testing.T, dist, version string) {
	t.Helper()
	dir := filepath.Join(dist, "download", "v"+version)
	mustWrite(t, filepath.Join(dir, "agentd-"+version+".tgz"), "tarball "+version)
	mustWrite(t, filepath.Join(dir, "latest.json"), `{"version":"`+version+`"}`)
	mustWrite(t, filepath.Join(dir, "install.sh"), "#!/bin/sh\n# "+version+"\n")
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func newHandler(t *testing.T, dist string) http.Handler {
	t.Helper()
	h, err := Handler(Config{Dist: dist})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHealthz(t *testing.T) {
	if rec := get(t, newHandler(t, t.TempDir()), "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rec.Code)
	}
}

func TestLatestRedirectsToTheLatestReleasesAsset(t *testing.T) {
	dist := t.TempDir()
	release(t, dist, "1.0.0")
	release(t, dist, "1.1.0")
	mustWrite(t, filepath.Join(dist, "latest"), "1.1.0\n")
	h := newHandler(t, dist)
	for file, want := range map[string]string{
		"latest.json":      "/download/v1.1.0/latest.json",
		"install.sh":       "/download/v1.1.0/install.sh",
		"agentd-1.1.0.tgz": "/download/v1.1.0/agentd-1.1.0.tgz",
	} {
		rec := get(t, h, "/latest/download/"+file)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != want {
			t.Errorf("%s: %d -> %q, want 302 -> %q", file, rec.Code, rec.Header().Get("Location"), want)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: the latest pointer must never be cached, got %q", file, rec.Header().Get("Cache-Control"))
		}
	}
	mustWrite(t, filepath.Join(dist, "latest"), "1.0.0\n")
	if rec := get(t, h, "/latest/download/latest.json"); rec.Header().Get("Location") != "/download/v1.0.0/latest.json" {
		t.Fatalf("a new pointer is served at once: %q", rec.Header().Get("Location"))
	}
}

func TestLatestWithoutAReleaseIs503(t *testing.T) {
	h := newHandler(t, t.TempDir())
	if rec := get(t, h, "/latest/download/latest.json"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no release published: %d", rec.Code)
	}
}

func TestALatestPointerThatIsNotAVersionIsRefused(t *testing.T) {
	dist := t.TempDir()
	for _, bad := range []string{"", "../x", "1.0.0\n2.0.0", "1.0';x", strings.Repeat("1", 80)} {
		mustWrite(t, filepath.Join(dist, "latest"), bad)
		rec := get(t, newHandler(t, dist), "/latest/download/latest.json")
		if rec.Code != http.StatusInternalServerError && rec.Code != http.StatusServiceUnavailable {
			t.Errorf("latest %q: %d, want an error", bad, rec.Code)
		}
		if rec.Header().Get("Location") != "" {
			t.Errorf("latest %q was followed to %q", bad, rec.Header().Get("Location"))
		}
	}
}

func TestAssetsOfAReleaseAreServedByTheirTag(t *testing.T) {
	dist := t.TempDir()
	release(t, dist, "1.2.3")
	h := newHandler(t, dist)
	rec := get(t, h, "/download/v1.2.3/agentd-1.2.3.tgz")
	if rec.Code != http.StatusOK || rec.Body.String() != "tarball 1.2.3" {
		t.Fatalf("tarball: %d %q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("a version's tarball never changes: %q", rec.Header().Get("Cache-Control"))
	}
	for _, file := range []string{"latest.json", "install.sh"} {
		if rec := get(t, h, "/download/v1.2.3/"+file); rec.Code != http.StatusOK {
			t.Errorf("%s: %d", file, rec.Code)
		}
	}
	if rec := get(t, h, "/download/v1.2.3/install.sh"); rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("install.sh: %q", rec.Header().Get("Cache-Control"))
	}
}

func TestUnknownAndUnsafePathsAre404(t *testing.T) {
	dist := t.TempDir()
	release(t, dist, "1.2.3")
	mustWrite(t, filepath.Join(dist, "secret"), "do not serve")
	h := newHandler(t, dist)
	for _, path := range []string{
		"/download/v9.9.9/agentd-9.9.9.tgz",
		"/download/1.2.3/agentd-1.2.3.tgz", // the tag is v<version>
		"/download/v1.2.3/nothing.txt",
		"/download/v1.2.3/..%2f..%2fsecret",
		"/download/v..%2f/agentd-1.2.3.tgz",
		"/download/v1.2.3/%2e%2e/secret",
		"/download/v1.2.3/.hidden",
		"/download/v1.2.3/../v1.2.3/agentd-1.2.3.tgz", // the mux would clean this into a redirect to a real file
		"/download/v1.2.3/",
		"/download/",
		"/latest/download/..%2fsecret",
		"/releases/latest.json", // the old layout is gone
		"/install.sh",
	} {
		if rec := get(t, h, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, rec.Code)
		}
	}
}
