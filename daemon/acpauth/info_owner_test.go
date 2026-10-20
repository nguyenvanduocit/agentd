package acpauth

import (
	"os"
	"testing"
)

// Two daemons can run on one home (agentd.app beside a terminal `agentd run`). The one that ends takes away its own
// record and leaves the other's.
func TestRemoveInfoOfTakesAwayOnlyTheRecordThatNamesTheCaller(t *testing.T) {
	home := t.TempDir()
	mine := Info{Address: "ws://127.0.0.1:47615/acp", Agents: []string{"claude"}}
	theirs := Info{Address: "ws://127.0.0.1:47616/acp", Agents: []string{"claude"}}

	if err := WriteInfo(home, theirs); err != nil {
		t.Fatal(err)
	}
	if err := RemoveInfoOf(home, mine.Address); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadInfo(home); err != nil || got.Address != theirs.Address {
		t.Fatalf("after the other daemon ended, ReadInfo() = %+v, %v; the record of %s must stay", got, err, theirs.Address)
	}

	if err := RemoveInfoOf(home, theirs.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadInfo(home); !os.IsNotExist(err) {
		t.Errorf("the record of the daemon that ended is still there: %v", err)
	}
	if err := RemoveInfoOf(home, mine.Address); err != nil {
		t.Errorf("removing what is not there: %v", err)
	}
}

// A record that cannot be read names no daemon; it is left for whoever writes the next one.
func TestRemoveInfoOfLeavesARecordThatCannotBeRead(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(InfoFile(home), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RemoveInfoOf(home, "ws://127.0.0.1:47615/acp"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(InfoFile(home)); err != nil {
		t.Errorf("the unreadable record was removed: %v", err)
	}
}
