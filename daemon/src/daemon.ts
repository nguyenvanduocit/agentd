import { mkdirSync, mkdtempSync, realpathSync, rmSync, statSync } from "node:fs"
import { hostname, tmpdir } from "node:os"
import { dirname, join, resolve } from "node:path"
import type { Hello } from "../../contract/define.acp.ts"
import { allAgentStatuses } from "./core/agents.ts"
import type { Command } from "./core/cli.ts"
import { installedVersion, layoutOf, type Layout } from "./core/layout.ts"
import { agentdHome, sessionsFile } from "./core/paths.ts"
import { controlOutcome, EXIT_RESTART, EXIT_STOPPED, isManaged } from "./core/exit.ts"
import { addBrowser, removeOrigin, selectApps, withdrawn, type App } from "./core/registry.ts"
import { releaseBase } from "./core/update.ts"
import { agentEnv, WRAPPER } from "./core/wrapper.ts"
import { AdapterStore } from "./adapters.ts"
import { AppHub, installWrapper } from "./apps.ts"
import { ask } from "./consent.ts"
import { loadRegistry, registryFile, updateRegistry } from "./config.ts"
import { gatewayPort } from "./core/local.ts"
import { startGateway, type GatewayDeps } from "./local.ts"
import { McpRelay } from "./page-mcp.ts"
import { BrowserSessions, type BrowserSessionOptions } from "./session-ws.ts"
import { startTrayMenu, trayFile } from "./tray-menu.ts"
import { pair } from "./pair.ts"
import { currentHost, findProgram } from "./host.ts"
import { refreshLauncher } from "./launcher.ts"
import { writeApp } from "./appentry.ts"
import { rotateLog, say, stampLines, stdoutIs, warn } from "./log.ts"
import { SessionState } from "./state.ts"
import { ReleaseWatcher } from "./releases.ts"
import { Updater } from "./updater.ts"
import { RELEASE_KEY, RELEASE_URL, VERSION } from "./version.ts"

type RunCommand = Extract<Command, { kind: "run" }>

const SHUTDOWN_GRACE_MS = 2000
const LOG_CHECK_MS = 60_000

function workDir(dir: string | undefined): string {
  const cwd = resolve(dir ?? process.cwd())
  if (!statSync(cwd, { throwIfNoEntry: false })?.isDirectory()) throw new Error(`--dir ${cwd} is not a directory`)
  return cwd
}

/** The layout of the installed daemon this process is, or undefined for a source run, a test bundle or an npx copy. */
function installedLayout(): Layout | undefined {
  try {
    const l = layoutOf(realpathSync(agentdHome(currentHost())))
    const script = realpathSync(process.argv[1] ?? "")
    const version = installedVersion(script, l)
    if (version === undefined) return undefined
    if (version !== VERSION) {
      warn(`updates are off: ${script} is version ${VERSION}, not ${version}`)
      return undefined
    }
    refreshLauncher(dirname(script), l)
    if (isManaged(process.env)) refreshApp(l)
    return l
  } catch {
    return undefined
  }
}

/** A daemon a service runs makes the app that starts it again after a Stop when it is missing, and keeps its icon current. */
function refreshApp(l: Layout): void {
  try {
    writeApp(l, currentHost(), VERSION, "refresh")
  } catch (e) {
    warn(`no app to start agentd again: ${e instanceof Error ? e.message : String(e)}`)
  }
}

function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void } {
  let resolve: (v: T) => void = () => undefined
  const promise = new Promise<T>((r) => {
    resolve = r
  })
  return { promise, resolve }
}

function makeUpdater(layout: Layout, server: string, idle: () => boolean, stop: (code: number) => void): Updater {
  return new Updater({
    layout,
    server,
    running: VERSION,
    releaseKey: RELEASE_KEY,
    idle,
    restart: (version) => {
      say(`restarting on ${version}`)
      stop(EXIT_RESTART)
    },
  })
}

