import * as _cp from "node:child_process";
import * as _fs from "node:fs";
import * as _path from "node:path";

// A machine: its own HOME and XDG directories (config and state), so no scenario touches the real ones; the HOME is
// under the run's directory, which scripts/e2e.sh removes however the run ends. The agent is the fake ACP agent (it
// answers `initialize`), and the machine has no adapters to install.
// On macOS every file a process under this browser writes is marked quarantined, and macOS kills an unnotarized binary that
// carries the mark (the Go engine, when a process of this browser wrote it). A job launchd starts itself writes no mark, so
// on macOS a machine's process is a launchd job (stopped with `launchctl remove`, which ends its whole tree).
const _viaLaunchd = process.platform === "darwin";
let _jobs = 0;
const _launchctl = (...args) => _cp.spawnSync("launchctl", args, { encoding: "utf8" });
const _daemons = new Set();
const _killDaemons = () => {
  for (const d of _daemons) {
    if (d.job) _launchctl("remove", d.job);
    else { try { process.kill(-d.child.pid, "SIGKILL"); } catch {} }
  }
};
process.on("exit", _killDaemons);

const machine = (extraEnv = {}) => {
  const home = _fs.mkdtempSync(_path.join("__WORK__", "machines", "agentd-e2e-__RUN__-"));
  const dir = _path.join(home, "work");
  _fs.mkdirSync(dir);
  const env = {
    ...process.env, PATH: "__PATH__", HOME: home, XDG_CONFIG_HOME: _path.join(home, ".config"), XDG_STATE_HOME: _path.join(home, ".local", "state"),
    npm_config_update_notifier: "false", npm_config_fund: "false", npm_config_audit: "false",
    // AGENTD_LOCAL_PORT: the first of the ten ports of this run (scripts/e2e.sh), where the example's page looks; the
    // machines of a run walk up from it together, so a daemon of the user on 47615 is never the one the page finds.
    AGENTD_LOCAL_PORT: "__GATEWAY_BASE__",
    AGENTD_AGENT_CLAUDE: "__FAKE_AGENT__", AGENTD_AGENT_CODEX: "/usr/bin/false", AGENTD_ADAPTERS: "off", ...extraEnv,
  };
  let runs = 0;
  const m = {
    home, dir, env, child: null,
    // Starts a command line in its own process group (a launchd job on macOS); stop() ends all of it.
    start(cmd, more = {}) {
      const log = _path.join(home, `daemon-${++runs}.log`);
      m.log = log;
      if (_viaLaunchd) {
        m.job = `agentd-e2e-__RUN__-${++_jobs}`;
        const vars = Object.entries({ ...env, ...more }).map(([k, v]) => `${k}=${v}`);
        const r = _launchctl("submit", "-l", m.job, "-o", log, "-e", log, "--", "/usr/bin/env", "-i", ...vars, "/bin/bash", "-c", `cd '${dir}' && ${cmd}`);
        if (r.status !== 0) throw new Error(`launchctl submit failed: ${r.stderr}`);
        _daemons.add(m);
        return;
      }
      const fd = _fs.openSync(log, "a");
      m.child = _cp.spawn("bash", ["-c", cmd], { cwd: dir, env: { ...env, ...more }, detached: true, stdio: ["ignore", fd, fd] });
      m.exited = new Promise((resolve) => m.child.once("exit", (code, signal) => resolve({ code, signal })));
      _daemons.add(m);
    },
    output: () => (m.log && _fs.existsSync(m.log) ? _fs.readFileSync(m.log, "utf8") : ""),
    alive: () => (m.job ? /"PID" = /.test(_launchctl("list", m.job).stdout) : !!m.child && m.child.exitCode === null && m.child.signalCode === null),
    async stop(signal = "SIGTERM", graceMs = 10000) {
      if (m.job) { _launchctl("remove", m.job); return { code: null, signal }; }
      if (!m.alive()) return m.exited;
      try { process.kill(-m.child.pid, signal); } catch {}
      const ended = await Promise.race([m.exited, new Promise((r) => setTimeout(r, graceMs, null))]);
      if (ended) return ended;
      try { process.kill(-m.child.pid, "SIGKILL"); } catch {}
      return m.exited;
    },
    async destroy() { await m.stop(); _daemons.delete(m); _fs.rmSync(home, { recursive: true, force: true }); },
  };
  return m;
};
