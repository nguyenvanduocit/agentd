package run

import (
	"errors"
	"os"
	"time"

	"github.com/nguyenvanduocit/agentd/daemon/home"
	"github.com/nguyenvanduocit/agentd/daemon/ui"
)

// instanceKind is what started this daemon, for the record in the lock file and the message of a second daemon: the
// app (it hands the daemon a channel), a service manager (AGENTD_MANAGED=1) or a person at a terminal.
func instanceKind(env home.Env) string {
	_, hasChannel, _ := ui.UIFd(env) // an unusable descriptor is the start's problem to report, not a reason to call it an app
	return home.InstanceKind(home.IsManaged(env), hasChannel)
}

// takeHome takes the lock that lets one daemon run on this home, before anything else of the daemon starts: before the
// registry is read, a link or the gateway starts, or a file of the home is written. stayOut is true when another daemon
// holds it; the caller then ends with home.ExitStopped, and with nothing else done.
//
// The code is 0 and not another one because the exit codes are the daemon's protocol with its service manager
// (daemon/home/exit.go): 0 stays stopped, so launchd, systemd and the launcher's loop leave it alone, while any other
// code is started again at once and counted as a crash, and two quick crashes make the launcher roll a good version back.
//
// A lock that cannot be made for another reason (a home on a file system without locks, a read-only home) is told and
// the daemon runs without it: the lock keeps two daemons from fighting, it is not worth a daemon that does not serve.
func takeHome(deps Deps, now time.Time) (lock *home.InstanceLock, stayOut bool) {
	lock, err := home.AcquireInstanceLock(home.LayoutOf(home.AgentdHome(deps.Host)).LockFile)
	var held *home.HeldError
	switch {
	case errors.As(err, &held):
		deps.Log.Warn(held.Message(now))
		return nil, true
	case err != nil:
		deps.Log.Warn("cannot take the single-instance lock of this home (" + err.Error() + "): running without it, so a second agentd on this home would not be stopped")
		return nil, false
	}
	record := home.LockRecord{PID: os.Getpid(), StartedAt: now.UTC(), Kind: instanceKind(deps.Host.Env)}
	if err := lock.Record(record); err != nil {
		deps.Log.Warn("cannot write the lock record (" + err.Error() + "): a second agentd will not be able to say who runs")
	}
	return lock, false
}
