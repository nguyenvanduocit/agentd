package hub_test

import (
	"errors"
	"testing"

	"github.com/nguyenvanduocit/agentd/hub"
)

func TestMemoryTokensNameTheSecondDaemonOfAMachineAfterTheFirst(t *testing.T) {
	m := hub.NewMemoryTokens()
	a, _ := m.Add(hub.Record{ID: "1", UserID: "u", TokenHash: "h1", Name: "laptop"})
	b, _ := m.Add(hub.Record{ID: "2", UserID: "u", TokenHash: "h2", Name: "laptop"})
	c, _ := m.Add(hub.Record{ID: "3", UserID: "u", TokenHash: "h3", Name: "laptop"})
	other, _ := m.Add(hub.Record{ID: "4", UserID: "v", TokenHash: "h4", Name: "laptop"})
	if a.Name != "laptop" || b.Name != "laptop (2)" || c.Name != "laptop (3)" || other.Name != "laptop" {
		t.Fatalf("names %q %q %q %q", a.Name, b.Name, c.Name, other.Name)
	}
	list, _ := m.List("u")
	if len(list) != 3 || list[0].ID != "1" || list[2].ID != "3" {
		t.Fatalf("list %+v: the user's daemons, the one paired first first", list)
	}
}

func TestMemoryTokensFindOnlyTheUsersOwn(t *testing.T) {
	m := hub.NewMemoryTokens()
	_, _ = m.Add(hub.Record{ID: "1", UserID: "u", TokenHash: "h1", Name: "laptop"})
	if r, err := m.ByHash("h1"); err != nil || r.ID != "1" {
		t.Fatalf("by hash: %+v %v", r, err)
	}
	if _, err := m.ByID("v", "1"); !errors.Is(err, hub.ErrNotFound) {
		t.Fatalf("another user's daemon by id: %v", err)
	}
	if _, err := m.ByHash("nope"); !errors.Is(err, hub.ErrNotFound) {
		t.Fatalf("unknown hash: %v", err)
	}
	if err := m.Delete("v", "1"); !errors.Is(err, hub.ErrNotFound) {
		t.Fatalf("another user deleting it: %v", err)
	}
	if err := m.Delete("u", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ByHash("h1"); !errors.Is(err, hub.ErrNotFound) {
		t.Fatalf("a deleted daemon's token still works: %v", err)
	}
}

func TestMemoryTokensRefuseANameAnotherDaemonOfTheUserHas(t *testing.T) {
	m := hub.NewMemoryTokens()
	_, _ = m.Add(hub.Record{ID: "1", UserID: "u", TokenHash: "h1", Name: "laptop"})
	_, _ = m.Add(hub.Record{ID: "2", UserID: "u", TokenHash: "h2", Name: "desktop"})
	if _, err := m.Rename("u", "2", "laptop"); !errors.Is(err, hub.ErrNameTaken) {
		t.Fatalf("rename onto a name in use: %v", err)
	}
	if _, err := m.Rename("u", "9", "x"); !errors.Is(err, hub.ErrNotFound) {
		t.Fatalf("rename of a daemon that is not there: %v", err)
	}
	if r, err := m.Rename("u", "2", "workstation"); err != nil || r.Name != "workstation" {
		t.Fatalf("rename: %+v %v", r, err)
	}
	if err := m.Seen("2", 1234); err != nil {
		t.Fatal(err)
	}
	if r, _ := m.ByID("u", "2"); r.LastSeen != 1234 {
		t.Fatalf("last seen %d", r.LastSeen)
	}
}
