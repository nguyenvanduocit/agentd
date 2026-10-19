package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

const (
	downloadTimeout = 120 * time.Second
	// MaxTarballBytes bounds one download.
	MaxTarballBytes int64 = 100 * 1024 * 1024
	// IdleCheck is how often a staged update looks again for an idle daemon.
	IdleCheck = 5 * time.Second
	// DefaultEntry is the daemon's file in an installed version directory: what the launcher runs under current/.
	DefaultEntry = "agentd"
	// binaryPrefix starts the name of every platform's binary in the tarball: `agentd-<os>-<arch>`.
	binaryPrefix = "agentd-"
)

// BinaryName is the file of a tarball's package/ that holds the daemon for a platform ("<GOOS>-<GOARCH>").
func BinaryName(platform string) string { return binaryPrefix + platform }

func readRecord(file string) (string, bool) {
	text, err := os.ReadFile(file)
	if err != nil {
		return "", false
	}
	return ParseVersionRecord(string(text))
}

// pointCurrent points `current` at a version by renaming a new symlink over it: a reader sees the old target or the
// new, never none.
func pointCurrent(l home.Layout, version string) error {
	tmp := path.Join(l.DaemonDir, fmt.Sprintf(".current.%d", os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Symlink(version, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.CurrentLink); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// download fetches the offer's tarball into dir/pkg.tgz; it returns the path once its sha256 matches.
func download(ctx context.Context, client *http.Client, server string, offer Offer, dir string) (string, error) {
	url := server + offer.URL
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	res, err := FetchFollowing(ctx, client, url)
	if err != nil {
		return "", err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		_ = res.Body.Close()
		return "", fmt.Errorf("%s: HTTP %d", url, res.StatusCode)
	}
	body, err := ReadCapped(res, MaxTarballBytes)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	if actual := hex.EncodeToString(sum[:]); actual != offer.SHA256 {
		return "", fmt.Errorf("sha256 is %s, the server said %s", actual, offer.SHA256)
	}
	file := filepath.Join(dir, "pkg.tgz")
	return file, os.WriteFile(file, body, 0o644)
}

// extract unpacks a tarball in npm's layout (one top-level directory, stripped) into out. A symlink, a hard link, a
// device or a name with ".." is an error: nothing may land outside out.
func extract(tgz []byte, out string) error {
	zr, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeLink {
			return fmt.Errorf("tarball contains a symlink: %s", h.Name)
		}
		if strings.HasPrefix(h.Name, "/") {
			return fmt.Errorf("tarball contains an absolute path: %s", h.Name)
		}
		parts := strings.Split(strings.TrimSuffix(h.Name, "/"), "/")
		for _, p := range parts {
			if p == ".." {
				return fmt.Errorf("tarball contains path traversal: %s", h.Name)
			}
		}
		if len(parts) < 2 { // the top-level directory itself, or a loose file beside it: --strip-components=1 drops them
			continue
		}
		dest := filepath.Join(out, filepath.Join(parts[1:]...))
		switch h.Typeflag {
		case tar.TypeDir:
			err = os.MkdirAll(dest, 0o755)
		case tar.TypeReg:
			err = writeFile(dest, tr, os.FileMode(h.Mode).Perm())
		default:
			err = fmt.Errorf("tarball contains an entry that is not a file or a directory: %s", h.Name)
		}
		if err != nil {
			return err
		}
	}
}

func writeFile(dest string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// unpack unpacks a verified tarball into dir/pkg and checks what came out: a package.json that names `version` and the
// daemon for `platform`. The tarball carries the daemon of every platform; the one for `platform` becomes DefaultEntry
// (mode 0755) and the others are dropped.
func unpack(tgz, dir, version, platform string) (string, error) {
	body, err := os.ReadFile(tgz)
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "pkg")
	if err := os.Mkdir(out, 0o755); err != nil {
		return "", err
	}
	if err := extract(body, out); err != nil {
		return "", err
	}
	if err := pickBinary(out, platform); err != nil {
		return "", err
	}
	text, err := os.ReadFile(filepath.Join(out, "package.json"))
	if err != nil {
		return "", err
	}
	var pkg struct {
		Version any `json:"version"`
	}
	if err := json.Unmarshal(text, &pkg); err != nil {
		return "", err
	}
	if pkg.Version != version {
		claimed := "undefined"
		if pkg.Version != nil {
			claimed = fmt.Sprint(pkg.Version)
		}
		return "", fmt.Errorf("the tarball holds version %s, not %s", claimed, version)
	}
	return out, nil
}

// pickBinary makes the daemon for `platform` the DefaultEntry of the unpacked package and removes every other
// platform's binary.
func pickBinary(pkg, platform string) error {
	picked := filepath.Join(pkg, BinaryName(platform))
	if info, err := os.Stat(picked); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("the tarball has no daemon for %s (%s)", platform, BinaryName(platform))
	}
	others, err := filepath.Glob(filepath.Join(pkg, binaryPrefix+"*"))
	if err != nil {
		return err
	}
	for _, other := range others {
		if other != picked {
			if err := os.Remove(other); err != nil {
				return err
			}
		}
	}
	entry := filepath.Join(pkg, DefaultEntry)
	if err := os.Rename(picked, entry); err != nil {
		return err
	}
	return os.Chmod(entry, 0o755)
}

// prune removes every version directory except those in keep.
func prune(l home.Layout, keep map[string]bool) error {
	entries, err := os.ReadDir(l.DaemonDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() && IsVersion(e.Name()) && !keep[e.Name()] {
			if err := os.RemoveAll(filepath.Join(l.DaemonDir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// UpdaterOptions configures NewUpdater.
type UpdaterOptions struct {
	Layout home.Layout
	// Server is the releases page the tarball is fetched from (see ReleaseBase).
	Server string
	// Running is the version this process runs.
	Running string
	// ReleaseKey is the release public key (base64 SPKI DER, home.ReleaseKey) every update must be signed with; empty refuses every update.
	ReleaseKey string
	// Idle reports whether no agent session is running.
	Idle func() bool
	// Restart is called when the update is in place and the daemon is idle: exit so the service starts the new version.
	Restart func(version string)
	// Client makes the requests; nil means http.DefaultClient.
	Client *http.Client
	// IdleCheck is how often a staged update looks again for an idle daemon; zero means the package's IdleCheck.
	IdleCheck time.Duration
	// Retry shapes the wait after a failed update; nil means Retry.
	Retry *home.BackoffOptions
	// Platform is the "<GOOS>-<GOARCH>" whose binary is taken from the tarball; empty means this process's own.
	Platform string
	// Log is told what the updater does and refuses; nil says nothing.
	Log Log
}

// Updater keeps the daemon on the version the server serves. Offer takes every hello reply: a different version
// signed by the release key is downloaded, its sha256 checked, unpacked beside the running one and made `current`;
// then, once no session runs, Restart is called. A failure (a bad signature too) changes nothing and is retried with
// backoff.
type Updater struct {
	o      UpdaterOptions
	log    Log
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // the goroutine that works through offers

	mu      sync.Mutex
	staged  string
	busy    bool
	queued  *Offer
	attempt int
	timer   *time.Timer // the retry of a failed update, or the next look for an idle daemon
	stopped bool
	told    map[string]bool
}

// NewUpdater makes an updater; it does nothing until Offer.
func NewUpdater(o UpdaterOptions) *Updater {
	if o.Platform == "" {
		o.Platform = runtime.GOOS + "-" + runtime.GOARCH
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Updater{o: o, log: orDiscard(o.Log), ctx: ctx, cancel: cancel, told: map[string]bool{}}
}

// Offer takes a reply to hello. It returns at once; the work runs on a goroutine, one offer at a time, and an offer
// that arrives meanwhile replaces any other waiting.
func (u *Updater) Offer(reply Offer) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.stopped {
		return
	}
	if u.busy {
		u.queued = &reply
		return
	}
	u.busy = true
	u.wg.Add(1)
	go u.drain(reply)
}

// Stop ends the updater: no offer is taken, no timer fires, and the work in flight is cancelled. It does not wait.
func (u *Updater) Stop() {
	u.mu.Lock()
	u.stopped = true
	u.clearTimerLocked()
	u.mu.Unlock()
	u.cancel()
}

func (u *Updater) drain(reply Offer) {
	defer u.wg.Done()
	for {
		u.consider(reply)
		u.mu.Lock()
		next := u.queued
		u.queued = nil
		if next == nil || u.stopped {
			u.busy = false
			u.mu.Unlock()
			return
		}
		u.mu.Unlock()
		reply = *next
	}
}

func (u *Updater) clearTimerLocked() {
	if u.timer != nil {
		u.timer.Stop()
		u.timer = nil
	}
}

func (u *Updater) clearTimer() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clearTimerLocked()
}

func (u *Updater) consider(reply Offer) {
	rejected, _ := readRecord(u.o.Layout.RejectedFile)
	u.mu.Lock()
	staged := u.staged
	u.mu.Unlock()
	d := Decide(State{Running: u.o.Running, Staged: staged, Rejected: rejected}, reply)
	switch d.Kind {
	case None:
		u.forget()
	case Waiting:
	case Unstage:
		u.unstage(staged)
	case Rejected:
		u.mu.Lock()
		told := u.told[d.Version]
		u.told[d.Version] = true
		u.mu.Unlock()
		if !told {
			u.log.Warn(fmt.Sprintf("not updating to %s: it crashed at start twice here; waiting for another version", d.Version))
		}
	case Invalid:
		u.log.Warn(fmt.Sprintf("not updating: the server's offer is wrong (%s)", d.Why))
	case Install:
		u.update(d.Offer)
	}
}

// forget: the server has nothing newer, so a retry that is waiting has nothing left to do.
func (u *Updater) forget() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.clearTimerLocked()
	u.attempt = 0
}

func (u *Updater) unstage(staged string) {
	u.clearTimer()
	if err := pointCurrent(u.o.Layout, u.o.Running); err != nil {
		u.log.Warn(fmt.Sprintf("cannot point current back at %s: %v", u.o.Running, err))
		return
	}
	if staged == "" {
		staged = "?"
	}
	u.log.Say(fmt.Sprintf("the server serves %s again: update to %s cancelled", u.o.Running, staged))
	u.mu.Lock()
	u.staged = ""
	u.mu.Unlock()
}

func (u *Updater) update(offer Offer) {
	u.clearTimer()
	// A signature that does not verify will not verify later: say so, like an invalid offer, and do not retry.
	if why := SignatureProblem(u.o.ReleaseKey, Signed{Version: offer.Version, SHA256: offer.SHA256, Signature: offer.Signature}); why != "" {
		u.log.Warn(fmt.Sprintf("not updating to %s: %s", offer.Version, why))
		return
	}
	if err := u.stage(offer); err != nil {
		u.mu.Lock()
		if u.stopped {
			u.mu.Unlock()
			return
		}
		retry := Retry
		if u.o.Retry != nil {
			retry = *u.o.Retry
		}
		delay := home.BackoffDelay(u.attempt, retry, nil)
		u.attempt++
		u.timer = time.AfterFunc(delay, func() { u.Offer(offer) })
		u.mu.Unlock()
		u.log.Warn(fmt.Sprintf("cannot update to %s: %v; staying on %s, retrying in %d s", offer.Version, err, u.o.Running, seconds(delay)))
		return
	}
	u.mu.Lock()
	u.attempt = 0
	u.staged = offer.Version
	u.mu.Unlock()
	u.log.Say(fmt.Sprintf("updated to %s; restarting when no session is running", offer.Version))
	u.waitIdle(offer.Version)
}

// stage downloads, checks the sha256, unpacks and points `current`. Nothing is left half done: a temporary directory
// holds the work. update has checked the offer's signature, which covers the version and the sha256, and the download
// must hash to that sha256, so only a tarball the release key signed is unpacked.
func (u *Updater) stage(offer Offer) error {
	l := u.o.Layout
	if err := os.MkdirAll(l.DaemonDir, 0o755); err != nil {
		return err
	}
	dest := home.VersionDir(l, offer.Version)
	if _, err := os.Stat(filepath.Join(dest, DefaultEntry)); err != nil {
		if err := u.install(offer, dest); err != nil {
			return err
		}
	}
	if err := os.WriteFile(l.PreviousFile, []byte(u.o.Running+"\n"), 0o644); err != nil {
		return err
	}
	if err := os.Remove(l.RejectedFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := pointCurrent(l, offer.Version); err != nil {
		return err
	}
	return prune(l, map[string]bool{offer.Version: true, u.o.Running: true})
}

// install puts the offer's package at dest.
func (u *Updater) install(offer Offer, dest string) error {
	work, err := os.MkdirTemp(u.o.Layout.DaemonDir, ".update-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	tgz, err := download(u.ctx, u.o.Client, u.o.Server, offer, work)
	if err != nil {
		return err
	}
	pkg, err := unpack(tgz, work, offer.Version, u.o.Platform)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return err
	}
	return os.Rename(pkg, dest)
}

func (u *Updater) waitIdle(version string) {
	u.mu.Lock()
	if u.stopped || u.staged != version {
		u.mu.Unlock()
		return
	}
	u.mu.Unlock()
	if u.o.Idle() {
		u.o.Restart(version)
		return
	}
	every := u.o.IdleCheck
	if every == 0 {
		every = IdleCheck
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.stopped {
		return
	}
	u.timer = time.AfterFunc(every, func() { u.waitIdle(version) })
}
