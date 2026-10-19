// Package update keeps the daemon on the version the release server serves: the offer (latest.json), the signature
// of an offer, the bounded HTTP fetch, the watcher that polls the offer and the updater that installs it under
// ~/.agentd/daemon/<version>.
package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// Offer is what the release server answers: the version it serves, where to fetch it, and the release key's
// signature of both. It is the body of latest.json.
type Offer struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
}

var (
	// versionPattern: a version names a directory and is baked into the tarball's name, so nothing else may reach a
	// path. The daemon directory's own entries are excluded by reservedNames, which RE2 cannot say.
	versionPattern = regexp.MustCompile(`(?i)^[0-9a-z][0-9a-z.-]{0,63}$`)
	sha256Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	releasePattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)
	urlScheme      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*://[^/?#]*`)
	plainSegment   = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// reservedNames are the daemon directory's own entries (launcher.sh's plain_version says the same). They are not
// versions in any case, because macOS file names ignore it.
var reservedNames = []string{"current", "previous", "rejected", "crashes"}

// IsVersion reports whether s is a plain version: a name that may be a directory under ~/.agentd/daemon.
func IsVersion(s string) bool {
	for _, r := range reservedNames {
		if strings.EqualFold(s, r) {
			return false
		}
	}
	return versionPattern.MatchString(s)
}

// Retry: a failed update is retried from 30 s, doubling, up to 30 min.
var Retry = home.BackoffOptions{Base: 30 * time.Second, Max: 30 * time.Minute, Jitter: 0.2}

// isOlder is true when both are plain x.y.z releases and offered is the lower one. Anything else (a dev build) is not ordered.
func isOlder(offered, running string) bool {
	a := releasePattern.FindStringSubmatch(offered)
	b := releasePattern.FindStringSubmatch(running)
	if a == nil || b == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		if c := compareDigits(a[i], b[i]); c != 0 {
			return c < 0
		}
	}
	return false
}

