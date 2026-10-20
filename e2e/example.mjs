// The whole product, through a real browser: an app built only on sdk/goserver and the page SDK (examples/minimal-app),
// the SDK built from this tree, a daemon installed by the install.sh of the (local stand-in for the) GitHub release,
// paired with the app, answering an agent, and updated from the release server. Nothing is mocked except the agent (a fake
// ACP agent that answers `initialize`) and the native dialog (a program that exits 0, MINDCHAINS_CONSENT's counterpart).
const task = await taskSpace("agentd e2e: example __RUN__");
const page = task.page("p1");
const q = (id) => `css:[data-testid=${id}]`;
const failures = [];
const check = (name, cond, extra = "") => { console.log((cond ? "PASS " : "FAIL ") + name + (extra ? " " + extra : "")); if (!cond) failures.push(name); };
const text = (id) => page.evaluate((i) => document.querySelector(`[data-testid=${i}]`)?.textContent.trim() ?? "", id);
const exists = (id) => page.evaluate((i) => !!document.querySelector(`[data-testid=${i}]`), id);
const visible = (id) => page.evaluate((i) => { const e = document.querySelector(`[data-testid=${i}]`); return !!e && !e.hidden && !e.closest("[hidden]"); }, id);
const rows = () => page.evaluate(() => [...document.querySelectorAll("[data-testid=daemon]")].map((e) => e.textContent.trim()));
const waitRow = (pred, what, timeout = 40000) => page.waitForFunction((src) => [...document.querySelectorAll("[data-testid=daemon]")].some((e) => new Function("t", `return (${src})(t)`)(e.textContent.trim())), pred.toString(), { timeout }).catch(async (e) => { console.log(`WAITING FOR ${what} FAILED; rows:`, JSON.stringify(await rows())); throw e; });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const refocus = () => page.evaluate(() => window.dispatchEvent(new Event("focus")));

await page.goto("__APP_URL__/");
await page.waitForSelector(q("daemons"), { state: "attached" });
check("with no daemon anywhere the page offers the install command and no local connection", (await visible("install-start")) && !(await visible("local")));
const href = (id) => page.evaluate((i) => document.querySelector(`[data-testid=${i}]`)?.href ?? "", id);
const dmgs = [await href("download-arm64"), await href("download-amd64")];
check("and agentd.app for both Macs, from the release", (await visible("download")) && dmgs[0] === "__AGENTD_URL__/latest/download/agentd-darwin-arm64.dmg" && dmgs[1] === "__AGENTD_URL__/latest/download/agentd-darwin-amd64.dmg", dmgs.join(" "));

// --- A: a daemon already runs on this computer, unpaired: the page offers to connect it, the user answers its dialog
const tarball = _fs.readdirSync("__DIST__/download/v0.0.1").find((f) => f.endsWith(".tgz"));
const a = machine({ AGENTD_RELEASE_URL: "" });
const bundle = _path.join(a.home, "daemon");
_fs.mkdirSync(bundle);
_cp.execFileSync("tar", ["-xzf", _path.join("__DIST__/download/v0.0.1", tarball), "-C", bundle, "--strip-components=1"]);
// A file made under this browser carries its quarantine flag, and macOS kills an unnotarized binary that has one; the installer's curl sets none.
_cp.spawnSync("xattr", ["-dr", "com.apple.quarantine", bundle]);
const consent = _path.join(a.home, "consent");
_fs.writeFileSync(consent, `#!/bin/sh\necho "$1" >> ${_path.join(a.home, "asked")}\necho ${a.home}\nexit 0\n`, { mode: 0o755 });
const engine = _path.join(bundle, `agentd-${process.platform}-${process.arch === "x64" ? "amd64" : process.arch}`);
a.start(`${engine} run`, { AGENTD_CONSENT: consent });
const gatewayLine = async () => { for (let i = 0; i < 100; i++) { const m = /local gateway on 127\.0\.0\.1:(\d+)/.exec(a.output()); if (m) return m[1]; await sleep(200); } return undefined; };
check("the daemon says where its gateway listens", !!(await gatewayLine()));
await refocus();
await page.waitForSelector(q("local"), { state: "visible", timeout: 15000 });
check("the page found the daemon on this computer", await visible("local"));
await page.click(q("connect-local"));
await waitRow((t) => /: online/.test(t), "the daemon online after the local connection");
check("the dialog named this app's origin", _fs.existsSync(_path.join(a.home, "asked")) && _fs.readFileSync(_path.join(a.home, "asked"), "utf8").includes("__APP_URL__"));
check("the daemon is no longer offered as a connection", !(await visible("local")));
await page.click(q("ping"));
await page.waitForFunction(() => document.querySelector("[data-testid=ping-result]")?.textContent.includes("protocolVersion"), undefined, { timeout: 20000 });
check("the app opened an agent through sdk/goserver and read its answer", (await text("ping-result")).includes('"protocolVersion":1'), await text("ping-result"));
await a.destroy();
await waitRow((t) => /: offline/.test(t), "the daemon offline after it stopped");
check("a daemon that stopped shows offline", (await rows()).length === 1);

