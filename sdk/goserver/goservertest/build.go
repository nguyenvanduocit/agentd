package goservertest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// modulePath is the module the daemon is built from: the one the app already depends on for sdk/goserver.
const modulePath = "github.com/nguyenvanduocit/agentd"

// binDirEnv names a directory that holds a built `agentd` and `fake-agent`. When it is set nothing is compiled: for a
// CI that builds them once, an app that vendors its dependencies, or a machine without the Go toolchain.
const binDirEnv = "GOSERVERTEST_BIN"

// buildTimeout bounds the first compile of the daemon, which may also download what the daemon needs and the app does not.
const buildTimeout = 10 * time.Minute

// tools are the two programs a test needs, built once per test binary.
type tools struct{ agentd, fakeAgent string }

var (
	toolsOnce sync.Once
	toolsVal  tools
	toolsErr  error
)

// binaries returns the built daemon and fake agent, building them the first time.
func binaries(t tb) tools {
	t.Helper()
	toolsOnce.Do(func() { toolsVal, toolsErr = prepare() })
	if toolsErr != nil {
		t.Fatalf("goservertest: %v", toolsErr)
	}
	return toolsVal
}

func prepare() (tools, error) {
	if dir := os.Getenv(binDirEnv); dir != "" {
		got := tools{agentd: filepath.Join(dir, "agentd"), fakeAgent: filepath.Join(dir, "fake-agent")}
		for _, f := range []string{got.agentd, got.fakeAgent} {
			if _, err := os.Stat(f); err != nil {
				return tools{}, fmt.Errorf("%s=%s, but %v", binDirEnv, dir, err)
			}
		}
		return got, nil
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		return tools{}, fmt.Errorf("`go` is not on PATH, and goservertest builds agentd with the Go toolchain. Install Go, or build agentd and fake-agent "+
			"(go build ./cmd/agentd ./cmd/fake-agent in the %s module) and set %s to the directory that holds them", modulePath, binDirEnv)
	}
	dir, immutable, err := moduleDir(goBin)
	if err != nil {
		return tools{}, err
	}
	key, err := cacheKey(goBin, dir, immutable)
	if err != nil {
		return tools{}, err
	}
	root := filepath.Join(os.TempDir(), "goservertest-agentd")
	final := filepath.Join(root, key)
	got := tools{agentd: filepath.Join(final, "agentd"), fakeAgent: filepath.Join(final, "fake-agent")}
	if built(got) {
		return got, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return tools{}, err
	}
	stage, err := os.MkdirTemp(root, ".build-")
	if err != nil {
		return tools{}, err
	}
	defer os.RemoveAll(stage)
	if err := compile(goBin, dir, stage); err != nil {
		return tools{}, err
	}
	if err := os.Rename(stage, final); err != nil {
		// Another test binary built the same version at the same moment and won the rename: use its copy.
		if !built(got) {
			return tools{}, fmt.Errorf("keeping the built daemon in %s: %w", final, err)
		}
	}
	prune(root, key)
	return got, nil
}

func built(t tools) bool {
	for _, f := range []string{t.agentd, t.fakeAgent} {
		if info, err := os.Stat(f); err != nil || info.IsDir() {
			return false
		}
	}
	return true
}

// prune keeps one copy: the build of another version, or of an older state of a source tree, is removed. A build that
// another test binary is making right now (a .build- directory younger than an hour) is left alone.
func prune(root, keep string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() == keep {
			continue
		}
		if strings.HasPrefix(e.Name(), ".build-") {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) < time.Hour {
				continue
			}
		}
		_ = os.RemoveAll(filepath.Join(root, e.Name()))
	}
}

