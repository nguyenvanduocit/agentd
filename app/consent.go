package main

import (
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/nguyenvanduocit/agentd/daemon/consent"
)

// The consent window is where the daemon asks the user to let an app or a web page run agents: what it wants, the
// folders agents start in, Allow or Deny.

// ConsentRequest is what the window shows: the daemon's words for the question and how long it waits for the answer.
type ConsentRequest struct {
	Title    string
	Body     string
	TimeoutS int
}

// RequestOf is the window's question for the daemon's: its words, and its time.
func RequestOf(q consent.Question, timeoutS int) ConsentRequest {
	text := consent.ConsentText(q)
	return ConsentRequest{Title: text.Title, Body: text.Body, TimeoutS: timeoutS}
}

// Consent is the window of one question: the question, the folders chosen so far, how long the access lasts, and where
// the decision goes. Its fields are read and changed on the main thread only.
type Consent struct {
	Request ConsentRequest
	Folders []string
	// Lasts is the index of the chosen time in consent.LastsChoices.
	Lasts int
	// Answered: the decision went out; the window may close without saying more.
	Answered bool

	win    *mygo.Window
	answer func(allow bool)
	result chan<- consent.Decision
}

// newConsent is the window of a question with the time the daemon suggests chosen.
func newConsent(req ConsentRequest, result chan<- consent.Decision) *Consent {
	return &Consent{Request: req, Lasts: consent.DefaultLastsChoice, result: result}
}

// LastsFor is how long the chosen time lasts (consent.NoEnd for never).
func (c *Consent) LastsFor() time.Duration { return consent.LastsChoices[c.Lasts].Lasts }

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
		c.lasting(ctx)
		ui.Column(ctx).Gap(4).Children(func() {
			if line := timeoutLine(c.Request.TimeoutS); line != "" {
				ui.Text(ctx, line).FontSize(11).TextColor(ctx.Theme().TextMuted)
			}
			ui.Row(ctx).Gap(8).Children(func() {
				ui.Spacer(ctx)
				if ui.Button(ctx, "Deny").AutoFocus().Clicked() {
					c.answer(false)
				}
				if ui.PrimaryButton(ctx, "Allow").Disabled(!c.CanAllow()).Clicked() {
					c.answer(true)
				}
			})
			if !c.CanAllow() {
				ui.Text(ctx, "Add a folder to turn on Allow.").FontSize(11).TextColor(ctx.Theme().TextMuted)
			}
		})
	})
}

// lasting is the choice of how long the access lasts, with the day it ends said under it.
func (c *Consent) lasting(ctx *ui.Context) {
	labels := make([]string, len(consent.LastsChoices))
	for i, choice := range consent.LastsChoices {
		labels[i] = choice.Label
	}
	ui.Column(ctx).Gap(8).Children(func() {
		heading(ctx, "How long")
		ui.Segmented(ctx, &c.Lasts, labels...).Label("How long")
		ui.Text(ctx, endLine(c.LastsFor(), time.Now())).FontSize(11).TextColor(ctx.Theme().TextMuted)
	})
}

// endLine says when access given at now for lasts ends, so the choice has a day and not only a length.
func endLine(lasts time.Duration, now time.Time) string {
	if lasts == consent.NoEnd {
		return "The access has no end. You can disconnect it from the agentd icon in the menu bar."
	}
	return "The access ends " + now.Add(lasts).Format("Mon 2 Jan, 15:04") + "."
}

// timeoutLine says what no answer counts as, so a window that closes by itself is no surprise; empty when the daemon
// named no time.
func timeoutLine(seconds int) string {
	switch {
	case seconds <= 0:
		return ""
	case seconds%60 == 0:
		return fmt.Sprintf("No answer within %d min counts as Deny.", seconds/60)
	}
	return fmt.Sprintf("No answer within %d s counts as Deny.", seconds)
}

// folders is the list of chosen folders, each with its own Remove, and Add.
func (c *Consent) folders(ctx *ui.Context) {
	ui.Column(ctx).Gap(8).Children(func() {
		heading(ctx, "Where agents start")
		ui.Text(ctx, "Agents start in the first folder. They can still reach files outside it.").TextColor(ctx.Theme().TextMuted)
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
			Title:             "Where agents start",
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
