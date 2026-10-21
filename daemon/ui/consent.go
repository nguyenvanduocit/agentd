package ui

import (
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
)

// The consent window: when agentd.app shows the daemon's state it also asks the user (one window: what the origin
// wants, the folders to choose, Allow or Deny) instead of the native dialogs.

// Asker is a Link that can ask the user: the consent window of agentd.app.
type Asker interface {
	// Ask shows the question to the user and waits at most timeoutS for the decision. False means there was no one
	// to ask (the window could not open): the caller asks another way.
	Ask(q consent.Question, timeoutS int) (consent.Decision, bool)
}

// DecisionOf is what the user's choice decides: Allow needs at least one valid folder, anything else is a Deny. lasts is
// how long an Allow lasts (consent.NoEnd for never).
func DecisionOf(allow bool, folders []string, lasts time.Duration) consent.Decision {
	if allow {
		if clean := consent.CleanFolders(folders); len(clean) > 0 {
			return consent.Decision{Answer: consent.AnswerAllow, Folders: clean, Lasts: lasts}
		}
	}
	return consent.Decision{Answer: consent.AnswerDeny}
}
