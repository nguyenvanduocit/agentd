package goserver

import (
	"context"
	"errors"
	"fmt"
)

// control tells the user's daemon id to end itself with how: ending is what the sessions that die with it say.
func (h *Server) control(userID, id, ending string, call func(*peer, context.Context) error, needs func(*peer) error) (List, error) {
	p, err := h.connected(userID, id)
	var offline OfflineError
	switch {
	case errors.Is(err, ErrUnknownDaemon):
		return List{}, ErrUnknownDaemon
	case errors.As(err, &offline):
		return List{}, offlineCall{offline}
	case err != nil:
		return List{}, err
	}
	if err := needs(p); err != nil {
		return List{}, err
	}
	h.mu.Lock()
	p.ending = ending
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), ackTimeout)
	defer cancel()
	if err := call(p, ctx); err != nil {
		h.mu.Lock()
		p.ending = ""
		h.mu.Unlock()
		return List{}, fmt.Errorf("tell %s: %w", p.name, err)
	}
	return h.List(userID)
}

// Restart tells the user's daemon id to exit so that its service starts it again. The daemon closes its own socket;
// the list changes when that happens. A daemon that runs without a service has nothing to start it again.
//
// Errors: ErrUnknownDaemon when id is not one of the user's daemons; an error that errors.As finds an OfflineError in
// when it is paired but not connected (its text is "<name> is offline"); IncompatibleError when it is connected but
// incompatible; a plain error when the daemon runs without a service (Info.Managed is false) or did not answer.
func (h *Server) Restart(userID, id string) (List, error) {
	return h.control(userID, id, "the daemon was restarted",
		func(p *peer, ctx context.Context) error { return p.client.CallRestart(ctx) },
		func(p *peer) error {
			h.mu.Lock()
			managed, name := p.info.Managed, p.name
			h.mu.Unlock()
			if !managed {
				return fmt.Errorf("%s was started without a service and cannot be restarted; stop it or restart it on its machine", name)
			}
			return nil
		})
}

// Stop tells the user's daemon id to exit and stay stopped. It cannot be started again from the web. It answers the
// errors of Restart, except the one about a missing service.
func (h *Server) Stop(userID, id string) (List, error) {
	return h.control(userID, id, "the daemon was stopped",
		func(p *peer, ctx context.Context) error { return p.client.CallStop(ctx) },
		func(*peer) error { return nil })
}

// endingOf is why the daemon is going, empty when nobody told it to.
func (h *Server) endingOf(p *peer) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return p.ending
}

// noteOf is what a session that ends with p says: why the daemon was told to go, else fallback.
func (h *Server) noteOf(p *peer, fallback string) string {
	if ending := h.endingOf(p); ending != "" {
		return ending
	}
	return fallback
}
