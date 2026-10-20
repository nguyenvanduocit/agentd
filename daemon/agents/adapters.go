// Package agents is which agents the daemon can run and how it runs them: the table of agents and their readiness,
// the npm adapters of claude and codex (the pure install rules, and the store that installs them in the background),
// and the processes of agent sessions (the fleet, and one session's stdout/stderr/exit as frames).
//
// agents.go and adapters.go are pure: the environment and the lookups are passed in.
package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// AdapterName is an agent that runs an npm adapter the daemon installs.
type AdapterName string

const (
	Claude AdapterName = "claude"
	Codex  AdapterName = "codex"
)

// Adapter is one npm adapter.
type Adapter struct {
	Name  AdapterName
	Title string
	// Pkg is the npm package, installed once per version under ~/.agentd/adapters.
	Pkg string
	// Version is the pinned version: the ones in adapters/package.json; change both together.
	Version string
}

// Adapters is the adapter of each agent that runs one.
var Adapters = map[AdapterName]Adapter{
	Claude: {Name: Claude, Title: "Claude", Pkg: "@agentclientprotocol/claude-agent-acp", Version: "0.86.0"},
	Codex:  {Name: Codex, Title: "Codex", Pkg: "@agentclientprotocol/codex-acp", Version: "2.1.1"},
}

// AdapterOrder is the order the daemon installs and lists adapters in.
var AdapterOrder = []AdapterName{Claude, Codex}

// AdapterRetry: a failed install is retried from 60 s, doubling, up to 30 min.
var AdapterRetry = home.BackoffOptions{Base: 60 * time.Second, Max: 30 * time.Minute, Jitter: 0.2}

// Marker is the file inside an install directory: written last, before the directory gets its final name.
const Marker = ".agentd-adapter.json"

// SpecVariable is the TEST SEAM AGENTD_ADAPTER_SPEC_<NAME> (for example `file:/path/fake-adapter.tgz`): it replaces
// the package spec that `npm install` gets. The server cannot set it.
func SpecVariable(name AdapterName) string {
	return "AGENTD_ADAPTER_SPEC_" + strings.ToUpper(string(name))
}

// AdapterSpec is what `npm install` gets: the seam's spec, or the package at its pinned version.
func AdapterSpec(a Adapter, env home.Env) string {
	if own := strings.TrimSpace(env[SpecVariable(a.Name)]); own != "" {
		return own
	}
	return a.Pkg + "@" + a.Version
}

// AdapterKey is the directory name of an install: the pinned version, or a hash of the seam's spec.
func AdapterKey(a Adapter, env home.Env) string {
	spec := AdapterSpec(a, env)
	if spec == a.Pkg+"@"+a.Version {
		return a.Version
	}
	sum := sha256.Sum256([]byte(spec))
	return "spec-" + hex.EncodeToString(sum[:])[:12]
}

// AdapterAgentDir is `adapters/<agent>`: every version of one adapter.
func AdapterAgentDir(l home.Layout, name AdapterName) string {
	return path.Join(l.AdaptersDir, string(name))
}

// AdapterDir is `adapters/<agent>/<version>`: it exists only complete (renamed from a partial directory, marker inside).
func AdapterDir(l home.Layout, name AdapterName, key string) string {
	return path.Join(AdapterAgentDir(l, name), key)
}

// PartialDir is where one install in progress writes.
func PartialDir(l home.Layout, name AdapterName, key string, pid int) string {
	return path.Join(AdapterAgentDir(l, name), fmt.Sprintf(".partial-%s-%d", key, pid))
}

// IsPartial reports whether a directory entry of an adapter's directory is an unfinished install.
func IsPartial(entry string) bool {
	return strings.HasPrefix(entry, ".partial")
}

// AdapterKind is where an adapter's install stands.
type AdapterKind string

const (
	AdapterInstalling AdapterKind = "installing"
	AdapterReady      AdapterKind = "ready"
	AdapterFailed     AdapterKind = "failed"
)

// AdapterState is an adapter's install: Bin is set when ready, Reason when failed.
type AdapterState struct {
	Kind   AdapterKind
	Bin    string
	Reason string
}

// AdaptersDisabled: AGENTD_ADAPTERS=off installs no adapter (tests, e2e, debugging); an agent without a seam says so.
func AdaptersDisabled(env home.Env) bool {
	return env["AGENTD_ADAPTERS"] == "off"
}

// The reasons below are shown to the person at the keyboard (the window, and the app's own page), so they are sentences
// with the agent's title, and none of them says "adapter" or "npm error".

// DisabledReason is the reason of an agent whose adapter install is off.
const DisabledReason = "Setting up agents is turned off on this computer."

// agentTitle is what a person calls the agent this adapter runs: Claude Code, where the adapter is "Claude".
func agentTitle(a Adapter) string {
	if title, ok := titles[AgentName(a.Name)]; ok {
		return title
	}
	return a.Title
}

// InstallingReason is the reason of an agent whose adapter is being installed.
func InstallingReason(a Adapter) string {
	return agentTitle(a) + " is setting up (first run only, a few minutes)."
}

// whyLength is how much of npm's explanation the reason keeps.
const whyLength = 160