/** How often the release server is asked for a newer daemon. */
const RELEASE_CHECK_MS = 6 * 60 * 60 * 1000

/** $AGENTD_RELEASE_CHECK_MS (a test seam, at least 50 ms) or six hours. */
function releaseCheckMs(env: Readonly<Record<string, string | undefined>>): number {
  const n = Number(env.AGENTD_RELEASE_CHECK_MS)
  return Number.isInteger(n) && n >= 50 ? n : RELEASE_CHECK_MS
}

/**
 * Keeps this daemon on the version the release server offers, when it is an installed daemon and there is a release server:
 * asks at start and every few hours, and hands each offer to the updater. Returns what ends it.
 */
function beginUpdates(layout: Layout | undefined, idle: () => boolean, stop: (code: number) => void): () => void {
  if (layout === undefined) return () => undefined
  const base = releaseBase(process.env, RELEASE_URL)
  if (base === undefined) {
    say("updates are off: there is no release server (AGENTD_RELEASE_URL is empty)")
    return () => undefined
  }
  const updater = makeUpdater(layout, base, idle, stop)
  const watcher = new ReleaseWatcher({ base, intervalMs: releaseCheckMs(process.env), onOffer: (offer) => updater.offer(offer) })
  watcher.start()
  return () => {
    watcher.stop()
    updater.stop()
  }
}

/**
 * The directory this run's MCP wrappers live in: `<tmp>/agentd-XXXXXX/bin` (SessionState removes leftovers of that
 * name). `root` is the daemon's own (mkdtemp makes it 0700): the socket of the pages' bridges goes there too.
 */
function makeTempDirs(): { root: string; bin: string } {
  const root = mkdtempSync(join(tmpdir(), "agentd-"))
  const bin = join(root, "bin")
  mkdirSync(bin)
  return { root, bin }
}

/** The socket of the pages' `agentd-mcp --page` bridges; without it pages run agents but serve them no tools. */
async function listenForBridges(root: string): Promise<McpRelay | undefined> {
  try {
    return await McpRelay.listen(join(root, "mcp.sock"))
  } catch (e) {
    warn(`pages cannot serve MCP tools to their agents: ${e instanceof Error ? e.message : String(e)}`)
    return undefined
  }
}

/** The pages' agents: their own wrapper (`bin/page/agentd-mcp`, which runs `mcp --page`) and the bridges' socket. */
async function makeBrowserSessions(root: string, bin: string, fleet: Omit<BrowserSessionOptions["fleet"], "binDir">, hello: () => Hello): Promise<BrowserSessions> {
  const binDir = installWrapper(join(bin, "page"), ["--page"])
  return new BrowserSessions({ fleet: { ...fleet, binDir }, hello, relay: await listenForBridges(root), bridgeCommand: join(binDir, WRAPPER) })
}

/** The layout updates and log rotation work in, or undefined (said in the log) for a daemon that is not installed. */
function startUpdates(): Layout | undefined {
  const layout = installedLayout()
  if (layout) keepLogSmall(layout)
  else say("updates are off: this daemon is not installed under ~/.agentd/daemon (the installer does that)")
  return layout
}

/** Keeps the log file of a service under its size limit: only when this process writes its stdout into that file. */
function keepLogSmall(l: Layout): void {
  const check = () => {
    if (stdoutIs(l.logFile)) rotateLog(l.logFile)
  }
  check()
  setInterval(check, LOG_CHECK_MS).unref()
}

function makeHello(env: Record<string, string>, adapters: AdapterStore): Hello {
  return {
    version: VERSION,
    hostname: hostname(),
    os: process.platform,
    arch: process.arch,
    agents: allAgentStatuses(env, (p) => findProgram(p, env), adapters.state),
    managed: isManaged(process.env),
  }
}

/** Settles with the exit code; SIGTERM and SIGINT settle it with 0. */
function stopSignal(): { promise: Promise<number>; resolve: (code: number) => void } {
  const stop = deferred<number>()
  for (const sig of ["SIGTERM", "SIGINT"] as const) process.once(sig, () => stop.resolve(EXIT_STOPPED))
  return stop
}

