package home

// The daemon's exit codes are its protocol with whatever restarts it (launchd, systemd, the launcher's loop):
//
//	0   stay stopped: a Stop from the web UI, a SIGTERM from the service manager, a revoked token
//	75  start again now: a Restart from the web UI, an update that is in place (EX_TEMPFAIL)
//	any other code is a crash; the service restarts it after its throttle.
//
// launcher.sh carries the same 75.
const (
	// ExitStopped is the exit code that stays stopped.
	ExitStopped = 0
	// ExitRestart is the exit code that starts the daemon again now.
	ExitRestart = 75
)

// ExitMeaning is what an exit code tells whoever restarts the daemon.
type ExitMeaning string

// The meanings of an exit code.
const (
	MeaningStopped ExitMeaning = "stopped"
	MeaningRestart ExitMeaning = "restart"
	MeaningCrash   ExitMeaning = "crash"
)

// ClassifyExit is the meaning of an exit code.
func ClassifyExit(code int) ExitMeaning {
	switch code {
	case ExitStopped:
		return MeaningStopped
	case ExitRestart:
		return MeaningRestart
	}
	return MeaningCrash
}

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

// ControlOutcomeOf is what a Restart or Stop from the server does. A daemon that no service restarts would exit and
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

// IsManaged reports AGENTD_MANAGED=1, set by the service definitions, by the launcher's restart loop and by agentd.app.
func IsManaged(env Env) bool {
	return env["AGENTD_MANAGED"] == "1"
}