// FailedReason is the reason of an agent whose adapter could not be installed: the one line of the error that says
// why, and when the daemon tries again.
func FailedReason(a Adapter, detail string, retry time.Duration) string {
	wait := unitsOf(retry)
	why := whyLine(detail)
	if why == "" {
		return fmt.Sprintf("%s could not be set up. It will try again in %s.", agentTitle(a), wait)
	}
	if !strings.HasSuffix(why, ".") && !strings.HasSuffix(why, "…") {
		why += "."
	}
	return fmt.Sprintf("%s could not be set up: %s It will try again in %s.", agentTitle(a), why, wait)
}

// whyLine is the last line of the error that explains it, from the ` | `-joined tail NpmTail makes: without npm's
// own prefix, and without the pointer to npm's log, which says nothing about the cause. "" when there is none.
func whyLine(detail string) string {
	why := ""
	for _, line := range strings.Split(detail, " | ") {
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "npm error "), "npm ERR! "))
		if line != "" && !strings.Contains(line, "A complete log of this run") {
			why = line
		}
	}
	if r := []rune(why); len(r) > whyLength {
		why = string(r[:whyLength]) + "…"
	}
	return why
}

// unitsOf says a wait the way a person would: seconds below a minute, then whole minutes.
func unitsOf(d time.Duration) string {
	seconds := int(math.Round(d.Seconds()))
	n, unit := seconds, "second"
	if seconds >= 60 {
		n, unit = int(math.Round(float64(seconds)/60)), "minute"
	}
	if n == 1 {
		return fmt.Sprintf("%d %s", n, unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// NpmTail is the last lines of npm's stderr: what the user needs to see, not the whole log.
func NpmTail(stderr string) string {
	var lines []string
	for _, l := range strings.Split(stderr, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	tail := []rune(strings.Join(lines, " | "))
	if len(tail) > 400 {
		tail = tail[len(tail)-400:]
	}
	return string(tail)
}

// NpmArgs are the arguments of the install: one package into its own prefix, quietly, without npm's audit and fund
// calls.
func NpmArgs(prefix, spec string) []string {
	return []string{"install", "--prefix", prefix, "--no-audit", "--no-fund", "--loglevel", "error", spec}
}

func unscoped(pkgName string) string {
	return pkgName[strings.LastIndex(pkgName, "/")+1:]
}

// escapesPackage: a path that is absolute or leaves its directory.
func escapesPackage(rel string) bool {
	return strings.HasPrefix(rel, "/") || strings.HasPrefix(path.Clean(rel), "..")
}

// BinEntry is the adapter's executable, relative to its package, from the `bin` of the installed package.json (its
// text): a string, or the entry named like the package, or the only entry. Anything else is an error: the path is
// never guessed.
func BinEntry(pkgName string, packageJSON []byte) (string, error) {
	var doc struct {
		Bin any `json:"bin"`
	}
	if err := json.Unmarshal(packageJSON, &doc); err != nil {
		return "", fmt.Errorf("%s has an unreadable package.json: %w", pkgName, err)
	}
	var rel any
	switch bin := doc.Bin.(type) {
	case string:
		rel = bin
	case map[string]any:
		if named, ok := bin[unscoped(pkgName)]; ok {
			rel = named
		} else if len(bin) == 1 {
			for _, only := range bin {
				rel = only
			}
		}
	}
	text, ok := rel.(string)
	if !ok || text == "" {
		return "", fmt.Errorf("%s declares no usable bin in its package.json", pkgName)
	}
	if escapesPackage(text) {
		return "", fmt.Errorf("%s has a bin outside its package: %s", pkgName, text)
	}
	return path.Clean(text), nil
}

// PackageDir is where the package's files are, inside an install directory.
func PackageDir(installDir, pkgName string) string {
	return path.Join(installDir, "node_modules", pkgName)
}

// MarkerText is the content of the marker: the spec and the executable relative to the install directory.
func MarkerText(spec, binRelativeToInstall string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(struct {
		Spec string `json:"spec"`
		Bin  string `json:"bin"`
	}{spec, binRelativeToInstall}) // a struct of strings always encodes
	return b.String()
}

// ParseMarker is the executable of a complete install (relative to the install directory), or false when the marker
// is not valid.
func ParseMarker(text string) (string, bool) {
	var doc struct {
		Bin any `json:"bin"`
	}
	if json.Unmarshal([]byte(text), &doc) != nil {
		return "", false
	}
	bin, ok := doc.Bin.(string)
	if !ok || bin == "" || escapesPackage(bin) {
		return "", false
	}
	return bin, true
}

// Entry is one entry of an adapter's directory.
type Entry struct {
	Name    string
	ModTime time.Time
}

// OldInstalls are the old install directories to remove: everything except `keep` and the newest other one (the
// previous version).
func OldInstalls(entries []Entry, keep string) []string {
	var others []Entry
	for _, e := range entries {
		if e.Name != keep && !IsPartial(e.Name) {
			others = append(others, e)
		}
	}
	sort.SliceStable(others, func(i, j int) bool { return others[i].ModTime.After(others[j].ModTime) })
	var old []string
	for _, e := range others[min(1, len(others)):] {
		old = append(old, e.Name)
	}
	return old
}
