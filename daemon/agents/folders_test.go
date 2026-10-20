package agents

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWithin(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "a", "b")
	outside := t.TempDir()
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		path string
		want bool
	}{
		{"the folder itself", root, true},
		{"below it", inside, true},
		{"below it, not yet created", filepath.Join(inside, "new", "deeper"), true},
		{"a sibling with the same prefix", root + "-other", false},
		{"its parent", filepath.Dir(root), false},
		{"dot-dot out of it", filepath.Join(root, "..", "x"), false},
		{"through a link that leaves it", filepath.Join(root, "escape", "x"), false},
		{"a relative path", "a/b", false},
		{"empty", "", false},
	} {
		if got := Within([]string{root}, c.path); got != c.want {
			t.Errorf("%s: Within(%q) = %v, want %v", c.name, c.path, got, c.want)
		}
	}
}

func TestWithinAnyOfSeveralFolders(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if !Within([]string{a, b}, filepath.Join(b, "x")) {
		t.Error("a path below the second folder is allowed")
	}
	if Within([]string{a, b}, t.TempDir()) {
		t.Error("a path below neither folder is refused")
	}
}

func TestGuardLine(t *testing.T) {
	root := t.TempDir()
	out := t.TempDir()
	folders := []string{root}
	pass := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"` + root + `","mcpServers":[]}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"s","prompt":[]}}`,
		`{"jsonrpc":"2.0","id":4,"method":"session/new","params":{"cwd":"` + root + `","additionalDirectories":["` + root + `"]}}`,
		`{"jsonrpc":"2.0","id":5,"method":"session/cancel","params":{"sessionId":"s"}}`,
		`{"jsonrpc":"2.0","id":6,"method":"session/new","params":{"cwd":null}}`,
		`{"jsonrpc":"2.0","id":7,"result":{"outcome":"cancelled"}}`,
	}
	for _, line := range pass {
		if reply, refused := GuardLine(folders, line, nil); refused || reply != "" {
			t.Errorf("%s: refused = %v, reply = %q; want it passed", line, refused, reply)
		}
	}

	line := `{"jsonrpc":"2.0","id":"req-7","method":"session/new","params":{"cwd":"` + out + `","mcpServers":[]}}`
	reply, refused := GuardLine(folders, line, nil)
	if !refused {
		t.Fatalf("a cwd outside the folders passed")
	}
	var got struct {
		ID    string `json:"id"`
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(reply), &got); err != nil {
		t.Fatalf("reply %q is not JSON: %v", reply, err)
	}
	if got.ID != "req-7" || got.Error.Code != rpcInvalidParams || got.Error.Message == "" {
		t.Errorf("reply = %q, want an invalid-params error for id req-7 that says why", reply)
	}

	for _, line := range []string{
		`{"jsonrpc":"2.0","id":9,"method":"session/load","params":{"sessionId":"s","cwd":"` + out + `"}}`,
		`{"jsonrpc":"2.0","id":9,"method":"session/new","params":{"cwd":"` + root + `","additionalDirectories":["` + out + `"]}}`,
		`{"jsonrpc":"2.0","id":9,"method":"session/new","params":{"cwd":"relative/dir"}}`,
	} {
		if _, refused := GuardLine(folders, line, nil); !refused {
			t.Errorf("%s passed", line)
		}
	}

	if reply, refused := GuardLine(folders, `{"jsonrpc":"2.0","method":"session/new","params":{"cwd":"`+out+`"}}`, nil); !refused || reply != "" {
		t.Errorf("a notification outside the folders: refused = %v, reply = %q; want dropped without a reply", refused, reply)
	}
}

func TestGuardLineWithoutFoldersIsNoLimitButUnreadableFoldersRefuse(t *testing.T) {
	line := `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/anywhere"}}`
	if reply, refused := GuardLine(nil, line, nil); refused || reply != "" {
		t.Errorf("no folders: refused = %v, reply = %q; want it passed", refused, reply)
	}
	reply, refused := GuardLine(nil, line, errors.New("daemon.json is gone"))
	if !refused || !strings.Contains(reply, "daemon.json is gone") {
		t.Errorf("unreadable folders: refused = %v, reply = %q; want an error that says why", refused, reply)
	}
	if _, refused := GuardLine(nil, `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{}}`, errors.New("x")); refused {
		t.Error("a request that names no place is not held back by an unreadable list")
	}
}
