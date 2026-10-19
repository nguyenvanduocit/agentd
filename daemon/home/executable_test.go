package home

import (
	"reflect"
	"slices"
	"testing"
)

func files(present ...string) func(string) bool {
	return func(f string) bool { return slices.Contains(present, f) }
}

func TestResolveExecutable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cmd      string
		env      Env
		platform string
		present  []string
		want     string
		found    bool
	}{
		{"takes the first directory of PATH that has the file", "npx", Env{"PATH": "/a:/b:/c"}, "linux", []string{"/b/npx", "/c/npx"}, "/b/npx", true},
		{"not found in PATH", "npx", Env{"PATH": "/a"}, "linux", nil, "", false},
		{"not found without PATH", "npx", Env{}, "linux", []string{"/npx"}, "", false},
		{"a name with a directory part is checked as given", "/opt/x/agent", Env{"PATH": "/a"}, "linux", []string{"/opt/x/agent"}, "/opt/x/agent", true},
		{"a relative name with a directory part is not looked up in PATH", "./agent", Env{"PATH": "/a"}, "linux", []string{"/a/agent"}, "", false},
		{"Windows: PATHEXT tried in order", "npx", Env{"Path": `C:\n;C:\m`, "PATHEXT": ".EXE;.CMD"}, "windows", []string{`C:\m\npx.CMD`, `C:\n\npx.CMD`}, `C:\n\npx.CMD`, true},
		{"Windows: a name with an extension is tried as given", "node.exe", Env{"Path": `C:\n;C:\m`, "PATHEXT": ".EXE;.CMD"}, "windows", []string{`C:\m\node.exe`}, `C:\m\node.exe`, true},
		{"Windows: a name with a directory part gets the extensions", `C:\x\a`, Env{"Path": `C:\n`, "PATHEXT": ".EXE;.CMD"}, "windows", []string{`C:\x\a.EXE`}, `C:\x\a.EXE`, true},
		{"Windows: the default PATHEXT", "git", Env{"Path": `C:\g`}, "windows", []string{`C:\g\git.EXE`}, `C:\g\git.EXE`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ResolveExecutable(tc.cmd, tc.env, tc.platform, files(tc.present...))
			if got != tc.want || ok != tc.found {
				t.Errorf("ResolveExecutable = %q, %v; want %q, %v", got, ok, tc.want, tc.found)
			}
		})
	}
}

func TestPathDirsSkipsEmptyEntries(t *testing.T) {
	if got := PathDirs(Env{"PATH": ":/a::"}, "linux"); !reflect.DeepEqual(got, []string{"/a"}) {
		t.Errorf("PathDirs = %q, want [/a]", got)
	}
	if got := PathDirs(Env{}, "linux"); len(got) != 0 {
		t.Errorf("PathDirs of no PATH = %q", got)
	}
}

func TestPathKey(t *testing.T) {
	for _, tc := range []struct {
		env      Env
		platform string
		want     string
	}{
		{Env{"Path": `C:\n`}, "windows", "Path"},
		{Env{"PATH": `C:\n`}, "windows", "PATH"},
		{Env{}, "windows", "Path"},
		{Env{"PATH": "x"}, "linux", "PATH"},
	} {
		if got := PathKey(tc.env, tc.platform); got != tc.want {
			t.Errorf("PathKey(%v, %s) = %q, want %q", tc.env, tc.platform, got, tc.want)
		}
	}
}
