package agents

import (
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/proc"
)

// run starts `sh -c script` as a session whose frames go to sink; ended counts onEnd calls.
func run(t *testing.T, sink FrameSink, script string, maxLine int) (*Session, *atomic.Int32) {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	proc.Detach(cmd)
	var ended atomic.Int32
	s, err := startSession("s1", cmd, sink, func() { ended.Add(1) }, maxLine)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.SignalGroup(s.PGID(), true) })
	return s, &ended
}

func waitExit(t *testing.T, sink *recordingSink) frame {
	t.Helper()
	waitFor(t, "the exit frame", 10*time.Second, func() bool { _, ok := sink.exit(); return ok })
	f, _ := sink.exit()
	return f
}

func TestSessionFrames(t *testing.T) {
	t.Run("stdout lines are out frames, stderr text err frames, the end one exit frame carrying the code", func(t *testing.T) {
		sink := &recordingSink{}
		_, ended := run(t, sink, `printf 'a\r\nb\npartial'; echo oops >&2; exit 3`, proc.MaxLineBytes)
		exit := waitExit(t, sink)
		if exit != (frame{"s1", FrameExit, "", 3}) {
			t.Fatalf("got %+v", exit)
		}
		if got := sink.data(FrameOut); !reflect.DeepEqual(got, []string{"a", "b"}) {
			t.Fatalf("out: %q (an unterminated tail is never emitted)", got)
		}
		if got := sink.data(FrameErr); !reflect.DeepEqual(got, []string{"oops\n"}) {
			t.Fatalf("err: %q", got)
		}
		frames := sink.all()
		if frames[len(frames)-1].Kind != FrameExit || ended.Load() != 1 {
			t.Fatalf("the exit frame is last and onEnd ran once: %v, %d", frames, ended.Load())
		}
	})

	t.Run("a process a signal ended exits with 128", func(t *testing.T) {
		sink := &recordingSink{}
		run(t, sink, `kill -9 $$`, proc.MaxLineBytes)
		if got := waitExit(t, sink); got.Code != 128 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("a character split across two reads stays whole", func(t *testing.T) {
		sink := &recordingSink{}
		run(t, sink, `printf '\342\202' >&2; sleep 0.3; printf '\254\n' >&2`, proc.MaxLineBytes)
		waitExit(t, sink)
		if got := strings.Join(sink.data(FrameErr), ""); got != "€\n" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("a process that outlives its leader in the group is ended with it", func(t *testing.T) {
		sink := &recordingSink{}
		began := time.Now()
		run(t, sink, `sleep 60 & echo started`, proc.MaxLineBytes)
		waitExit(t, sink)
		if took := time.Since(began); took > pipeGrace-time.Second {
			t.Fatalf("took %v: the group was not ended with the leader", took)
		}
		if got := sink.data(FrameOut); !reflect.DeepEqual(got, []string{"started"}) {
			t.Fatalf("got %q", got)
		}
	})
}

func TestSessionWriteAndEnd(t *testing.T) {
	t.Run("a written line reaches the agent's stdin; after the exit nothing can be written", func(t *testing.T) {
		sink := &recordingSink{}
		s, _ := run(t, sink, `read line; echo "got $line"`, proc.MaxLineBytes)
		if !s.Write("hello") {
			t.Fatal("write failed")
		}
		waitExit(t, sink)
		if got := sink.data(FrameOut); !reflect.DeepEqual(got, []string{"got hello"}) {
			t.Fatalf("got %q", got)
		}
		if s.Write("late") {
			t.Fatal("a line was written to an agent that is gone")
		}
	})

	t.Run("End ends the group: SIGTERM, then SIGKILL after the grace period", func(t *testing.T) {
		sink := &recordingSink{}
		s, _ := run(t, sink, `trap '' TERM; echo up; while :; do sleep 1; done`, proc.MaxLineBytes)
		waitFor(t, "the agent to start", 5*time.Second, func() bool { return len(sink.data(FrameOut)) > 0 })
		began := time.Now()
		s.End()
		exit := waitExit(t, sink)
		if took := time.Since(began); took < termGrace-200*time.Millisecond || exit.Code != 128 {
			t.Fatalf("exit %+v after %v: a TERM-ignoring agent is killed after the grace period", exit, took)
		}
	})
}

func TestSessionBackpressure(t *testing.T) {
	t.Run("the agent's output is not read while the sink has no room", func(t *testing.T) {
		sink := &recordingSink{}
		release := make(chan struct{})
		sink.setBacklog(release)
		run(t, sink, `echo 1; sleep 0.2; echo 2`, proc.MaxLineBytes)
		waitFor(t, "the first line", 5*time.Second, func() bool { return len(sink.data(FrameOut)) == 1 })
		time.Sleep(600 * time.Millisecond)
		if got := sink.data(FrameOut); !reflect.DeepEqual(got, []string{"1"}) {
			t.Fatalf("read on while the sink was full: %q", got)
		}
		sink.setBacklog(nil)
		close(release)
		waitExit(t, sink)
		if got := sink.data(FrameOut); !reflect.DeepEqual(got, []string{"1", "2"}) {
			t.Fatalf("got %q", got)
		}
	})
}

func TestSessionLineTooLong(t *testing.T) {
	sink := &recordingSink{}
	run(t, sink, `echo ok; echo 0123456789012345678901234567890; sleep 30`, 16)
	exit := waitExit(t, sink)
	if got := sink.data(FrameOut); !reflect.DeepEqual(got, []string{"ok"}) {
		t.Fatalf("out: %q", got)
	}
	errs := strings.Join(sink.data(FrameErr), "")
	if !strings.Contains(errs, "the agent wrote a line longer than 32 MiB; the session is ended") {
		t.Fatalf("err: %q", errs)
	}
	if exit.Code != 128 {
		t.Fatalf("got %+v", exit)
	}
}

func TestStartSessionReportsACommandThatCannotRun(t *testing.T) {
	sink := &recordingSink{}
	cmd := exec.Command("/nonexistent/agentd-test-agent")
	if _, err := StartSession("s1", cmd, sink, func() { t.Error("onEnd ran for a session that never started") }); err == nil {
		t.Fatal("no error")
	}
	time.Sleep(50 * time.Millisecond)
	if got := sink.all(); len(got) != 0 {
		t.Fatalf("frames: %v", got)
	}
}
