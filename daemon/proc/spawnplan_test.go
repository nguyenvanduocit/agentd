package proc

import (
	"reflect"
	"testing"
)

func TestPlanSpawn(t *testing.T) {
	t.Run("POSIX: no shell, own process group", func(t *testing.T) {
		got := PlanSpawn("/usr/bin/npx", []string{"--yes", "x@1"}, "linux")
		want := SpawnPlan{File: "/usr/bin/npx", Args: []string{"--yes", "x@1"}, Shell: false, Detached: true}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("Windows: an .exe runs directly, not detached", func(t *testing.T) {
		got := PlanSpawn(`C:\n\grok.exe`, []string{"agent"}, "windows")
		want := SpawnPlan{File: `C:\n\grok.exe`, Args: []string{"agent"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("Windows: .cmd and .bat go through the shell with quoted arguments", func(t *testing.T) {
		got := PlanSpawn(`C:\Program Files\nodejs\npx.cmd`, []string{"--yes", "a b", ""}, "windows")
		want := SpawnPlan{File: `"C:\Program Files\nodejs\npx.cmd"`, Args: []string{"--yes", `"a b"`, `""`}, Shell: true}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
		if !PlanSpawn(`C:\x.BAT`, nil, "windows").Shell {
			t.Fatal("C:\\x.BAT does not go through the shell")
		}
	})

	t.Run("a quote inside an argument is doubled", func(t *testing.T) {
		got := PlanSpawn(`x.cmd`, []string{`say "hi"`}, "windows")
		if got.Args[0] != `"say ""hi"""` {
			t.Fatalf("got %q", got.Args[0])
		}
	})

	t.Run("the caller's args are copied", func(t *testing.T) {
		args := []string{"a"}
		plan := PlanSpawn("x", args, "darwin")
		args[0] = "b"
		if plan.Args[0] != "a" {
			t.Fatal("the plan shares the caller's slice")
		}
	})
}

func TestTaskkillArgs(t *testing.T) {
	want := []string{"/pid", "42", "/T", "/F"}
	if got := TaskkillArgs(42); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
