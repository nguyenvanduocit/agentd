package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The agent is a Node program that reads each line with JSON.parse, so the guard must see the same fields it does.
// Each line below puts a place outside the folders where the agent finds it and a different place, or none, where
// Go's decoder looks.
func TestGuardLineRefusesWhatTheAgentWouldReadDifferently(t *testing.T) {
	root := t.TempDir()
	out := t.TempDir()
	if err := os.MkdirAll(filepath.Join(out, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// root/escape leads to out/sub, so root/escape/.. is out, though cleaning the text says root.
	if err := os.Symlink(filepath.Join(out, "sub"), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	folders := []string{root}

	for name, line := range map[string]string{
		"a key that differs in case":              `{"id":1,"method":"session/new","params":{"cwd":"` + out + `","CWD":"` + root + `"}}`,
		"a method that differs in case":           `{"id":1,"method":"session/new","Method":"x","params":{"cwd":"` + out + `"}}`,
		"a folder list that is not a list":        `{"id":1,"method":"session/new","params":{"cwd":"` + root + `","additionalDirectories":"` + out + `"}}`,
		"a cwd that is not text":                  `{"id":1,"method":"session/new","params":{"cwd":5}}`,
		"a folder under _meta (Claude)":           `{"id":1,"method":"session/new","params":{"cwd":"` + root + `","_meta":{"claudeCode":{"options":{"additionalDirectories":["` + out + `"]}}}}}`,
		"a root under _meta":                      `{"id":1,"method":"session/new","params":{"cwd":"` + root + `","_meta":{"additionalRoots":["` + out + `"]}}}`,
		"a method that is not under session/":     `{"id":1,"method":"_vendor/start","params":{"cwd":"` + out + `"}}`,
		"dot-dot after a link that leaves":        `{"id":1,"method":"session/new","params":{"cwd":"` + filepath.Join(root, "escape") + `/.."}}`,
		"dot-dot that stays inside, written out":  `{"id":1,"method":"session/new","params":{"cwd":"` + root + `/a/../b"}}`,
		"a line that is not JSON":                 `not json`,
		"a line that is not an object":            `[{"id":1,"method":"session/new","params":{"cwd":"` + out + `"}}]`,
		"a request with no params object at all":  `{"id":1,"method":"session/new","params":"` + out + `"}`,
		"a place nested in the arguments of tool": `{"id":1,"method":"session/new","params":{"cwd":"` + root + `","mcpServers":[{"name":"x","command":"y","cwd":"` + out + `"}]}}`,
	} {
		if _, refused := GuardLine(folders, line, nil); !refused {
			t.Errorf("%s passed: %s", name, line)
		}
	}
}

// The agent cuts its input at every newline and reads each piece as a line of its own; Go reads the whole string as one
// value. A newline inside the string must never carry a request the guard did not see.
func TestGuardLineRefusesALineThatTheAgentWouldCutInTwo(t *testing.T) {
	root := t.TempDir()
	out := t.TempDir()
	for name, line := range map[string]string{
		"a newline inside an object": `{"k":` + "\n" + `{"id":2,"method":"session/new","params":{"cwd":"` + out + `"}}` + "\n" + `}`,
		"a carriage return inside":   `{"k":` + "\r" + `{"id":2,"method":"session/new","params":{"cwd":"` + out + `"}}}`,
		"two requests on one line":   `{"id":1,"method":"session/cancel","params":{}}` + "\n" + `{"id":2,"method":"session/new","params":{"cwd":"` + out + `"}}`,
	} {
		if _, refused := GuardLine([]string{root}, line, nil); !refused {
			t.Errorf("%s passed: %q", name, line)
		}
	}
	valid := `{"id":1,"method":"session/new","params":{"cwd":"` + root + `"}}`
	for _, ending := range []string{"", "\n", "\r\n"} {
		if reply, refused := GuardLine([]string{root}, valid+ending, nil); refused || reply != "" {
			t.Errorf("a request inside the folders with the ending %q: refused = %v, reply = %q", ending, refused, reply)
		}
	}
}

// Listing sessions names no folder unless it is asked to, and the adapters then list the sessions of every project.
func TestGuardLineRefusesListingTheSessionsOfEveryProject(t *testing.T) {
	root := t.TempDir()
	out := t.TempDir()
	folders := []string{root}
	for _, line := range []string{
		`{"id":1,"method":"session/list","params":{}}`,
		`{"id":1,"method":"session/list"}`,
		`{"id":1,"method":"session/list","params":{"cwd":null}}`,
		`{"id":1,"method":"session/list","params":{"cwd":"` + out + `"}}`,
	} {
		if _, refused := GuardLine(folders, line, nil); !refused {
			t.Errorf("%s passed", line)
		}
	}
	if reply, refused := GuardLine(folders, `{"id":1,"method":"session/list","params":{"cwd":"`+root+`"}}`, nil); refused || reply != "" {
		t.Errorf("listing the sessions of an allowed folder: refused = %v, reply = %q", refused, reply)
	}
}

// A refusal that has an id must answer it: the client waits for the reply.
func TestGuardLineAnswersTheRequestItRefuses(t *testing.T) {
	root := t.TempDir()
	out := t.TempDir()
	for _, line := range []string{
		`{"id":7,"method":"session/new","params":{"cwd":"` + out + `"}}`,
		`{"id":7,"method":"session/new","params":{"cwd":5}}`,
		`{"id":7,"method":"session/new","params":"` + out + `"}`,
		`{"id":7,"method":"session/list","params":{}}`,
	} {
		reply, refused := GuardLine([]string{root}, line, nil)
		var got struct {
			ID    int `json:"id"`
			Error struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if !refused || json.Unmarshal([]byte(reply), &got) != nil || got.ID != 7 || got.Error.Code != rpcInvalidParams || got.Error.Message == "" {
			t.Errorf("%s: refused = %v, reply = %q; want an invalid-params error for id 7 that says why", line, refused, reply)
		}
	}
}

func TestGuardLineLetsAnEmptyLinePass(t *testing.T) {
	for _, line := range []string{"", "  ", "\t"} {
		if _, refused := GuardLine([]string{t.TempDir()}, line, nil); refused {
			t.Errorf("%q was refused", line)
		}
	}
}
