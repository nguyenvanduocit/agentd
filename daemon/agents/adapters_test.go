package agents

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

var layout = home.LayoutOf("/h/.agentd")

func TestThePinnedAdapters(t *testing.T) {
	t.Run("claude-agent-acp 0.86.0 and codex-acp 2.1.1, installed by their exact version", func(t *testing.T) {
		if got := AdapterSpec(Adapters[Claude], nil); got != "@agentclientprotocol/claude-agent-acp@0.86.0" {
			t.Fatal(got)
		}
		if got := AdapterSpec(Adapters[Codex], nil); got != "@agentclientprotocol/codex-acp@2.1.1" {
			t.Fatal(got)
		}
		if got := AdapterKey(Adapters[Claude], nil); got != "0.86.0" {
			t.Fatal(got)
		}
	})

	t.Run("the seam replaces the spec, and gets a directory of its own", func(t *testing.T) {
		if got := SpecVariable("codex"); got != "AGENTD_ADAPTER_SPEC_CODEX" {
			t.Fatal(got)
		}
		codex := Adapters[Codex]
		env := home.Env{"AGENTD_ADAPTER_SPEC_CODEX": "file:/x/fake.tgz"}
		if got := AdapterSpec(codex, env); got != "file:/x/fake.tgz" {
			t.Fatal(got)
		}
		key := AdapterKey(codex, env)
		if !regexp.MustCompile(`^spec-[0-9a-f]{12}$`).MatchString(key) {
			t.Fatal(key)
		}
		if key == AdapterKey(codex, home.Env{"AGENTD_ADAPTER_SPEC_CODEX": "file:/y.tgz"}) {
			t.Fatal("two specs share a directory")
		}
		if got := AdapterSpec(codex, home.Env{"AGENTD_ADAPTER_SPEC_CODEX": "  "}); got != "@agentclientprotocol/codex-acp@2.1.1" {
			t.Fatal(got)
		}
	})
}

func TestTheStoresPaths(t *testing.T) {
	t.Run("adapters/<agent>/<version>, a partial directory beside it", func(t *testing.T) {
		for got, want := range map[string]string{
			AdapterAgentDir(layout, "claude"):          "/h/.agentd/adapters/claude",
			AdapterDir(layout, "claude", "0.86.0"):     "/h/.agentd/adapters/claude/0.86.0",
			PartialDir(layout, "claude", "0.86.0", 42): "/h/.agentd/adapters/claude/.partial-0.86.0-42",
			PackageDir("/i", "@a/b"):                   "/i/node_modules/@a/b",
		} {
			if got != want {
				t.Errorf("got %s, want %s", got, want)
			}
		}
		if !IsPartial(".partial-0.86.0-42") || IsPartial("0.86.0") {
			t.Fatal("IsPartial")
		}
	})

	t.Run("npm gets one package into its own prefix, quietly", func(t *testing.T) {
		want := []string{"install", "--prefix", "/p", "--no-audit", "--no-fund", "--loglevel", "error", "@a/b@1"}
		if got := NpmArgs("/p", "@a/b@1"); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v", got)
		}
	})
}

func TestBinEntry(t *testing.T) {
	t.Run("an object keyed like the package, a string, or the only entry", func(t *testing.T) {
		for _, c := range []struct{ pkg, json, want string }{
			{"@agentclientprotocol/codex-acp", `{"bin":{"codex-acp":"dist/index.js"}}`, "dist/index.js"},
			{"pkg", `{"bin":"./cli.js"}`, "cli.js"},
			{"pkg", `{"bin":{"other":"x.js"}}`, "x.js"},
			{"pkg", `{"bin":{"a":"a.js","pkg":"p.js"}}`, "p.js"},
		} {
			if got, err := BinEntry(c.pkg, []byte(c.json)); err != nil || got != c.want {
				t.Errorf("%s: got %q, %v; want %q", c.json, got, err, c.want)
			}
		}
	})

	t.Run("nothing is guessed: no bin, several unrelated bins, or a path that leaves the package", func(t *testing.T) {
		for _, json := range []string{`{}`, `{"bin":{}}`, `{"bin":{"a":"a.js","b":"b.js"}}`, `{"bin":"../../evil.js"}`, `{"bin":"/etc/passwd"}`, `null`, `{"bin":{"pkg":5}}`, `{`} {
			if got, err := BinEntry("pkg", []byte(json)); err == nil {
				t.Errorf("%s: got %q, want an error", json, got)
			}
		}
	})
}

func TestTheCompletionMarker(t *testing.T) {
	t.Run("a marker names the executable relative to the install directory", func(t *testing.T) {
		text := MarkerText("@a/b@1", "node_modules/@a/b/dist/index.js")
		if text != `{"spec":"@a/b@1","bin":"node_modules/@a/b/dist/index.js"}`+"\n" {
			t.Fatal(text)
		}
		if got, ok := ParseMarker(text); !ok || got != "node_modules/@a/b/dist/index.js" {
			t.Fatalf("got %q, %v", got, ok)
		}
	})

	t.Run("a missing, torn or hostile marker is not a complete install", func(t *testing.T) {
		for _, bad := range []string{"", "{", "{}", `{"bin":""}`, `{"bin":"/abs"}`, `{"bin":"../x"}`, `{"bin":5}`, "null"} {
			if got, ok := ParseMarker(bad); ok {
				t.Errorf("%q: got %q", bad, got)
			}
		}
	})
}

