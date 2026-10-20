package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// rpcInvalidParams is the JSON-RPC code of a request whose parameters are refused.
const rpcInvalidParams = -32602

// folderKeys are the fields that name a folder an agent works in or reads: the ones of ACP, and those the Claude
// adapter also takes under `_meta`. A key counts whatever its case: the guard cannot know which spelling the agent's
// own reader takes, so it checks them all.
var folderKeys = []string{"cwd", "additionalDirectories", "additionalRoots"}

// GuardLine is the check of the folders a user allowed an app: a request that names a folder outside `folders`, in any
// of the `folderKeys` at any depth of its params, is not for the agent. refused is true then, and reply is the JSON-RPC
// error for the client (empty when the request has no id, or the line is not a request, which have nobody to answer).
// No folders is no limit; failed, when the folders could not be read, refuses every request that names one.
//
// A line the guard cannot read as a JSON object with object params is refused too: the agent parses lines with its
// own decoder, and a line the two read differently must not reach it. Keys are read exactly as written, as that
// decoder does, and the last of two equal keys wins, as there. That decoder cuts its input at every newline and reads
// each piece as a line of its own, so a line with a newline inside it is refused: this guard reads it as one value.
// Listing sessions without a folder is refused, because the adapters then list the sessions of every project.
//
// It keeps an app that asks for the wrong folder out of it. It is not a sandbox: an agent's shell can leave the folder.
func GuardLine(folders []string, line string, failed error) (reply string, refused bool) {
	if failed == nil && len(folders) == 0 {
		return "", false
	}
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return "", false
	}
	if strings.ContainsAny(line, "\r\n") {
		return "", true
	}
	var msg map[string]json.RawMessage
	if json.Unmarshal([]byte(line), &msg) != nil {
		return "", true
	}
	var params any
	if raw := msg["params"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &params)
	}
	if _, isObject := params.(map[string]any); params != nil && !isObject {
		return refuse(msg["id"], "the parameters of this request are not an object, so the folders in them cannot be checked")
	}
	paths, unreadable := foldersIn(params)
	if unreadable {
		return refuse(msg["id"], "a folder of this request is not a path, so it cannot be checked against the folders you allowed this app")
	}
	var method string
	_ = json.Unmarshal(msg["method"], &method)
	if method == "session/list" && len(paths) == 0 {
		return refuse(msg["id"], "listing sessions needs a cwd, so that it lists only the sessions of the folders you allowed this app")
	}
	sort.Strings(paths)
	for _, p := range paths {
		switch {
		case failed != nil:
			return refuse(msg["id"], "cannot check "+p+" against the folders you allowed this app: "+failed.Error())
		case !Within(folders, p):
			return refuse(msg["id"], fmt.Sprintf("%s is outside the folders you allowed this app: %s", p, strings.Join(folders, ", ")))
		}
	}
	return "", false
}

// refuse is the answer to a refused request: an invalid-params error for its id, nothing for a line with none.
func refuse(id json.RawMessage, text string) (reply string, refused bool) {
	if len(id) == 0 {
		return "", true
	}
	out, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": rpcInvalidParams, "message": text},
	})
	return string(out), true
}

// foldersIn is every folder a decoded value names under a folder key, at any depth. unreadable is true when a folder
// key holds something that is not a path or a list of paths (null counts as absent, as it does for the agent).
func foldersIn(v any) (paths []string, unreadable bool) {
	switch v := v.(type) {
	case map[string]any:
		for key, child := range v {
			if !isFolderKey(key) {
				more, bad := foldersIn(child)
				paths, unreadable = append(paths, more...), unreadable || bad
				continue
			}
			more, bad := pathsOf(child)
			paths, unreadable = append(paths, more...), unreadable || bad
		}
	case []any:
		for _, child := range v {
			more, bad := foldersIn(child)
			paths, unreadable = append(paths, more...), unreadable || bad
		}
	}
	return paths, unreadable
}

// pathsOf is the paths held by the value of a folder key: a text, or a list of them.
func pathsOf(v any) (paths []string, unreadable bool) {
	switch v := v.(type) {
	case nil:
		return nil, false
	case string:
		return []string{v}, false
	case []any:
		for _, child := range v {
			more, bad := pathsOf(child)
			paths, unreadable = append(paths, more...), unreadable || bad
		}
		return paths, unreadable
	}
	return nil, true
}

func isFolderKey(key string) bool {
	for _, name := range folderKeys {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

// Within is whether the absolute path p is one of the folders or below one. Symlinks are resolved on both sides, so a
// link inside a folder that points out of it does not count. A path with a `..` in it is never within: the text does
// not say where it leads once a link comes before it.
func Within(folders []string, p string) bool {
	if !filepath.IsAbs(p) || slices.Contains(strings.Split(filepath.ToSlash(p), "/"), "..") {
		return false
	}
	p = resolve(p)
	for _, folder := range folders {
		rel, err := filepath.Rel(resolve(folder), p)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// resolve is p with the symlinks of its longest existing part resolved; a part that does not exist yet stays as written.
func resolve(p string) string {
	p = filepath.Clean(p)
	for rest := ""; ; {
		if target, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(target, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// firstDir is the first of the folders that exists as a directory.
func firstDir(folders []string) (string, error) {
	for _, folder := range folders {
		if info, err := os.Stat(folder); err == nil && info.IsDir() {
			return folder, nil
		}
	}
	return "", fmt.Errorf("none of the folders you allowed this app exists: %s", strings.Join(folders, ", "))
}