// moduleDir is where the source of the daemon is: the directory of the agentd module in the module graph of the
// directory the test runs in (its module cache copy, or the directory a replace points at, or the module itself when
// the test runs inside agentd). immutable is true for a module cache copy, which never changes.
func moduleDir(goBin string) (dir string, immutable bool, err error) {
	out, err := exec.Command(goBin, "list", "-m", "-f", "{{.Dir}}", modulePath).Output()
	if err != nil {
		return "", false, fmt.Errorf("cannot find the %s module from %s: %s\n"+
			"goservertest builds the daemon from the module that your app already depends on for sdk/goserver. "+
			"With vendored or offline dependencies build agentd and fake-agent yourself and set %s to the directory that holds them",
			modulePath, cwd(), explain(err), binDirEnv)
	}
	dir = strings.TrimSpace(string(out))
	if dir == "" {
		return "", false, fmt.Errorf("the %s module has no directory in this build (vendored dependencies?): build agentd and fake-agent yourself and set %s to the directory that holds them", modulePath, binDirEnv)
	}
	cache, err := exec.Command(goBin, "env", "GOMODCACHE").Output()
	if err == nil && strings.HasPrefix(dir, strings.TrimSpace(string(cache))+string(filepath.Separator)) {
		immutable = true
	}
	return dir, immutable, nil
}

func cwd() string {
	d, err := os.Getwd()
	if err != nil {
		return "the current directory"
	}
	return d
}

func explain(err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return strings.TrimSpace(string(exit.Stderr))
	}
	return err.Error()
}

// cacheKey names a build: the same source, toolchain and platform give the same key. A module cache copy is told apart
// by its directory (it carries the version); a source tree by the newest modification time of its Go files, so that an
// edit to agentd while it is being worked on is built again.
func cacheKey(goBin, dir string, immutable bool) (string, error) {
	env, err := exec.Command(goBin, "env", "GOVERSION", "GOOS", "GOARCH").Output()
	if err != nil {
		return "", fmt.Errorf("go env: %s", explain(err))
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", dir, env)
	if !immutable {
		var newest time.Time
		count := 0
		skip := map[string]bool{".git": true, "node_modules": true, "build": true, "dist": true, "resources": true}
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if skip[d.Name()] && path != dir {
					return filepath.SkipDir
				}
				return nil
			}
			if n := d.Name(); strings.HasSuffix(n, ".go") || n == "go.mod" || n == "go.sum" {
				if info, err := d.Info(); err == nil {
					count++
					if info.ModTime().After(newest) {
						newest = info.ModTime()
					}
				}
			}
			return nil
		})
		fmt.Fprintf(h, "%d %d\n", count, newest.UnixNano())
	}
	return hex.EncodeToString(h.Sum(nil))[:16], nil
}

// compile builds the daemon and the fake agent from dir into out. It builds in the module's own directory, with that
// module's go.mod and go.sum (an app's go.sum may lack what only the daemon needs), without cgo and without a
// workspace, so the result does not depend on how the app is built.
func compile(goBin, dir, out string) error {
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	for _, p := range []struct{ pkg, name string }{{"./cmd/agentd", "agentd"}, {"./cmd/fake-agent", "fake-agent"}} {
		cmd := buildCommand(ctx, goBin, dir, "build", "-trimpath", "-o", filepath.Join(out, p.name), p.pkg)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("building %s in %s failed: %v\n%s\n"+
				"Build agentd and fake-agent yourself and set %s to the directory that holds them if this machine cannot", p.pkg, dir, err, strings.TrimSpace(string(output)), binDirEnv)
		}
	}
	return nil
}

// buildCommand is `go args...` in dir with the environment of a build of the daemon: the app's GOFLAGS minus the flags
// that would make the daemon's own module build differently (-mod, -modfile), no workspace, no cgo. Finding where the
// daemon's source is runs in the app's environment as it is (the app's -mod and workspace are how its module graph is
// resolved); only the build of the daemon's own module is made independent of them.
func buildCommand(ctx context.Context, goBin, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, goBin, args...)
	cmd.Dir = dir
	var env []string
	flags := ""
	for _, kv := range os.Environ() {
		switch k, v, _ := strings.Cut(kv, "="); k {
		case "GOWORK", "CGO_ENABLED":
		case "GOFLAGS":
			flags = v
		default:
			env = append(env, kv)
		}
	}
	var keep []string
	for _, f := range strings.Fields(flags) {
		if !strings.HasPrefix(f, "-mod=") && !strings.HasPrefix(f, "-modfile=") {
			keep = append(keep, f)
		}
	}
	cmd.Env = append(env, "GOWORK=off", "CGO_ENABLED=0", "GOFLAGS="+strings.Join(keep, " "))
	return cmd
}
