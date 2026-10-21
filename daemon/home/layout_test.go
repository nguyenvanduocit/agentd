package home

import "testing"

func TestAgentdHome(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  Env
		want string
	}{
		{"~/.agentd", nil, "/home/ann/.agentd"},
		{"$AGENTD_HOME when set", Env{"AGENTD_HOME": "/x/mc"}, "/x/mc"},
		{"$AGENTD_HOME when empty does not count", Env{"AGENTD_HOME": ""}, "/home/ann/.agentd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := AgentdHome(linux(tc.env)); got != tc.want {
				t.Errorf("AgentdHome = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLayoutOfHangsEveryPathOffTheHome(t *testing.T) {
	l := LayoutOf("/h/.agentd")
	want := Layout{Home: "/h/.agentd", AdaptersDir: "/h/.agentd/adapters", LockFile: "/h/.agentd/run/daemon.lock"}
	if l != want {
		t.Errorf("LayoutOf = %+v, want %+v", l, want)
	}
}
