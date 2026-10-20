package consent

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// TimeoutS is how long the dialog waits for an answer; no answer counts as a Deny.
const TimeoutS = 60

// killGrace is added to the dialog tool's own timeout: the tools stop by themselves after timeoutS; the extra is for
// those that cannot (kdialog) or that hang.
const killGrace = 5 * time.Second

// pipeWait bounds the wait for a killed tool's output pipes, which a grandchild may still hold.
const pipeWait = time.Second

type exit struct {
	code     int
	stdout   string
	timedOut bool
	// failed: the program could not be run at all.
	failed bool
}

func run(file string, args []string, timeoutS int) exit {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutS)*time.Second+killGrace)
	defer cancel()
	cmd := exec.CommandContext(ctx, file, args...)
	cmd.WaitDelay = pipeWait
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err := cmd.Run()
	if err == nil {
		return exit{stdout: stdout.String()}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return exit{code: -1, stdout: stdout.String(), timedOut: true}
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() >= 0 {
		return exit{code: ee.ExitCode(), stdout: stdout.String()}
	}
	return exit{code: -1, stdout: stdout.String(), failed: true}
}

// Ask asks the user in a native dialog. `AGENTD_CONSENT` in env (a test seam, like AGENTD_AGENT_<NAME>) names a program
// that is run instead, with the title and the body as arguments; exit 0 means Allow. AnswerUnavailable means the
// machine has no way to ask (no tool, no graphical session): the caller must not treat it as consent. env is the
// environment the dialog tool is looked up in; callers pass TimeoutS.
func Ask(q Question, env home.Env, timeoutS int) Answer {
	return askOn(runtime.GOOS, q, env, timeoutS)
}

func askOn(platform string, q Question, env home.Env, timeoutS int) Answer {
	answer, _ := ask(platform, q, env, timeoutS)
	return answer
}

// ask is the Allow/Deny dialog: the answer, and what the tool printed (the seam's folders).
func ask(platform string, q Question, env home.Env, timeoutS int) (Answer, string) {
	text := DialogText(q)
	var (
		kind DialogKind
		file string
		args []string
	)
	if seam := env["AGENTD_CONSENT"]; seam != "" {
		file, args = seam, []string{text.Title, text.Body}
	} else {
		has := func(cmd string) bool { _, ok := home.FindProgram(cmd, env); return ok }
		plan, ok := Plan(platform, has, text, timeoutS)
		if !ok {
			return AnswerUnavailable, ""
		}
		// Run the program found in env's PATH: the lookup and the run agree on which one.
		path, _ := home.FindProgram(plan.File, env)
		kind, file, args = plan.Kind, path, plan.Args
	}
	r := run(file, args, timeoutS)
	if r.timedOut {
		return AnswerTimeout, r.stdout
	}
	if r.failed {
		return AnswerUnavailable, r.stdout
	}
	if kind == "" {
		if r.code == 0 {
			return AnswerAllow, r.stdout
		}
		return AnswerDeny, r.stdout
	}
	return AnswerOf(kind, r.code, r.stdout), r.stdout
}
