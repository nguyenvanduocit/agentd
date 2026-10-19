package main

import "testing"

func TestPathFromShellReadsBetweenTheMarksWhateverTheShellPrints(t *testing.T) {
	out := "Welcome to zsh\n" + pathMark + "/opt/homebrew/bin:/usr/bin:/bin" + pathMark + "\nbye\n"
	if got, ok := PathFromShell(out); !ok || got != "/opt/homebrew/bin:/usr/bin:/bin" {
		t.Fatalf("got %q, %v", got, ok)
	}
	for _, bad := range []string{"", "no marks here", pathMark + pathMark, pathMark + "/bin"} {
		if got, ok := PathFromShell(bad); ok {
			t.Errorf("%q: got %q, want none", bad, got)
		}
	}
}

func TestEnginePathPutsTheBundledNodeFirst(t *testing.T) {
	if got := EnginePath("/App/node/bin", "/opt/homebrew/bin:/usr/bin"); got != "/App/node/bin:/opt/homebrew/bin:/usr/bin" {
		t.Fatalf("got %q", got)
	}
}
