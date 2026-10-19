package proc

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// PsRow is one line of `ps -axo pid=,pgid=,lstart=`.
type PsRow struct {
	PID    int
	PGID   int
	Lstart string
}

// StateGroup is a recorded process group of a live agent.
type StateGroup struct {
	Pgid int `json:"pgid"`
	// Lstart is the start time of the group's leader (`ps -o lstart=`); "" until it has been read.
	Lstart string `json:"lstart"`
}

// DaemonRef identifies the daemon that wrote the state.
type DaemonRef struct {
	PID    int    `json:"pid"`
	Lstart string `json:"lstart"`
}

// State is the record kept in the session state file.
type State struct {
	Daemon DaemonRef `json:"daemon"`
	// Bin is the directory that holds the agentd-mcp wrapper; the next start removes it.
	Bin    string       `json:"bin"`
	Groups []StateGroup `json:"groups"`
}

// ParsePs parses `ps -axo pid=,pgid=,lstart=`.
func ParsePs(output string) []PsRow {
	var rows []PsRow
	for line := range strings.SplitSeq(output, "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, errPid := strconv.Atoi(f[0])
		pgid, errPgid := strconv.Atoi(f[1])
		if errPid != nil || errPgid != nil {
			continue
		}
		rows = append(rows, PsRow{PID: pid, PGID: pgid, Lstart: strings.Join(f[2:], " ")})
	}
	return rows
}

// integer reads a JSON number that must be a whole number.
func integer(v *float64) (int, bool) {
	if v == nil || *v != math.Trunc(*v) || math.Abs(*v) > 1<<53 {
		return 0, false
	}
	return int(*v), true
}

// ParseState reads the state file; anything that is not a valid record is "no state". A group that cannot be a real
// process group (0, 1, negative, not a number) is dropped.
func ParseState(text string) (State, bool) {
	var raw struct {
		Daemon *struct {
			PID    *float64 `json:"pid"`
			Lstart *string  `json:"lstart"`
		} `json:"daemon"`
		Bin    json.RawMessage    `json:"bin"`
		Groups *[]json.RawMessage `json:"groups"`
	}
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		return State{}, false
	}
	if raw.Daemon == nil || raw.Daemon.Lstart == nil || raw.Groups == nil {
		return State{}, false
	}
	pid, ok := integer(raw.Daemon.PID)
	if !ok {
		return State{}, false
	}
	state := State{Daemon: DaemonRef{PID: pid, Lstart: *raw.Daemon.Lstart}, Groups: []StateGroup{}}
	_ = json.Unmarshal(raw.Bin, &state.Bin) // not a string: ""
	for _, g := range *raw.Groups {
		var r struct {
			Pgid   *float64 `json:"pgid"`
			Lstart *string  `json:"lstart"`
		}
		if json.Unmarshal(g, &r) != nil || r.Lstart == nil {
			continue
		}
		if pgid, ok := integer(r.Pgid); ok && pgid > 1 {
			state.Groups = append(state.Groups, StateGroup{Pgid: pgid, Lstart: *r.Lstart})
		}
	}
	return state, true
}

// Orphan is a group to end, with the number of processes found in it.
type Orphan struct {
	Pgid    int
	Members int
}

// SweepResult is the decision of Sweep.
type SweepResult struct {
	// PredecessorAlive: the daemon that wrote the state is still running; its groups are not orphans.
	PredecessorAlive bool
	Kill             []Orphan
}

// Sweep decides which recorded groups a dead daemon left behind. A group is an orphan when it still has members and
// either its leader is gone (POSIX does not recycle a pid while a group of that id exists) or the leader's start time
// equals the recorded one (a different start time means the pid was recycled by another process). A leader whose
// start time was never recorded is left alone.
func Sweep(prev State, table []PsRow, selfPID int) SweepResult {
	var pd *PsRow
	for i := range table {
		if table[i].PID == prev.Daemon.PID {
			pd = &table[i]
			break
		}
	}
	alive := prev.Daemon.PID != selfPID && pd != nil && (prev.Daemon.Lstart == "" || pd.Lstart == prev.Daemon.Lstart)
	if alive {
		return SweepResult{PredecessorAlive: true}
	}
	kill := []Orphan{}
	for _, g := range prev.Groups {
		members := 0
		var leader *PsRow
		for i := range table {
			if table[i].PGID == g.Pgid {
				members++
			}
			if leader == nil && table[i].PID == g.Pgid {
				leader = &table[i]
			}
		}
		if members == 0 {
			continue
		}
		if leader == nil || (g.Lstart != "" && leader.Lstart == g.Lstart) {
			kill = append(kill, Orphan{Pgid: g.Pgid, Members: members})
		}
	}
	return SweepResult{Kill: kill}
}
