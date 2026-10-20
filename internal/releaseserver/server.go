// Package releaseserver is a local stand-in for GitHub Releases: it serves a directory of release files under the URLs GitHub
// gives them, so the daemon, install.sh and the end-to-end tests run against it without touching the network. It is
// read-only: publishing is a build step (scripts/build-daemon.sh).
//
//	dist/download/v<version>/agentd-<version>.tgz   the daemon tarball
//	dist/download/v<version>/latest.json            {version, url, sha256, signature}
//	dist/download/v<version>/install.sh             the installer of that release
//	dist/latest                                     the version GitHub would call the latest release
//
// GET /download/v<version>/<file> serves a file; GET /latest/download/<file> redirects to it in the latest release, as
// github.com/<owner>/<repo>/releases/latest/download/<file> does.
package releaseserver

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.-]{0,63}$`)
	// assetPattern is a file name of a release: no separator, no leading dot, so no path leaves the release's directory.
	assetPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,127}$`)
)

// Config is where the server reads from.
type Config struct {
	// Dist is the directory of release files.
	Dist string
}

type server struct{ cfg Config }

// Handler serves the stand-in's routes.
func Handler(cfg Config) (http.Handler, error) {
	if cfg.Dist == "" {
		return nil, errors.New("dist directory is empty")
	}
	s := &server{cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /latest/download/{file}", s.handleLatest)
	mux.HandleFunc("GET /download/{tag}/{file}", s.handleAsset)
	return refuseDotDot(mux), nil
}

// refuseDotDot answers 404 to any path with ".." in it, before the mux would clean it into another route.
func refuseDotDot(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "..") {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// latestVersion reads dist/latest.
func (s *server) latestVersion() (string, error) {
	raw, err := os.ReadFile(filepath.Join(s.cfg.Dist, "latest")) //nolint:gosec // G304: the server's own dist directory plus a constant name
	if errors.Is(err, fs.ErrNotExist) {
		return "", errNoRelease
	}
	if err != nil {
		return "", fmt.Errorf("read latest: %w", err)
	}
	v := strings.TrimSuffix(string(raw), "\n")
	if !versionPattern.MatchString(v) {
		return "", fmt.Errorf("latest %q is not a plain version", v)
	}
	return v, nil
}

var errNoRelease = errors.New("no release is published")

// handleLatest is GET /latest/download/<file>: a redirect to the file in the latest release, never cached.
func (s *server) handleLatest(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	if !assetPattern.MatchString(file) {
		http.NotFound(w, r)
		return
	}
	v, err := s.latestVersion()
	if errors.Is(err, errNoRelease) {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		log.Printf("agentd-server: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/download/v"+v+"/"+file, http.StatusFound)
}

// handleAsset is GET /download/v<version>/<file>.
func (s *server) handleAsset(w http.ResponseWriter, r *http.Request) {
	tag, file := r.PathValue("tag"), r.PathValue("file")
	version, ok := strings.CutPrefix(tag, "v")
	if !ok || !versionPattern.MatchString(version) || !assetPattern.MatchString(file) {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(file, ".tgz") {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, os.DirFS(filepath.Join(s.cfg.Dist, "download", tag)), file) //nolint:gosec // G703: tag and file match their patterns, and an fs.FS refuses paths outside its root
}
