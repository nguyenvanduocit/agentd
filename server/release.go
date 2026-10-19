package server

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"text/template"
	"time"

	_ "embed"
)

// nodeVersion is the Node.js LTS release install.sh downloads (from nodejs.org/dist, checked against that
// release's SHASUMS256.txt) when the machine has no Node >= 20 with npm. Moving to a newer LTS is this one line.
const nodeVersion = "24.21.0"

// The values baked into install.sh go inside single quotes of a script the user runs, so each one must match a
// pattern that contains no quote, no `$`, no backtick, no space and no newline before it is rendered.
var (
	originPattern  = regexp.MustCompile(`^https?://([A-Za-z0-9.-]+|\[[0-9A-Fa-f:]+\])(:\d{1,5})?$`)
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.-]{0,63}$`)
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// tarballName is the only shape of file served under /releases/: the version is in the name, so a URL never
	// changes content. No path separator can match it.
	tarballName = regexp.MustCompile(`^agentd-[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}\.tgz$`)
)

//go:embed install.sh.tmpl
var installTemplate string

// manifest is releases/latest.json, which scripts/build-daemon.sh writes next to the tarball. Signature is the
// release key's Ed25519 signature (base64) of the version and sha256; the server only passes it on, a daemon checks it
// before it updates.
type manifest struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
}

var errNoRelease = errors.New("no daemon release is published")

func tarballPath(version string) string { return "/releases/agentd-" + version + ".tgz" }

// check says whether every value of the manifest has the shape build-daemon.sh produces.
func (m manifest) check() error {
	switch {
	case !versionPattern.MatchString(m.Version) || strings.Contains(m.Version, ".."):
		return fmt.Errorf("version %q is not a plain version", m.Version)
	case !sha256Pattern.MatchString(m.SHA256):
		return fmt.Errorf("sha256 %q is not 64 lower-case hex digits", m.SHA256)
	case m.URL != tarballPath(m.Version):
		return fmt.Errorf("url %q is not the tarball of version %s", m.URL, m.Version)
	case !isSignature(m.Signature):
		return fmt.Errorf("signature %q is not 64 bytes of base64", m.Signature)
	}
	return nil
}

// isSignature is whether s has the shape of an Ed25519 signature: canonical padded base64 of 64 bytes (the decoder
// skips newlines, so the bytes must also encode back to s).
func isSignature(s string) bool {
	raw, err := base64.StdEncoding.Strict().DecodeString(s)
	return err == nil && len(raw) == ed25519.SignatureSize && base64.StdEncoding.EncodeToString(raw) == s
}

// manifestCache keeps the last read of latest.json until the file changes (its modification time or its size), so
// that every request does not read the disk.
type manifestCache struct {
	mu    sync.Mutex
	path  string
	mtime time.Time
	size  int64
	m     manifest
	err   error
	have  bool
}

func (c *manifestCache) get() (manifest, error) {
	info, err := os.Stat(c.path)
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.have = false
		if errors.Is(err, fs.ErrNotExist) {
			return manifest{}, errNoRelease
		}
		return manifest{}, fmt.Errorf("stat latest.json: %w", err)
	}
	if c.have && c.mtime.Equal(info.ModTime()) && c.size == info.Size() {
		return c.m, c.err
	}
	c.m, c.err = readManifest(c.path)
	c.mtime, c.size, c.have = info.ModTime(), info.Size(), true
	return c.m, c.err
}

func readManifest(path string) (manifest, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: path is the server's own dist directory plus a constant name
	if err != nil {
		return manifest{}, fmt.Errorf("read latest.json: %w", err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return manifest{}, fmt.Errorf("latest.json is not valid JSON: %w", err)
	}
	if err := m.check(); err != nil {
		return manifest{}, err
	}
	return m, nil
}

// installValues are the values of one rendered install.sh.
type installValues struct {
	Origin      string // the server's PUBLIC_URL: https://host[:port], no path
	Version     string
	TarballURL  string // Origin + the tarball's path
	SHA256      string
	NodeVersion string
}

func (s *server) renderInstall(m manifest) ([]byte, error) {
	var out bytes.Buffer
	v := installValues{Origin: s.cfg.PublicURL, Version: m.Version, TarballURL: s.cfg.PublicURL + m.URL, SHA256: m.SHA256, NodeVersion: nodeVersion}
	if err := s.install.Execute(&out, v); err != nil {
		return nil, fmt.Errorf("render install.sh: %w", err)
	}
	return out.Bytes(), nil
}

func parseInstall() (*template.Template, error) {
	return template.New("install.sh").Option("missingkey=error").Parse(installTemplate)
}

// releaseError answers a failed read of the manifest: nothing published is a 503, a manifest of the wrong shape a 500.
func releaseError(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, errNoRelease) {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	log.Printf("agentd-server: %s: %v", what, err)
	http.Error(w, what+" cannot be served: "+err.Error(), http.StatusInternalServerError)
}

// handleLatest is GET /releases/latest.json: the offer a daemon checks, never cached.
func (s *server) handleLatest(w http.ResponseWriter, _ *http.Request) {
	m, err := s.manifest.get()
	if err != nil {
		releaseError(w, "latest.json", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(m); err != nil {
		log.Printf("agentd-server: latest.json: write: %v", err)
	}
}

// handleInstallScript is GET /install.sh: the installer for the published release, with this server's public URL (never
// the request's Host) and that release's version and sha256 baked in. A new release is served at once, and the script
// is never cached by the browser.
func (s *server) handleInstallScript(w http.ResponseWriter, _ *http.Request) {
	m, err := s.manifest.get()
	var script []byte
	if err == nil {
		script, err = s.renderInstall(m)
	}
	if err != nil {
		releaseError(w, "install.sh", err)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if _, err := w.Write(script); err != nil {
		log.Printf("agentd-server: install.sh: write: %v", err)
	}
}

// handleTarball is GET /releases/agentd-<version>.tgz.
func (s *server) handleTarball(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if !tarballName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeFileFS(w, r, os.DirFS(filepath.Join(s.cfg.Dist, "releases")), name) //nolint:gosec // G703: name matches tarballName, and an fs.FS refuses paths outside its root
}
