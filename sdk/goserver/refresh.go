package goserver

// Refresh tells the browser tabs of userID that are connected to THIS Server (the channels of Watch) the user's list as
// it is now, if it is not the list they already have. A Server tells its tabs about what happens to it; with several
// instances of an app, what another instance did (it issued a code, a daemon paired or connected there) does not reach
// them until something tells them to look again: an app that runs more than one instance calls Refresh from whatever
// it uses to carry a message between its instances (a Redis channel, a Postgres LISTEN). Since 0.8.0.
func (h *Server) Refresh(userID string) { h.publish(userID) }
