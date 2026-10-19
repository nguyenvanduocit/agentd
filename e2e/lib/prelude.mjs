import { execFileSync } from "node:child_process";
import * as _cp from "node:child_process";
import * as _fs from "node:fs";
import * as _path from "node:path";

// A machine: its own HOME and XDG directories (config and state), so no scenario touches the real ones; the HOME is
// under the run's directory, which scripts/e2e.sh removes however the run ends. The agent is the fake ACP agent (it
// answers `initialize`), and the machine has no tray and no adapters to install.
const _daemons = new Set();
const _killDaemons = () => { for (const d of _daemons) { try { process.kill(-d.child.pid, "SIGKILL"); } catch {} } };
process.on("exit", _killDaemons);

const machine = (extraEnv = {}) => {
  const home = _fs.mkdtempSync(_path.join("__WORK__", "machines", "agentd-e2e-__RUN__-"));
  const dir = _path.join(home, "work");
  _fs.mkdirSync(dir);
  const env = {
    ...process.env, PATH: "__PATH__", HOME: home, XDG_CONFIG_HOME: _path.join(home, ".config"), XDG_STATE_HOME: _path.join(home, ".local", "state"),
    npm_config_update_notifier: "false", npm_config_fund: "false", npm_config_audit: "false",
    AGENTD_AGENT_CLAUDE: "__FAKE_AGENT__", AGENTD_AGENT_CODEX: "/usr/bin/false", AGENTD_ADAPTERS: "off", AGENTD_TRAY: "off", ...extraEnv,
  };
  let runs = 0;
  const m = {
    home, dir, env, child: null,
    // Starts a command line in its own process group; stop() ends all of it.
    start(cmd, more = {}) {
      const log = _path.join(home, `daemon-${++runs}.log`);
      const fd = _fs.openSync(log, "a");
      m.log = log;
      m.child = _cp.spawn("bash", ["-c", cmd], { cwd: dir, env: { ...env, ...more }, detached: true, stdio: ["ignore", fd, fd] });
      m.exited = new Promise((resolve) => m.child.once("exit", (code, signal) => resolve({ code, signal })));
      _daemons.add(m);
    },
    output: () => (m.log && _fs.existsSync(m.log) ? _fs.readFileSync(m.log, "utf8") : ""),
    alive: () => !!m.child && m.child.exitCode === null && m.child.signalCode === null,
    async stop(signal = "SIGTERM", graceMs = 10000) {
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
