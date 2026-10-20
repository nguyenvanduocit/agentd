package consent

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/nguyenvanduocit/agentd/daemon/home"
)

// PickTimeoutS is how long the folder picker waits: choosing folders takes longer than answering Allow or Deny. No
// choice in time counts as a Deny.
const PickTimeoutS = 300

// Decision is what the user decided: the answer, and for an Allow the folders they chose (at least one).
type Decision struct {
	Answer  Answer
	Folders []string
}

// Decide asks the user in native dialogs: Allow or Deny, then, after an Allow, the folders the origin may start agents
// in. A picker that is cancelled or closed without a folder is a Deny. `AGENTD_CONSENT` (see Ask) stands for both
// dialogs: what the program prints, one folder per line, is the choice.
func Decide(q Question, env home.Env, timeoutS int) Decision {
	return decideOn(runtime.GOOS, q, env, timeoutS)
}

func decideOn(platform string, q Question, env home.Env, timeoutS int) Decision {
	answer, out := ask(platform, q, env, timeoutS)
	if answer != AnswerAllow {
		return Decision{Answer: answer}
	}
	if env["AGENTD_CONSENT"] != "" {
		return Decision{Answer: AnswerAllow, Folders: FoldersOf(out)}
	}
	return pickOn(platform, q.Origin, env, PickTimeoutS)
}

// The prompt travels as an argument (`argv` for osascript), never inside the script, like the dialog's text.
var osascriptPick = []string{
	"on run argv",
	"set picked to choose folder with prompt (item 1 of argv) with multiple selections allowed",
	`set out to ""`,
	"repeat with p in picked",
	"set out to out & POSIX path of p & linefeed",
	"end repeat",
	"return out",
	"end run",
}

// PickPrompt is what the picker says: whose folders they are.
func PickPrompt(origin string) string {
	return "Choose the folders " + origin + " may start agents in. They start in the first one."
}

// PickPlan is the command that shows the folder picker on `platform` (a GOOS value), or false when the machine has no
// tool to show one. kdialog picks one folder; the others pick several.
func PickPlan(platform string, has func(cmd string) bool, prompt string) (DialogPlan, bool) {
	if platform == "darwin" && has("osascript") {
		args := make([]string, 0, 2*len(osascriptPick)+1)
		for _, line := range osascriptPick {
			args = append(args, "-e", line)
		}
		return DialogPlan{Kind: KindOsascript, File: "osascript", Args: append(args, prompt)}, true
	}
	if platform != "linux" {
		return DialogPlan{}, false
	}
	if has("zenity") {
		return DialogPlan{Kind: KindZenity, File: "zenity", Args: []string{
			"--file-selection", "--directory", "--multiple", "--separator=\n", "--title=" + prompt,
		}}, true
	}
	if has("kdialog") {
		return DialogPlan{Kind: KindKdialog, File: "kdialog", Args: []string{"--title", prompt, "--getexistingdirectory", "."}}, true
	}
	return DialogPlan{}, false
}

// FoldersOf is the folders a picker printed, one per line (see CleanFolders).
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

func pickOn(platform, origin string, env home.Env, timeoutS int) Decision {
	has := func(cmd string) bool { _, ok := home.FindProgram(cmd, env); return ok }
	plan, ok := PickPlan(platform, has, PickPrompt(origin))
	if !ok {
		return Decision{Answer: AnswerUnavailable}
	}
	path, _ := home.FindProgram(plan.File, env)
	r := run(path, plan.Args, timeoutS)
	switch {
	case r.timedOut:
		return Decision{Answer: AnswerTimeout}
	case r.failed:
		return Decision{Answer: AnswerUnavailable}
	case r.code != 0:
		return Decision{Answer: AnswerDeny}
	}
	folders := FoldersOf(r.stdout)
	if len(folders) == 0 {
		return Decision{Answer: AnswerDeny}
	}
	return Decision{Answer: AnswerAllow, Folders: folders}
}
