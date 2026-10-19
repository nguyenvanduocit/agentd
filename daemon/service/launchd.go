package service

import (
	"fmt"
	"os"
	"path"
	"strconv"
	"time"
)

// launchd is the LaunchAgent of the user's login session.
type launchd struct{ Deps }

func (l launchd) domain() string { return "gui/" + strconv.Itoa(l.UID) }

func (l launchd) target(c ctx) string { return l.domain() + "/" + c.name }

// loaded is the service as launchd has it loaded, or nil when it is not loaded.
func (l launchd) loaded(c ctx) *Running {
	r := l.Exec("launchctl", []string{"print", l.target(c)}, nil)
	if r.Code != 0 {
		return nil
	}
	now := ParseLaunchctlPrint(r.Stdout)
	return &now
}

func (l launchd) unload(c ctx) {
	l.Exec("launchctl", []string{"bootout", l.target(c)}, nil)
	for i := 0; i < 50 && l.loaded(c) != nil; i++ {
		l.Sleep(200 * time.Millisecond)
	}
}

// load: bootstrap right after bootout can fail with EIO while launchd still tears the old job down, so it tries a few times.
func (l launchd) load(c ctx) error {
	last := ""
	for i := 0; i < 8; i++ {
		r := l.Exec("launchctl", []string{"bootstrap", l.domain(), c.file}, nil)
		if r.Code == 0 {
			return nil
		}
		last = failure(r)
		l.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("launchctl bootstrap %s failed: %s", c.file, last)
}

// refuseForeign: a label that is loaded from another file is another install's service: leave it alone.
func refuseForeign(c ctx, now *Running) error {
	if now != nil && now.Path != "" && !sameFile(now.Path, c.file) {
		return fmt.Errorf("launchd already runs a service named %s from %s, not from %s: not touching it", c.name, now.Path, c.file)
	}
	return nil
}

// kickstart starts a loaded job now; with kill, it ends the running instance first.
func (l launchd) kickstart(c ctx, kill bool) error {
	args := []string{"kickstart"}
	if kill {
		args = append(args, "-k")
	}
	if r := l.Exec("launchctl", append(args, l.target(c)), nil); r.Code != 0 {
		return fmt.Errorf("launchctl kickstart failed: %s", failure(r))
	}
	return nil
}

func (l launchd) install(c ctx) ([]string, error) {
	if err := os.MkdirAll(path.Dir(c.layout.LogFile), 0o777); err != nil {
		return nil, err
	}
	workDir := c.workDir
	if workDir == "" {
		workDir = c.home
	}
	text := Plist(PlistInput{Label: c.name, Launcher: c.layout.Launcher, WorkDir: workDir, LogFile: c.layout.LogFile, Env: ServiceEnv(c.env)})
	now := l.loaded(c)
	if err := refuseForeign(c, now); err != nil {
		return nil, err
	}
	if err := l.writeAtomic(c.file, text); err != nil {
		return nil, err
	}
	l.Exec("launchctl", []string{"enable", l.target(c)}, nil)
	if now != nil {
		l.unload(c)
	}
	if err := l.load(c); err != nil {
		return nil, err
	}
	return []string{fmt.Sprintf("started the launchd agent %s (%s); it starts at login and restarts when the daemon exits", c.name, c.file)}, nil
}

func (l launchd) uninstall(c ctx) error {
	if now := l.loaded(c); now != nil && (now.Path == "" || sameFile(now.Path, c.file)) {
		l.unload(c)
	}
	return removeFile(c.file)
}

func (l launchd) status(c ctx) *Running { return l.loaded(c) }

func (l launchd) start(c ctx) error {
	now := l.loaded(c)
	if now == nil {
		if _, err := os.Stat(c.file); err != nil {
			return fmt.Errorf("%s is not installed: run agentd service install", c.name)
		}
		return l.load(c)
	}
	if err := refuseForeign(c, now); err != nil {
		return err
	}
	if !now.Running {
		return l.kickstart(c, false)
	}
	return nil
}

func (l launchd) restart(c ctx) error {
	now := l.loaded(c)
	if now == nil {
		return l.start(c)
	}
	if err := refuseForeign(c, now); err != nil {
		return err
	}
	return l.kickstart(c, true)
}
