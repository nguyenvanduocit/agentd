// Package release reads which daemon version agentd's releases page offers: latest.json, the signed file daemons
// update from. An app's server uses it for hub.Options.LatestVersion, so that its UI can tell a daemon that will
// update soon from one that is current.
//
//	latest := release.Watch(ctx, http.DefaultClient, release.Releases, time.Hour)
//	h := hub.New(hub.Options{Tokens: tokens, LatestVersion: latest.Version})
//	// a daemon of List(user) is behind when release.Older(daemon.Version, list.LatestVersion)
package release

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
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

// releaseKey is the release key's public half, base64 of its SPKI DER: daemon/release.pub, the key every daemon checks
// an update against (a test keeps the two the same).
const releaseKey = "MCowBQYDK2VwAyEACRLf/5ptlco5rJjDDSKjgxflq8DahAPzA4i71jE1Q30="

// maxOffer bounds what Fetch reads: latest.json is a few hundred bytes.
const maxOffer = 64 << 10

var (
	versionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.-]{0,63}$`)
	plainRelease   = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)
)

// Offer is latest.json: the newest release's version, where its tarball is, the tarball's sha256, and the release
// key's signature of the version and the sha256.
type Offer struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
}

// Fetch reads the latest release's latest.json under releases (Releases, or a stand-in with the same layout) and
// returns it once the release key's signature verifies: a version daemons would refuse is not one they update to.
func Fetch(ctx context.Context, client *http.Client, releases string) (Offer, error) {
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
	k, err := publicKey()
	if err != nil {
		return Offer{}, err
	}
	if err := check(o, k); err != nil {
		return Offer{}, fmt.Errorf("release: %s: %w", url, err)
	}
	return o, nil
}

func publicKey() (ed25519.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(releaseKey)
	if err != nil {
		return nil, fmt.Errorf("release: the release key: %w", err)
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("release: the release key: %w", err)
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("release: the release key is not an Ed25519 key")
	}
	return pub, nil
}

// check is nil when key signed o's version and sha256, as scripts/sign-daemon.ts signs them (and as the daemon
// checks them, daemon/src/core/signature.ts).
func check(o Offer, key ed25519.PublicKey) error {
	if !versionPattern.MatchString(o.Version) {
		return fmt.Errorf("version %q is not a plain version", o.Version)
	}
	sig, err := base64.StdEncoding.DecodeString(o.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("the signature is not 64 bytes of base64")
	}
	if !ed25519.Verify(key, []byte("agentd\n"+o.Version+"\n"+o.SHA256+"\n"), sig) {
		return fmt.Errorf("the signature of %s does not match the release key", o.Version)
	}
	return nil
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
// can be hub.Options.LatestVersion.
func (l *Latest) Version() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.version
}

// Watch reads the latest release in the background, at once and then every `every`, until ctx ends. A read that fails
// is logged and keeps the version read before.
func Watch(ctx context.Context, client *http.Client, releases string, every time.Duration) *Latest {
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
