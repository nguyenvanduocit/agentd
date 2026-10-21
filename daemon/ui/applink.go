package ui

// Link shows the daemon's state and hands back the user's choices: agentd.app, in the same process. Start is called once
// before the first Update; Stop once at the end.
type Link interface {
	Start()
	Update(State)
	Stop()
}
