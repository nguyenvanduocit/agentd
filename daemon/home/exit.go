package home

// The daemon's exit codes are its protocol with agentd.app, which runs it:
//
//	0   stay stopped: a Stop from an app's server, the end of agentd.app, a revoked token
//	75  start again now: a Restart from an app's server or from the window (EX_TEMPFAIL)
//	any other code is a crash; the app starts it again after a growing delay.
const (
	// ExitStopped is the exit code that stays stopped.
	ExitStopped = 0
	// ExitRestart is the exit code that starts the daemon again now.
	ExitRestart = 75
)

// Control is what the server asks of the daemon.
type Control string

// The controls a server sends.
const (
	ControlRestart Control = "restart"
	ControlStop    Control = "stop"
)

// ControlOutcome is what the daemon does with a Control: exit with Code, or (Exit false) ignore it and say Why.
type ControlOutcome struct {
	Exit bool
	Code int
	Why  string
}

// ControlOutcomeOf is what a Restart or Stop from the server does. A daemon that nothing starts again would exit and
// stay gone on Restart, so it ignores it; Stop is always honoured.
func ControlOutcomeOf(c Control, managed bool) ControlOutcome {
	if c == ControlStop {
		return ControlOutcome{Exit: true, Code: ExitStopped}
	}
	if managed {
		return ControlOutcome{Exit: true, Code: ExitRestart}
	}
	return ControlOutcome{Why: "not managed: restart ignored"}
}

// IsManaged reports AGENTD_MANAGED=1, set by agentd.app: it starts the daemon again after an exit of 75.
func IsManaged(env Env) bool {
	return env["AGENTD_MANAGED"] == "1"
}
