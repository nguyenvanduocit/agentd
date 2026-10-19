package service

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
)

// systemd is the systemd user unit.
type systemd struct{ Deps }

func unitOf(c ctx) string { return c.name + ".service" }

// ctl runs systemctl for a step whose failure does not matter.
func (s systemd) ctl(args ...string) {
	s.Exec("systemctl", append([]string{"--user"}, args...), nil)
}

// unitState is what systemd says about a unit: Running plus whether it knows the unit at all.
type unitState struct {
	Running
	loaded bool
}

var loadStateLoaded = regexp.MustCompile(`(?m)^LoadState=loaded$`)

// show is nil when systemctl cannot tell about the unit.
func (s systemd) show(c ctx) *unitState {
	r := s.Exec("systemctl", []string{"--user", "show", "-p", "LoadState,ActiveState,ExecMainStatus,MainPID,FragmentPath", unitOf(c)}, nil)
	if r.Code != 0 {
		return nil
	}
	return &unitState{Running: ParseSystemctlShow(r.Stdout), loaded: loadStateLoaded.MatchString(r.Stdout)}
}

func (s systemd) lingerHint() []string {
	user := s.Host.Env["USER"]
	r := s.Exec("loginctl", []string{"show-user", user, "-p", "Linger"}, nil)
	if r.Code != 0 || !LingerOff(r.Stdout) {
		return nil
	}
	return []string{"linger is off: the daemon stops when you log out. To keep it running without a login, run: loginctl enable-linger " + user}
}

func (s systemd) systemctl(verb string, c ctx) error {
	r := s.Exec("systemctl", []string{"--user", verb, unitOf(c)}, nil)
	if r.Code != 0 {
		return fmt.Errorf("systemctl --user %s %s failed: %s", verb, unitOf(c), failure(r))
	}
	return nil
}

func (s systemd) install(c ctx) ([]string, error) {
	now := s.show(c)
	if now != nil && now.loaded && now.Path != "" && !sameFile(now.Path, c.file) {
		return nil, fmt.Errorf("systemd already has a unit named %s from %s, not from %s: not touching it", unitOf(c), now.Path, c.file)
	}
	if err := os.MkdirAll(path.Dir(c.layout.LogFile), 0o777); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(path.Dir(c.file), 0o777); err != nil {
		return nil, err
	}
	text, err := SystemdUnit(UnitInput{Launcher: c.layout.Launcher, WorkDir: c.workDir, LogFile: c.layout.LogFile, Env: ServiceEnv(c.env)})
	if err != nil {
		return nil, err
	}
	if err := s.writeAtomic(c.file, text); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", unitOf(c)}, {"restart", unitOf(c)}} {
		r := s.Exec("systemctl", append([]string{"--user"}, args...), nil)
		if r.Code != 0 {
			return nil, fmt.Errorf("systemctl --user %s failed: %s", strings.Join(args, " "), failure(r))
		}
	}
	started := fmt.Sprintf("started the systemd user unit %s (%s); it starts at login and restarts when the daemon exits", unitOf(c), c.file)
	return append([]string{started}, s.lingerHint()...), nil
}

func (s systemd) uninstall(c ctx) error {
	if now := s.show(c); now != nil && now.loaded && (now.Path == "" || sameFile(now.Path, c.file)) {
		s.ctl("disable", "--now", unitOf(c))
	}
	if err := removeFile(c.file); err != nil {
		return err
	}
	s.ctl("daemon-reload")
	return nil
}

func (s systemd) status(c ctx) *Running {
	if now := s.show(c); now != nil && now.loaded {
		return &now.Running
	}
	return nil
}

func (s systemd) start(c ctx) error   { return s.systemctl("start", c) }
func (s systemd) restart(c ctx) error { return s.systemctl("restart", c) }