func TestOldVersions(t *testing.T) {
	at := func(ms int64) time.Time { return time.UnixMilli(ms) }
	entries := []Entry{
		{"0.1.0", at(1)},
		{"0.2.0", at(2)},
		{"0.3.0", at(3)},
		{"0.4.0", at(4)},
		{".partial-x-1", at(9)},
	}
	for _, c := range []struct {
		keep string
		want []string
	}{
		{"0.4.0", []string{"0.2.0", "0.1.0"}},
		{"0.1.0", []string{"0.3.0", "0.2.0"}},
	} {
		if got := OldInstalls(entries, c.keep); !reflect.DeepEqual(got, c.want) {
			t.Errorf("keep %s: got %v, want %v", c.keep, got, c.want)
		}
	}
	if got := OldInstalls([]Entry{{"0.4.0", at(4)}}, "0.4.0"); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestAdaptersDisabled(t *testing.T) {
	if !AdaptersDisabled(home.Env{"AGENTD_ADAPTERS": "off"}) || AdaptersDisabled(home.Env{"AGENTD_ADAPTERS": "on"}) || AdaptersDisabled(nil) {
		t.Fatal("AGENTD_ADAPTERS=off disables the install, nothing else does")
	}
}

func TestMessagesAndRetry(t *testing.T) {
	t.Run("the reasons say what is going on in a person's words, with the agent's title", func(t *testing.T) {
		if got := InstallingReason(Adapters[Codex]); got != "Codex is setting up (first run only, a few minutes)." {
			t.Fatal(got)
		}
		if got := InstallingReason(Adapters[Claude]); got != "Claude Code is setting up (first run only, a few minutes)." {
			t.Fatal(got)
		}
		if DisabledReason != "Setting up agents is turned off on this computer." {
			t.Fatal(DisabledReason)
		}
	})

	t.Run("a failure names the one line that says why, not the whole of npm's stderr, and when it tries again", func(t *testing.T) {
		const logPointer = "npm error A complete log of this run can be found in: /Users/ann/.npm/_logs/2026-10-20T00_00_00_000Z-debug-0.log"
		for _, c := range []struct {
			name   string
			agent  Adapter
			detail string
			retry  time.Duration
			want   string
		}{
			{"a plain detail", Adapters[Claude], "ENOTFOUND registry.npmjs.org", 61_400 * time.Millisecond,
				"Claude Code could not be set up: ENOTFOUND registry.npmjs.org. It will try again in 1 minute."},
			{"npm's tail: the line before the pointer to its log, without npm's prefix", Adapters[Codex],
				"npm error code ENOTFOUND | npm error network request to https://registry.npmjs.org/x failed, reason: getaddrinfo ENOTFOUND registry.npmjs.org | " + logPointer, 120 * time.Second,
				"Codex could not be set up: network request to https://registry.npmjs.org/x failed, reason: getaddrinfo ENOTFOUND registry.npmjs.org. It will try again in 2 minutes."},
			{"npm's tail of an old npm", Adapters[Claude], "npm ERR! code ENOENT | npm ERR! enoent could not read x.tgz", 60 * time.Second,
				"Claude Code could not be set up: enoent could not read x.tgz. It will try again in 1 minute."},
			{"a detail that already ends a sentence", Adapters[Claude], "npm not found: install Node.js (it comes with npm).", 30 * time.Second,
				"Claude Code could not be set up: npm not found: install Node.js (it comes with npm). It will try again in 30 seconds."},
			{"no detail", Adapters[Codex], "", time.Second, "Codex could not be set up. It will try again in 1 second."},
			{"only the pointer to npm's log", Adapters[Codex], logPointer, 1800 * time.Second, "Codex could not be set up. It will try again in 30 minutes."},
		} {
			if got := FailedReason(c.agent, c.detail, c.retry); got != c.want {
				t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
			}
		}
		long := FailedReason(Adapters[Codex], strings.Repeat("x", 1000), time.Minute)
		if n := len([]rune(long)); n > 220 || !strings.HasPrefix(long, "Codex could not be set up: xxx") || !strings.HasSuffix(long, "… It will try again in 1 minute.") {
			t.Errorf("a long detail is cut (%d runes): %q", n, long)
		}
	})

	t.Run("NpmTail keeps the last lines and a bounded length", func(t *testing.T) {
		if got := NpmTail("a\n\n  b \nc\nd\ne\nf\n"); got != "c | d | e | f" {
			t.Fatal(got)
		}
		if got := NpmTail(strings.Repeat("x", 1000)); len(got) != 400 {
			t.Fatal(len(got))
		}
		if got := NpmTail(""); got != "" {
			t.Fatal(got)
		}
	})

	t.Run("retries start at 60 s and double up to 30 min", func(t *testing.T) {
		noJitter := AdapterRetry
		noJitter.Jitter = 0
		var got []int
		for _, n := range []int{0, 1, 2, 3, 4, 5, 6, 7, 20} {
			got = append(got, int(home.BackoffDelay(n, noJitter, nil)/time.Second))
		}
		if want := []int{60, 120, 240, 480, 960, 1800, 1800, 1800, 1800}; !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v", got)
		}
	})
}