function restartRequested(server: string, managed: boolean, sessions: number, stop: (code: number) => void): void {
  const outcome = controlOutcome("restart", managed)
  if (outcome.kind === "ignore") {
    say(outcome.why)
    return
  }
  say(`restart requested by the server ${server}: ending ${sessions} agent(s), exiting ${outcome.code}`)
  stop(outcome.code)
}

function announce(gatewayPort: number | undefined, cwd: string, apps: number): void {
  say(gatewayPort === undefined ? "local gateway off: the ports 47615-47624 are taken; apps already paired still work" : `local gateway on 127.0.0.1:${gatewayPort}`)
  say(`agentd ${VERSION} (pid ${process.pid}) serving agents in ${cwd} for ${apps} app(s)`)
}

/** A page that may still ask to connect keeps the daemon up; without one an app that left has nothing to wait for. */
function stopUnlessPagesMayConnect(file: string, code: number, stop: (code: number) => void): void {
  loadRegistry(file).then(
    (r) => {
      if (r.browsers.length > 0) say("the last app left; staying up for the web pages that may connect")
      else stop(code)
    },
    () => stop(code),
  )
}

/** What the local gateway does on this daemon: the registry file is the truth, the hub the running apps. */
function gatewayDeps(file: string, hub: AppHub, browsers: BrowserSessions, hello: () => Hello): GatewayDeps {
  return {
    upgrade: (origin, req, socket, head) => browsers.upgrade(origin, req, socket, head),
    version: VERSION,
    agents: () => hello().agents,
    registry: () => loadRegistry(file),
    ask: (q) => ask(q, process.env),
    linked: (server) => hub.has(server),
    connectServer: async (server, code) => {
      const app = await pair(server, code, file)
      // Pairing again replaced the saved token: the running link, if there is one, still holds the old one.
      await hub.detach(server, true)
      hub.add(app)
    },
    grantBrowser: async (origin) => {
      await updateRegistry(file, (r) => addBrowser(r, origin, new Date().toISOString()))
    },
    revoke: (origin) => disconnectOrigin(file, hub, browsers, origin),
  }
}

/** How often the daemon reads its registry for an origin that another process took away. */
const REGISTRY_CHECK_MS = 2000

/**
 * Ends what still runs for an origin that another process removed from the registry (`agentd apps remove`): its link
 * and its pages' sockets, as Disconnect in the menu does. The daemon stays up. Returns what stops the checks.
 */
function followRegistry(file: string, hub: AppHub, browsers: BrowserSessions): () => void {
  let timer: ReturnType<typeof setTimeout> | undefined
  const check = async () => {
    try {
      // What runs is taken before the file is read: an app or a page is saved before it is linked, so one linked by
      // then is in the file unless it was removed, and one paired while the file is read is not judged on old content.
      const linked = hub.servers()
      const pages = browsers.origins()
      const gone = withdrawn(await loadRegistry(file), linked, pages)
      for (const origin of gone.pages) {
        say(`${origin} was disconnected: ending its pages' sessions`)
        browsers.endOrigin(origin)
      }
      for (const server of gone.apps) {
        say(`${server} was disconnected: ending its link and sessions`)
        await hub.detach(server, true)
      }
    } catch (e) {
      warn(`cannot read ${file}: ${e instanceof Error ? e.message : String(e)}`)
    }
    timer = setTimeout(() => void check(), REGISTRY_CHECK_MS)
    timer.unref()
  }
  timer = setTimeout(() => void check(), REGISTRY_CHECK_MS)
  timer.unref()
  return () => clearTimeout(timer)
}

/** Forgets the app and the browser grant of `origin` and ends their sessions; the daemon stays up so it can be connected again. */
async function disconnectOrigin(file: string, hub: AppHub, browsers: BrowserSessions, origin: string): Promise<void> {
  await updateRegistry(file, (r) => removeOrigin(r, origin))
  browsers.endOrigin(origin)
  await hub.detach(origin, true)
}

