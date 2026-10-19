package proc

import (
	"encoding/json"
	"reflect"
	"testing"
)

func row(pid, pgid int, lstart string) PsRow { return PsRow{PID: pid, PGID: pgid, Lstart: lstart} }

func state(groups []StateGroup, daemon ...DaemonRef) State {
	d := DaemonRef{PID: 100, Lstart: "D"}
	if len(daemon) > 0 {
		d = daemon[0]
	}
	return State{Daemon: d, Bin: "/t/bin", Groups: groups}
}

func TestParsePs(t *testing.T) {
	t.Run("pid, pgid and the five-word start time", func(t *testing.T) {
		out := "    1     1 Sat Oct 11 17:16:30 2026\n  812   812 Sat Oct 11 17:20:01 2026\nnot a row\n\n"
		want := []PsRow{row(1, 1, "Sat Oct 11 17:16:30 2026"), row(812, 812, "Sat Oct 11 17:20:01 2026")}
		if got := ParsePs(out); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
}

func TestParseState(t *testing.T) {
	t.Run("a valid record", func(t *testing.T) {
		want := state([]StateGroup{{Pgid: 5, Lstart: "x"}})
		text, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := ParseState(string(text))
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v (%v), want %+v", got, ok, want)
		}
	})

	t.Run("damage is no state", func(t *testing.T) {
		for _, text := range []string{"not json", "{}", `{"daemon":{"pid":1,"lstart":""},"groups":"x"}`, "null", "[]", `{"daemon":{"pid":1,"lstart":""}}`} {
			if _, ok := ParseState(text); ok {
				t.Errorf("%q is a state", text)
			}
		}
	})

	t.Run("a group that cannot be a real process group is dropped (0, 1, negative, text)", func(t *testing.T) {
		text := `{"daemon":{"pid":9,"lstart":""},"groups":[{"pgid":0,"lstart":""},{"pgid":1,"lstart":""},{"pgid":-7,"lstart":""},{"pgid":"5","lstart":""},{"pgid":6,"lstart":""}]}`
		got, ok := ParseState(text)
		if !ok {
			t.Fatal("no state")
		}
		if want := []StateGroup{{Pgid: 6}}; !reflect.DeepEqual(got.Groups, want) {
			t.Fatalf("groups %+v, want %+v", got.Groups, want)
		}
		if got.Bin != "" {
			t.Fatalf("bin %q", got.Bin)
		}
	})
}

func TestSweep(t *testing.T) {
	const self = 200
	group := func(lstart string) []StateGroup { return []StateGroup{{Pgid: 500, Lstart: lstart}} }

	t.Run("a group whose leader is gone but has members is an orphan", func(t *testing.T) {
		got := Sweep(state(group("L")), []PsRow{row(501, 500, "a"), row(502, 500, "b")}, self)
		want := SweepResult{Kill: []Orphan{{Pgid: 500, Members: 2}}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("a leader with the recorded start time is ours, with another start time it is a recycled pid", func(t *testing.T) {
		if n := len(Sweep(state(group("L")), []PsRow{row(500, 500, "L")}, self).Kill); n != 1 {
			t.Fatalf("recorded start time: %d groups", n)
		}
		if n := len(Sweep(state(group("L")), []PsRow{row(500, 500, "OTHER")}, self).Kill); n != 0 {
			t.Fatalf("another start time: %d groups", n)
		}
	})

	t.Run("a start time that was never recorded is left alone while the leader lives", func(t *testing.T) {
		if n := len(Sweep(state(group("")), []PsRow{row(500, 500, "L")}, self).Kill); n != 0 {
			t.Fatalf("live leader: %d groups", n)
		}
		if n := len(Sweep(state(group("")), []PsRow{row(501, 500, "L")}, self).Kill); n != 1 {
			t.Fatalf("gone leader: %d groups", n)
		}
	})

	t.Run("a group with no processes needs nothing", func(t *testing.T) {
		if n := len(Sweep(state(group("L")), []PsRow{row(1, 1, "x")}, self).Kill); n != 0 {
			t.Fatalf("%d groups", n)
		}
	})

	t.Run("a predecessor that is still running owns its groups", func(t *testing.T) {
		got := Sweep(state(group("L")), []PsRow{row(100, 100, "D"), row(501, 500, "x")}, self)
		if !reflect.DeepEqual(got, SweepResult{PredecessorAlive: true}) {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("a recycled pid of the predecessor is not the predecessor", func(t *testing.T) {
		got := Sweep(state(group("L")), []PsRow{row(100, 100, "SOMEONE ELSE"), row(501, 500, "x")}, self)
		if got.PredecessorAlive || len(got.Kill) != 1 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("a state written by this very process is never a live predecessor", func(t *testing.T) {
		got := Sweep(state(nil, DaemonRef{PID: self, Lstart: "D"}), []PsRow{row(self, self, "D")}, self)
		if got.PredecessorAlive {
			t.Fatal("this process is its own predecessor")
		}
	})

	t.Run("an unknown start time of the predecessor counts as alive while its pid exists", func(t *testing.T) {
		got := Sweep(state(nil, DaemonRef{PID: 100}), []PsRow{row(100, 100, "anything")}, self)
		if !got.PredecessorAlive {
			t.Fatal("the predecessor is not alive")
		}
	})
}
