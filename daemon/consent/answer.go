package consent

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// TimeoutS is how long the question waits for the user: choosing folders takes longer than answering Allow or Deny. No
// answer in time counts as a Deny.
const TimeoutS = 300

// Decision is what the user decided: the answer, and for an Allow the folders they chose (at least one) and how long
// the access lasts (NoEnd for never; none chosen is DefaultLasts).
type Decision struct {
	Answer  Answer
	Folders []string
	Lasts   time.Duration
}

// LastsOrDefault is how long the access lasts: what was chosen, or DefaultLasts when no time was.
func (d Decision) LastsOrDefault() time.Duration {
	if d.Lasts == 0 {
		return DefaultLasts
	}
	return d.Lasts
}

// The user answers in agentd.app's consent window (daemon/ui.Asker). A test answers with a program instead:
// AGENTD_CONSENT names it, and Decide and Ask run it.

// killGrace is added to the program's own timeout: the program has that long to answer; the extra is for one that hangs.
const killGrace = 5 * time.Second

// pipeWait bounds the wait for a killed program's output pipe, which a grandchild may still hold.
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

// Decide runs the program that AGENTD_CONSENT names in env, with the title and the body of the question as arguments:
// exit 0 is an Allow for the absolute folders it prints, one per line; any other exit is a Deny. AnswerUnavailable means
// there is no program, or it cannot run: the caller must not treat it as consent.
func Decide(q Question, env home.Env, timeoutS int) Decision {
	program := env["AGENTD_CONSENT"]
	if program == "" {
		return Decision{Answer: AnswerUnavailable}
	}
	text := ConsentText(q)
	r := run(program, []string{text.Title, text.Body}, timeoutS)
	switch {
	case r.timedOut:
		return Decision{Answer: AnswerTimeout}
	case r.failed:
		return Decision{Answer: AnswerUnavailable}
	case r.code != 0:
		return Decision{Answer: AnswerDeny}
	}
	return Decision{Answer: AnswerAllow, Folders: FoldersOf(r.stdout)}
}

// FoldersOf is the folders a program printed, one per line (see CleanFolders).
func FoldersOf(stdout string) []string {
	return CleanFolders(strings.Split(stdout, "\n"))
}

// CleanFolders is the folders a user chose, as the daemon keeps them: absolute, cleaned, each once, in the order
// chosen. Anything that is not an absolute path is left out.
func CleanFolders(chosen []string) []string {
	var folders []string
	for _, line := range chosen {
		line = strings.TrimRight(line, "\r")
		if !filepath.IsAbs(line) {
			continue
		}
		if folder := filepath.Clean(line); !slices.Contains(folders, folder) {
			folders = append(folders, folder)
		}
	}
	return folders
}
