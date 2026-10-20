// Package release reads which daemon version agentd's releases page offers: latest.json, the signed file daemons
// update from. An app's server uses it for goserver.Options.LatestVersion, so that its UI can tell a daemon that will
// update soon from one that is current.
//
//	latest := release.Watch(ctx, http.DefaultClient, release.Releases, time.Hour)
//	h := goserver.New(goserver.Options{Tokens: tokens, LatestVersion: latest.Version})
//	// a daemon of List(user) is behind when release.Older(daemon.Version, list.LatestVersion)
//
// An offer counts only when a release key signed it, the same keys daemons check updates against: the release key and,
// once the owner has made one, the backup key kept offline (the *.pub files of keys/, which a test keeps the same as
// daemon/release*.pub).
package release

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"embed"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Releases is agentd's releases page on GitHub; the latest release's latest.json is at latest/download/latest.json.
const Releases = "https://github.com/nguyenvanduocit/agentd/releases"

// keyFiles are the public halves of the release keys, as PEM files: keys/release.pub is daemon/release.pub, and a
// keys/release-backup.pub that appears with daemon/release-backup.pub is taken without a change of code.
//
//go:embed keys/*.pub
var keyFiles embed.FS

// releaseKeys reads every embedded release key.
func releaseKeys() ([]ed25519.PublicKey, error) {
	files, err := keyFiles.ReadDir("keys")
	if err != nil {
		return nil, fmt.Errorf("release: the release keys: %w", err)
	}
	var keys []ed25519.PublicKey
	for _, f := range files {
		text, err := keyFiles.ReadFile("keys/" + f.Name())
		if err != nil {
			return nil, fmt.Errorf("release: the release key %s: %w", f.Name(), err)
		}
		block, _ := pem.Decode(text)
		if block == nil {
			return nil, fmt.Errorf("release: the release key %s is not PEM", f.Name())
		}
		k, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("release: the release key %s: %w", f.Name(), err)
		}
		pub, ok := k.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("release: the release key %s is not an Ed25519 key", f.Name())
		}
		keys = append(keys, pub)
	}
	return keys, nil
}

// maxOffer bounds what Fetch reads: latest.json is a few hundred bytes.
const maxOffer = 64 << 10

var (
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.-]{0,63}$`)
	plainRelease   = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)
)

// Offer is latest.json: the newest release's version, where its tarball is, the tarball's sha256, and the release
// key's signature of the version and the sha256.
type Offer struct {
	// Version is the release's version ("0.7.0").
	Version string `json:"version"`
	// URL is where the tarball is, relative to the releases page.
	URL string `json:"url"`
	// SHA256 is the hex SHA-256 of the tarball.
	SHA256 string `json:"sha256"`
	// Signature is a release key's Ed25519 signature of the version and the SHA-256, in base64.
	Signature string `json:"signature"`
}

// Fetch reads the latest release's latest.json under releases (Releases, or a stand-in with the same layout) and
// returns it once a release key's signature verifies: a version daemons would refuse is not one they update to. A nil
// client is http.DefaultClient.
func Fetch(ctx context.Context, client *http.Client, releases string) (Offer, error) {
	keys, err := releaseKeys()
	if err != nil {
		return Offer{}, err
	}
	return fetch(ctx, client, releases, keys)
}

// fetch is Fetch against the keys it is given.
func fetch(ctx context.Context, client *http.Client, releases string, keys []ed25519.PublicKey) (Offer, error) {
	if client == nil {
		client = http.DefaultClient
	}
	url := strings.TrimSuffix(releases, "/") + "/latest/download/latest.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return Offer{}, fmt.Errorf("release: %w", err)
	}
	res, err := client.Do(req)
	if err != nil {
		return Offer{}, fmt.Errorf("release: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return Offer{}, fmt.Errorf("release: GET %s: %s", url, res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxOffer+1))
	if err != nil {
		return Offer{}, fmt.Errorf("release: read %s: %w", url, err)
	}
	if len(body) > maxOffer {
		return Offer{}, fmt.Errorf("release: %s is larger than %d bytes", url, maxOffer)
	}
	var o Offer
	if err := json.Unmarshal(body, &o); err != nil {
		return Offer{}, fmt.Errorf("release: %s is not an offer: %w", url, err)
	}
	if err := check(o, keys); err != nil {
		return Offer{}, fmt.Errorf("release: %s: %w", url, err)
	}
	return o, nil
}

// check is nil when one of keys signed o's version and sha256, as cmd/sign-daemon signs them (and as the daemon
// checks them, daemon/update/signature.go). With no key it refuses everything.
func check(o Offer, keys []ed25519.PublicKey) error {
	if !versionPattern.MatchString(o.Version) {
		return fmt.Errorf("version %q is not a plain version", o.Version)
	}
	sig, err := base64.StdEncoding.DecodeString(o.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("the signature is not 64 bytes of base64")
	}
	message := []byte("agentd\n" + o.Version + "\n" + o.SHA256 + "\n")
	for _, key := range keys {
		if ed25519.Verify(key, message, sig) {
			return nil
		}
	}
	if len(keys) > 1 {
		return fmt.Errorf("the signature of %s does not match any of the release keys", o.Version)
	}
	return fmt.Errorf("the signature of %s does not match the release key", o.Version)
}

// Older is true when both are plain x.y.z releases and version is the lower one. Anything else (a dev build, an empty
// version) is not ordered, so it is never older: the daemon orders versions the same way.
func Older(version, than string) bool {
	a, b := plainRelease.FindStringSubmatch(version), plainRelease.FindStringSubmatch(than)
	if a == nil || b == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(a[i])
		y, _ := strconv.Atoi(b[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// Latest is the newest daemon version the releases page offers, as Watch last read it.
type Latest struct {
	mu      sync.Mutex
	version string
}

// Version is the version of the last offer that verified: empty until one has. It never waits on the network, so it
// can be goserver.Options.LatestVersion.
func (l *Latest) Version() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.version
}

// Watch reads the latest release in the background, at once and then every `every`, until ctx ends. A read that fails
// is logged and keeps the version read before. An interval of zero or less is one hour, and a nil client is
// http.DefaultClient.
func Watch(ctx context.Context, client *http.Client, releases string, every time.Duration) *Latest {
	if every <= 0 {
		every = time.Hour
	}
	l := &Latest{}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			if o, err := Fetch(ctx, client, releases); err != nil {
				if ctx.Err() == nil {
					log.Printf("release: the latest daemon version: %v", err)
				}
			} else {
				l.mu.Lock()
				l.version = o.Version
				l.mu.Unlock()
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	return l
}
