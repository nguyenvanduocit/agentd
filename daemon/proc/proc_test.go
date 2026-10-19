package proc

import (
	"strings"
	"testing"
)

func TestExec(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		got := Exec("sh", []string{"-c", "echo out; echo err >&2"}, nil)
		if got != (Exited{Code: 0, Stdout: "out\n", Stderr: "err\n"}) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("a failing program is its exit code", func(t *testing.T) {
		got := Exec("sh", []string{"-c", "echo partial; echo bad >&2; exit 3"}, nil)
		if got != (Exited{Code: 3, Stdout: "partial\n", Stderr: "bad\n"}) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("a failing program with no stderr says why", func(t *testing.T) {
		got := Exec("sh", []string{"-c", "exit 2"}, nil)
		if got.Code != 2 || got.Stderr == "" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("a missing program is 127", func(t *testing.T) {
		got := Exec("/nonexistent/agentd-test-program", nil, nil)
		if got.Code != 127 || got.Stderr == "" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("a program killed by a signal is 127", func(t *testing.T) {
		if got := Exec("sh", []string{"-c", "kill -9 $$"}, nil); got.Code != 127 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("env replaces the environment", func(t *testing.T) {
		got := Exec("sh", []string{"-c", "echo $AGENTD_TEST"}, []string{"AGENTD_TEST=yes"})
		if strings.TrimSpace(got.Stdout) != "yes" {
			t.Fatalf("got %+v", got)
		}
	})
}