// compareDigits orders two decimal strings by value, whatever their length.
func compareDigits(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

// TarballPath is the path of a version's tarball under the releases base, as GitHub Releases lays it out
// (https://github.com/<owner>/<repo>/releases/download/v<version>/agentd-<version>.tgz) and scripts/build-daemon.sh names it.
func TarballPath(version string) string {
	return "/download/v" + version + "/agentd-" + version + ".tgz"
}

// LatestURL is where the offer a daemon checks is published: the latest release's latest.json asset.
func LatestURL(base string) string {
	return base + "/latest/download/latest.json"
}

// ParseLatest reads latest.json as an offer; false when it is not four strings. Whether the values are sound is
// Decide's to say.
func ParseLatest(text string) (Offer, bool) {
	var v map[string]any
	if err := json.Unmarshal([]byte(text), &v); err != nil || v == nil {
		return Offer{}, false
	}
	var o Offer
	for key, dst := range map[string]*string{"version": &o.Version, "url": &o.URL, "sha256": &o.SHA256, "signature": &o.Signature} {
		s, ok := v[key].(string)
		if !ok {
			return Offer{}, false
		}
		*dst = s
	}
	return o, true
}

// ReleaseBase is the releases page the daemon takes updates from: $AGENTD_RELEASE_URL when lookup finds it set (empty
// switches updates off), else baked, the URL built into the binary. "" with a nil error means no updates. The scheme
// rule is the server address's (https, or http on a loopback host); the path of the page stays
// (https://github.com/<owner>/<repo>/releases), a query and a fragment are dropped, and a path segment that is not a
// plain name is refused. lookup is os.LookupEnv in the shell.
func ReleaseBase(lookup func(key string) (string, bool), baked string) (string, error) {
	raw := baked
	if v, ok := lookup("AGENTD_RELEASE_URL"); ok {
		raw = v
	}
	if raw == "" {
		return "", nil
	}
	origin, err := home.ServerOrigin(raw)
	if err != nil {
		return "", err
	}
	// The raw path: URL parsing would resolve a ".." before it could be refused.
	rest := urlScheme.ReplaceAllString(raw, "")
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	rest = regexp.MustCompile(`(?i)%2e`).ReplaceAllString(rest, ".")
	var segments []string
	for _, s := range strings.Split(rest, "/") {
		if s == "" {
			continue
		}
		if !plainSegment.MatchString(s) || s == "." || s == ".." {
			return "", fmt.Errorf(`"%s": the path of the releases page has a segment that is not a plain name`, raw)
		}
		segments = append(segments, s)
	}
	if len(segments) == 0 {
		return origin, nil
	}
	return origin + "/" + strings.Join(segments, "/"), nil
}

// IsRedirect is true for the statuses a release asset is sent on with.
func IsRedirect(status int) bool {
	switch status {
	case 301, 302, 303, 307, 308:
		return true
	}
	return false
}

// origin is scheme://host:port of u with a default port left out, as a browser's URL.origin says it.
func origin(u *url.URL) string {
	port := u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return u.Scheme + "://" + host
}

// RedirectTarget is where a redirect from `from` goes, or an error when the daemon must not follow it. GitHub sends a
// release asset to another host (an https URL), so any https target is followed: what the daemon installs is checked
// against the signed sha256, not against the host. Plain http is followed only inside the same origin (a development
// server). An empty location is a redirect without a Location header.
func RedirectTarget(from, location string) (string, error) {
	if location == "" {
		return "", errors.New("the redirect has no Location")
	}
	base, err := url.Parse(from)
	if err != nil {
		return "", fmt.Errorf(`the redirect goes from "%s", which is not a URL`, from)
	}
	to, err := base.Parse(location)
	if err != nil {
		return "", fmt.Errorf(`the redirect goes to "%s", which is not a URL`, location)
	}
	if to.Host != "" && (to.Scheme == "https" || (to.Scheme == "http" && origin(to) == origin(base))) {
		if to.Path == "" && to.Opaque == "" {
			to.Path = "/"
		}
		return to.String(), nil
	}
	return "", fmt.Errorf("the redirect goes to %s://%s, which is not https", to.Scheme, to.Host)
}

// State is what Decide needs to know about the running daemon. An empty string is "none".
type State struct {
	// Running is the version this process runs.
	Running string
	// Staged is a newer version already unpacked and pointed at by `current`, waiting for the daemon to be idle.
	Staged string
	// Rejected is a version that crashed at start twice (the launcher wrote it).
	Rejected string
}

// Kind is what to do with an offer.
type Kind int

const (
	// None: nothing to do.
	None Kind = iota
	// Unstage: the server went back to the running version, `current` must point at it again.
	Unstage
	// Waiting: the offered version is already staged.
	Waiting
	// Rejected: the offered version crashed at start twice here.
	Rejected
	// Invalid: the offer is wrong (Decision.Why says how).
	Invalid
	// Install: download, check and stage Decision.Offer.
	Install
)

// Decision is Decide's answer; Version is set for Rejected, Why for Invalid, Offer for Install.
type Decision struct {
	Kind    Kind
	Version string
	Why     string
	Offer   Offer
}

// offerProblem says why an offer cannot be installed, or "" when its shape is sound.
func offerProblem(running string, reply Offer) string {
	switch {
	case !IsVersion(reply.Version):
		return fmt.Sprintf(`version "%s" is not a plain version`, reply.Version)
	case isOlder(reply.Version, running):
		return fmt.Sprintf("version %s is older than the running %s", reply.Version, running)
	case reply.URL != TarballPath(reply.Version):
		return fmt.Sprintf(`url "%s" is not the tarball of %s`, reply.URL, reply.Version)
	case !sha256Pattern.MatchString(reply.SHA256):
		return "sha256 is not 64 lower-case hex digits"
	}
	if _, ok := DecodeSignature(reply.Signature); !ok {
		return "the signature is missing or is not 64 bytes of base64"
	}
	return ""
}

// Decide is what to do with the server's answer to hello. An empty version means the server has nothing to update
// to. The server's url, sha256 and signature are checked against the shape build-daemon.sh produces, so a reply
// cannot name another host, another path or a directory outside ~/.agentd/daemon. Whether the signature is the
// release key's is checked by the updater (SignatureProblem) before anything is downloaded. A release older than the
// running one is refused, so a replayed old signed manifest cannot roll the daemon back.
func Decide(s State, reply Offer) Decision {
	switch reply.Version {
	case "":
		return Decision{Kind: None}
	case s.Running:
		if s.Staged == "" {
			return Decision{Kind: None}
		}
		return Decision{Kind: Unstage}
	case s.Staged:
		return Decision{Kind: Waiting}
	case s.Rejected:
		return Decision{Kind: Rejected, Version: reply.Version}
	}
	if why := offerProblem(s.Running, reply); why != "" {
		return Decision{Kind: Invalid, Why: why}
	}
	return Decision{Kind: Install, Offer: reply}
}

// ParseVersionRecord reads the one line of a rollback record (`previous`, `rejected`): a version, or false when it is
// not a plain one.
func ParseVersionRecord(text string) (string, bool) {
	v := strings.TrimSpace(text)
	return v, IsVersion(v)
}