// --- B: no daemon here: the install command, run in a terminal, installs agentd from the release and pairs it
await page.reload();
await page.waitForSelector(q("install-start"), { state: "visible", timeout: 15000 });
await page.click(q("install-start"));
await page.waitForSelector(q("install-command"), { state: "visible", timeout: 15000 });
const command = await text("install-command");
const parts = /^curl -fsSL (\S+)\/latest\/download\/install\.sh \| bash -s -- --app (\S+) --code ([A-Za-z0-9-]{4,32})$/.exec(command);
check("the command installs from the release and carries this app's origin and the code", !!parts && parts[1] === "__AGENTD_URL__" && parts[2] === "__APP_URL__", command);
const b = machine({ AGENTD_RELEASE_URL: "__AGENTD_URL__", AGENTD_RELEASE_CHECK_MS: "500", AGENTD_RESTART: "loop" });
b.start(command.replace(" --code ", " --foreground --code "));
await waitRow((t) => /: online \(agentd 0\.0\.1\)/.test(t), "the installed daemon online");
check("the installer wrote the launcher under ~/.agentd", _fs.existsSync(_path.join(b.home, ".agentd", "bin", "agentd")));
await page.waitForFunction(() => document.querySelector("[data-testid=install]")?.hidden === true, undefined, { timeout: 15000 });
check("the pairing is spent: the install command is gone", !(await visible("install")));
await page.click(q("ping"));
await page.waitForFunction(() => /protocolVersion/.test(document.querySelector("[data-testid=ping-result]")?.textContent ?? ""), undefined, { timeout: 20000 });
check("the installed daemon answers an agent", (await text("ping-result")).includes('"protocolVersion":1'));

// --- C: the release server publishes a newer daemon; the running one takes it
const built = await run("bash __ROOT__/scripts/build-daemon.sh __DIST__", { PATH: "__PATH__", VERSION: "0.0.2", RELEASE_URL: "__AGENTD_URL__", TRAY_DIR: "__EMPTY_TRAY__" });
check("the release server has a newer daemon", built.status === 0, built.output.trim().split("\n").slice(-3).join(" | "));
await waitRow((t) => /: online \(agentd 0\.0\.2\)/.test(t), "the daemon online on 0.0.2", 90000);
check("the daemon updated itself from the release server and came back", /updated to 0\.0\.2/.test(b.output()), b.output().trim().split("\n").slice(-2).join(" | "));
await b.destroy();

if (failures.length) { console.log(`\n${failures.length} FAILED:\n- ${failures.join("\n- ")}`); throw new Error("example failed"); }
console.log("\nALL PASSED");
await task.finish({ keep: [] });
