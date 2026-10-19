// Package server is the agentd release server: it serves the installer and the signed release of the daemon from a
// directory of published artifacts. It is read-only: publishing is a build step.
//
//	dist/releases/latest.json               {version, url, sha256, signature}
//	dist/releases/agentd-<version>.tgz      the daemon tarball
package server

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"text/template"
)

// Config is where the server reads from and the origin it tells the world it has.
type Config struct {
	// Dist is the directory of published artifacts.
	Dist string
	// PublicURL is the server's own origin (https://host[:port], no path). install.sh bakes it in, so the script never
	// trusts the request's Host header.
	PublicURL string
}

type server struct {
	cfg      Config
	install  *template.Template
	manifest *manifestCache
}

// Handler serves the release server's routes.
func Handler(cfg Config) (http.Handler, error) {
	if !originPattern.MatchString(cfg.PublicURL) {
		return nil, fmt.Errorf("public URL %q is not a plain http(s) origin", cfg.PublicURL)
	}
	install, err := parseInstall()
	if err != nil {
		return nil, fmt.Errorf("parse install.sh: %w", err)
	}
	s := &server{cfg: cfg, install: install, manifest: &manifestCache{path: filepath.Join(cfg.Dist, "releases", "latest.json")}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("GET /install.sh", s.handleInstallScript)
	mux.HandleFunc("GET /releases/latest.json", s.handleLatest)
	mux.HandleFunc("GET /releases/{file}", s.handleTarball)
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
