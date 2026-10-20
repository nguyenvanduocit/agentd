package main

import (
	"encoding/json"
	"path/filepath"
	"slices"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The consent window is where the daemon asks the user to let an app or a web page run agents: what it wants, the
// folders it may work in, Allow or Deny. The wire is in daemon/ui/consent.go.

// ConsentRequest is what the daemon asks. Title and Body are the daemon's own words for it, the ones the native dialogs
// use too.
type ConsentRequest struct {
	T        string   `json:"t"`
	ID       string   `json:"id"`
	Origin   string   `json:"origin"`
	Mode     string   `json:"mode"`
	Agents   []string `json:"agents"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	TimeoutS int      `json:"timeoutS"`
}

// ParseConsent reads a line of the daemon that asks the user; false for any other line.
func ParseConsent(line []byte) (ConsentRequest, bool) {
	var r ConsentRequest
	if err := json.Unmarshal(line, &r); err != nil || r.T != "consent" || r.ID == "" {
		return ConsentRequest{}, false
	}
	return r, true
}

// ParseConsentEnd reads a line of the daemon that withdraws a question (it asked elsewhere, or ran out of time): the id
// of the question.
func ParseConsentEnd(line []byte) (string, bool) {
	var m struct {
		T  string `json:"t"`
		ID string `json:"id"`
	}
	if err := json.Unmarshal(line, &m); err != nil || m.T != "consent-end" || m.ID == "" {
		return "", false
	}
	return m.ID, true
}

// ShownLine tells the daemon the window is up: it asks nowhere else.
func ShownLine(id string) []byte {
	b, _ := json.Marshal(struct {
		T  string `json:"t"`
		ID string `json:"id"`
	}{"consent-shown", id})
	return b
}

// AnswerLine is the user's decision. Folders are those the user chose, and mean something for an Allow only.
func AnswerLine(id string, allow bool, folders []string) []byte {
	answer := "deny"
	if allow {
		answer = "allow"
	}
	if folders == nil {
		folders = []string{}
	}
	b, _ := json.Marshal(struct {
		T       string   `json:"t"`
		ID      string   `json:"id"`
		Answer  string   `json:"answer"`
		Folders []string `json:"folders"`
	}{"consent-answer", id, answer, folders})
	return b
}

// Consent is the window of one question: the question and the folders chosen so far. Its fields are read and changed
// on the main thread only.
type Consent struct {
	Request ConsentRequest
	Folders []string
	// Answered: the answer went out, or the daemon withdrew the question; the window may close without saying more.
	Answered bool

	win    *mygo.Window
	answer func(allow bool)
}

// Add chooses folders, each once, in the order chosen; what is not an absolute path is left out.
func (c *Consent) Add(paths []string) {
	for _, p := range paths {
		if p = filepath.Clean(p); filepath.IsAbs(p) && !slices.Contains(c.Folders, p) {
			c.Folders = append(c.Folders, p)
		}
	}
}

// Remove drops the chosen folder at index i.
func (c *Consent) Remove(i int) {
	if i >= 0 && i < len(c.Folders) {
		c.Folders = slices.Delete(c.Folders, i, i+1)
	}
}

// CanAllow is whether Allow may be pressed: an Allow needs a folder to start agents in.
func (c *Consent) CanAllow() bool { return len(c.Folders) > 0 }

// View builds the window.
//
// Deny is the default button: it has the keyboard focus when the window opens, so a stray Return denies, and Escape
// denies wherever the focus is. Allow is only ever pressed on purpose. The body is the disclosure that matters, so it is
// drawn in the text color of the window, not the muted one of its hints.
func (c *Consent) View(ctx *ui.Context) {
	if ctx.Shortcut(0, ui.KeyEscape) {
		c.answer(false)
	}
	ui.Scroll(ctx).Fill().Padding(24).Gap(20).Children(func() {
		ui.Column(ctx).Gap(8).Children(func() {
			ui.Text(ctx, c.Request.Title).FontSize(18).Bold()
			ui.Text(ctx, c.Request.Body)
		})
		c.folders(ctx)
		ui.Row(ctx).Gap(8).Children(func() {
			ui.Spacer(ctx)
			if ui.Button(ctx, "Deny").AutoFocus().Clicked() {
				c.answer(false)
			}
			if ui.PrimaryButton(ctx, "Allow").Disabled(!c.CanAllow()).Clicked() {
				c.answer(true)
			}
		})
	})
}

// folders is the list of chosen folders, each with its own Remove, and Add.
func (c *Consent) folders(ctx *ui.Context) {
	ui.Column(ctx).Gap(8).Children(func() {
		heading(ctx, "Folders it may work in")
		if len(c.Folders) == 0 {
			ui.Text(ctx, "Choose at least one folder. Agents start in the first one.").TextColor(ctx.Theme().TextMuted)
		}
		for i, folder := range c.Folders {
			ui.Row(ctx.Key(folder)).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(ctx, folder).Grow(1).SingleLine().Ellipsis("…")
				if ui.Button(ctx, "Remove").Label("Remove " + folder).Clicked() {
					c.Remove(i)
					c.win.Invalidate()
				}
			})
		}
		if ui.Button(ctx, "Add folder…").Clicked() {
			c.choose()
		}
	})
}

// choose opens the system's folder chooser (it waits for the user, so not on the main thread) and adds what was chosen.
func (c *Consent) choose() {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:            c.win,
			Title:             "Folders it may work in",
			ButtonLabel:       "Add",
			Directory:         true,
			Multiple:          true,
			CreateDirectories: true,
		})
		if err != nil || len(paths) == 0 {
			return
		}
		mygo.RunOnMain(func() {
			if c.Answered {
				return
			}
			c.Add(paths)
			c.win.Invalidate()
		})
	}()
}