function connectApps(apps: App[], hub: AppHub, adapters: AdapterStore, hello: () => Hello): void {
  for (const app of apps) hub.add(app)
  adapters.onChange(() => hub.agentsChanged(hello().agents))
}

interface TrayContext {
  file: string
  hub: AppHub
  browsers: BrowserSessions
  hello: () => Hello
  gatewayPort: number | undefined
  layout: Layout | undefined
  stop: (code: number) => void
}

/** The icon in the menu bar, when this platform has one; returns what ends it. */
function beginTray(c: TrayContext): () => void {
  const trayProgram = trayFile(process.env)
  if (trayProgram === undefined) return () => undefined
  return startTrayMenu({
    file: trayProgram,
    registryFile: c.file,
    hello: c.hello,
    linked: (server) => c.hub.has(server),
    sessions: () => c.hub.size + c.browsers.size,
    gatewayPort: c.gatewayPort,
    logFile: c.layout?.logFile,
    stop: c.stop,
    restart: () => restartRequested("the tray menu", c.hello().managed, c.hub.size + c.browsers.size, c.stop),
    disconnect: (origin) => disconnectOrigin(c.file, c.hub, c.browsers, origin),
  })
}

/** What runs beside the apps: the icon in the menu bar, the updates and the registry checks; returns what ends them. */
function beginExtras(c: TrayContext): () => void {
  const stopTray = beginTray(c)
  const stopUpdates = beginUpdates(c.layout, () => c.hub.size + c.browsers.size === 0, c.stop)
  const stopFollowing = followRegistry(c.file, c.hub, c.browsers)
  return () => {
    stopFollowing()
    stopUpdates()
    stopTray()
  }
}

/** Runs the daemon until it is stopped (SIGTERM, SIGINT), left by its last app or updated; resolves to the exit code. */
export async function runDaemon(cmd: RunCommand): Promise<number> {
  stampLines()
  const host = currentHost()
  const file = registryFile(cmd, host)
  const registry = await loadRegistry(file)
  const apps = selectApps(registry, cmd.app)
  const cwd = workDir(cmd.dir)
  const { root, bin } = makeTempDirs()
  const state = new SessionState(sessionsFile(host), bin)
  await state.start()

  const env = agentEnv(process.env, bin, process.platform)
  // The adapters install in the background from now on, whatever the servers do with their sessions.
  const adapters = new AdapterStore({ layout: layoutOf(agentdHome(host)), env: process.env })
  adapters.start()
  const hello = () => makeHello(env, adapters)
  const stop = stopSignal()

  const layout = startUpdates()
  const browsers = await makeBrowserSessions(root, bin, { cwd, env: process.env, state, adapter: adapters.state }, hello)
  const hub: AppHub = new AppHub({
    fleet: { cwd, env: process.env, state, adapter: adapters.state },
    wrapperRoot: bin,
    configFile: file,
    hello,
    onEmpty: (code) => stopUnlessPagesMayConnect(file, code, stop.resolve),
    onRestart: (server) => restartRequested(server, hello().managed, hub.size, stop.resolve),
  })
  connectApps(apps, hub, adapters, hello)
  const gateway = await startGateway(gatewayDeps(file, hub, browsers, hello), gatewayPort(process.env))
  announce(gateway?.port, cwd, apps.length)
  const stopExtras = beginExtras({ file, hub, browsers, hello, gatewayPort: gateway?.port, layout, stop: stop.resolve })

  const code = await stop.promise
  adapters.stop()
  stopExtras()
  await browsers.shutdown(SHUTDOWN_GRACE_MS)
  await gateway?.close()
  await hub.shutdown(SHUTDOWN_GRACE_MS)
  state.clear()
  rmSync(root, { recursive: true, force: true })
  return code
}
