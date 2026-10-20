package goserver

import (
	"log"
	"reflect"
	"sync"
)

// watcher is one browser tab following its user's daemons. Every list is a whole snapshot, so a tab that is slow
// only ever needs the latest: the channel holds one.
type watcher struct{ ch chan List }

// Watch returns the user's list of daemons as it changes, starting with the current one. A reader that is slow only
// ever gets the latest list. Call cancel when the tab goes: it closes the channel, so `for list := range ch` ends, and
// calling it again does nothing.
func (h *Server) Watch(userID string) (<-chan List, func()) {
	w := &watcher{ch: make(chan List, 1)}
	h.pubMu.Lock()
	defer h.pubMu.Unlock()
	h.mu.Lock()
	if h.watchers[userID] == nil {
		h.watchers[userID] = map[*watcher]struct{}{}
	}
	h.watchers[userID][w] = struct{}{}
	h.mu.Unlock()
	if list, err := h.List(userID); err != nil {
		log.Printf("daemon: first list of %s: %v", userID, err)
	} else {
		w.ch <- list
		h.sent[userID] = list
	}
	var once sync.Once
	return w.ch, func() {
		once.Do(func() {
			h.pubMu.Lock()
			defer h.pubMu.Unlock()
			h.mu.Lock()
			defer h.mu.Unlock()
			delete(h.watchers[userID], w)
			if len(h.watchers[userID]) == 0 {
				delete(h.watchers, userID)
				delete(h.sent, userID)
			}
			close(w.ch) // publish sends only under pubMu, which is held: no send can follow
		})
	}
}

// publish sends the user's current list to every tab watching it, unless it is the list they already have: a change
// that does not show (a session that opens and closes between two lists) is not pushed.
func (h *Server) publish(userID string) {
	h.pubMu.Lock()
	defer h.pubMu.Unlock()
	list, err := h.List(userID)
	if err != nil {
		log.Printf("daemon: publish the list of %s: %v", userID, err)
		return
	}
	h.mu.Lock()
	ws := make([]*watcher, 0, len(h.watchers[userID]))
	for w := range h.watchers[userID] {
		ws = append(ws, w)
	}
	h.mu.Unlock()
	if len(ws) == 0 || reflect.DeepEqual(h.sent[userID], list) {
		return
	}
	h.sent[userID] = list
	for _, w := range ws {
		select {
		case <-w.ch: // the tab has not read the previous one: replace it
		default:
		}
		w.ch <- list // pubMu makes this the only sender, and the slot is free
	}
}
