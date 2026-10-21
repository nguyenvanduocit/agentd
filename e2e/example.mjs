// The whole product, through a real browser: an app built only on sdk/goserver and the page SDK (examples/minimal-app),
// the SDK built from this tree, and an engine the page pairs and talks to. Nothing is mocked except the agent (a fake
// ACP agent that answers `initialize`) and the consent answer (AGENTD_CONSENT, a program that exits 0).
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
// E2E_SHOTS=<dir> scripts/e2e.sh saves what the page shows at each key moment; without it this does nothing.
const shotsDir = "__SHOTS__";
const shot = async (name) => { if (shotsDir) await page.screenshot({ path: `${shotsDir}/${name}.png` }); };
// Helpers for what a person sees and does. None of them throws on a missing element: a check reports FAIL instead.
const waitVisible = (id, ms = 6000) => page.waitForFunction((i) => { const e = document.querySelector(`[data-testid=${i}]`); return !!e && !e.hidden && !e.closest("[hidden]"); }, id, { timeout: ms }).then(() => true, () => false);
const waitGone = (id, ms = 6000) => page.waitForFunction((i) => { const e = document.querySelector(`[data-testid=${i}]`); return !e || e.hidden || !!e.closest("[hidden]"); }, id, { timeout: ms }).then(() => true, () => false);
const tryClick = async (id) => { if (!(await exists(id))) return false; try { await page.click(q(id)); return true; } catch { return false; } };
const attr = (id, name) => page.evaluate(([i, n]) => document.querySelector(`[data-testid=${i}]`)?.getAttribute(n) ?? null, [id, name]);
const style = (id, prop) => page.evaluate(([i, p]) => { const e = document.querySelector(`[data-testid=${i}]`); return e ? getComputedStyle(e)[p] : null; }, [id, prop]);
const daemonsJson = () => page.evaluate(async () => (await (await fetch("/api/daemons")).json()).daemons);
const rename = (id, name) => page.evaluate(([i, n]) => fetch(`/api/daemons/${i}/name`, { method: "PUT", body: JSON.stringify({ name: n }) }).then((r) => r.status), [id, name]);
const hasHorizontalScroll = () => page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth + 1);
// What a person reads must say agentd, this computer, app: never daemon, engine, origin or server. Computer names are
// theirs (a host may be called build-server-1), so they are taken out before looking.
const BANNED = /\b(daemons?|engines?|origins?|servers?)\b/i;
const vocab = async (where) => {
  let seen = await page.evaluate(() => document.body.innerText);
  const theirs = await page.evaluate(() => [...document.querySelectorAll("[data-testid=daemon-name],[data-testid=daemon-host]")].map((e) => e.textContent.trim()).filter(Boolean));
  for (const t of theirs) seen = seen.split(t).join("");
  const hit = BANNED.exec(seen);
  check(`${where}: the page says agentd, this computer, app`, !hit, hit ? `"...${seen.slice(Math.max(0, hit.index - 30), hit.index + 30).replace(/\s+/g, " ")}..."` : "");
};
// The contrast ratio (WCAG) of an element's text against what is behind it.
const contrastOf = (id) => page.evaluate((i) => {
  const el = document.querySelector(`[data-testid=${i}]`);
  if (!el) return 0;
  const parse = (c) => { const m = c.match(/[\d.]+/g).map(Number); return { r: m[0], g: m[1], b: m[2], a: m[3] ?? 1 }; };
  const lum = ({ r, g, b }) => { const f = (v) => { v /= 255; return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4; }; return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b); };
  let bg = { r: 255, g: 255, b: 255, a: 1 };
  for (let n = el; n; n = n.parentElement) { const c = parse(getComputedStyle(n).backgroundColor); if (c.a > 0) { bg = c; break; } }
  const [hi, lo] = [lum(parse(getComputedStyle(el).color)), lum(bg)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}, id);

await page.goto("__APP_URL__/");
await page.waitForSelector(q("daemons"), { state: "attached" });
check("with no daemon anywhere the page offers the Mac app and no local connection", (await visible("download")) && !(await visible("local")) && !(await exists("install-command")));
const href = (id) => page.evaluate((i) => document.querySelector(`[data-testid=${i}]`)?.href ?? "", id);
const dmgs = [await href("download-arm64"), await href("download-amd64")];
await shot("01-absent");
// An empty list says so and what to do, instead of a bare heading.
const emptyText = await text("daemons-empty");
check("an empty list of computers says so and what to do", (await visible("daemons-empty")) && /^No computer is connected yet\. Install agentd above/.test(emptyText), emptyText);
await vocab("with no agentd anywhere");
// A screen reader hears the page's status change: the note and the empty line politely, an error at once.
const live = async (id) => (await attr(id, "aria-live")) ?? (await attr(id, "role"));
check("the status note, the error and the list are announced to a screen reader", ["status", "polite"].includes(await live("local-note")) && ["alert", "assertive"].includes(await live("local-error")) && ["status", "polite"].includes(await live("daemons")), `${await live("local-note")} / ${await live("local-error")} / ${await live("daemons")}`);
// Room between neighbours: a button is not glued to the links or the divider above it.
const gapBelow = (above, below) => page.evaluate(([a, b]) => { const x = document.querySelector(`[data-testid=${a}]`), y = document.querySelector(`[data-testid=${b}]`); return x && y ? y.getBoundingClientRect().top - x.getBoundingClientRect().bottom : -1; }, [above, below]);
const contrast = await contrastOf("daemons-empty");
check("the muted text keeps a contrast of 4.5 to 1 against the page (WCAG AA)", contrast >= 4.5, contrast.toFixed(2));
check("an empty agent answer draws no grey box", (await page.evaluate(() => getComputedStyle(document.querySelector("[data-testid=ping-result]")).display)) === "none");
check("and agentd.app for both Macs, from the release", (await visible("download")) && dmgs[0] === "__AGENTD_URL__/latest/download/agentd-darwin-arm64.dmg" && dmgs[1] === "__AGENTD_URL__/latest/download/agentd-darwin-amd64.dmg", dmgs.join(" "));

// --- A: a daemon already runs on this computer, unpaired: the page offers to connect it, the user answers its dialog
const a = machine();
const consent = _path.join(a.home, "consent");
_fs.writeFileSync(consent, `#!/bin/sh\necho "$1" >> ${_path.join(a.home, "asked")}\nsleep 2\necho ${a.home}\nexit 0\n`, { mode: 0o755 });
a.start("__ENGINE__ run", { AGENTD_CONSENT: consent });
const gatewayLine = async () => { for (let i = 0; i < 100; i++) { const m = /local gateway on 127\.0\.0\.1:(\d+)/.exec(a.output()); if (m) return m[1]; await sleep(200); } return undefined; };
check("the daemon says where its gateway listens", !!(await gatewayLine()));
await refocus();
await page.waitForSelector(q("local"), { state: "visible", timeout: 15000 });
check("the page found the daemon on this computer", await visible("local"));
await shot("02-available");
await vocab("when agentd runs on this computer");
// The keyboard path: Tab reaches the primary button, it shows where the focus is, Enter presses it.
await page.evaluate(() => { if (document.activeElement && document.activeElement !== document.body) document.activeElement.blur(); });
let reached = false;
for (let i = 0; i < 12 && !reached; i++) { await page.keyboard.press("Tab"); reached = await page.evaluate(() => document.activeElement?.dataset?.testid === "connect-local"); }
check("Tab reaches the button that connects this computer", reached);
const ring = await page.evaluate(() => { const s = getComputedStyle(document.activeElement); return { style: s.outlineStyle, width: parseFloat(s.outlineWidth) }; });
check("the focused button shows a focus ring of at least 2 px", ring.style !== "none" && ring.width >= 2, JSON.stringify(ring));
if (reached) await page.keyboard.press("Enter"); else await page.click(q("connect-local"));
// The dialog is open (the seam waits two seconds): the page says so, and the button cannot be pressed twice.
await page.waitForFunction(() => document.querySelector("[data-testid=connect-local]")?.disabled === true, undefined, { timeout: 5000 });
check("while the dialog waits the button is off and the page says to answer it", (await text("local-note")).includes("Answer the dialog"), await text("local-note"));
await shot("03-connecting");
await vocab("while the dialog waits");
await waitRow((t) => /: online/.test(t), "the daemon online after the local connection");
await shot("04-connected");
check("the dialog named this app's origin", _fs.existsSync(_path.join(a.home, "asked")) && _fs.readFileSync(_path.join(a.home, "asked"), "utf8").includes("__APP_URL__"));
check("the daemon is no longer offered as a connection", !(await visible("local")));
await vocab("with a computer connected");

// --- A computer's row: its name first, the host name muted and cut with an ellipsis (whole in a tooltip), a remove
// button that names its target. The name is renamed through the app's own route so that it differs from the host name.
const mine = (await daemonsJson())[0];
check("renaming a computer through the app's route works", (await rename(mine.id, "laptop")) === 200);
await waitRow((t) => t.startsWith("laptop: online"), "the renamed computer");
const hostShown = await waitVisible("daemon-host");
check("the host name sits next to the name, muted, cut with an ellipsis and whole in a tooltip",
  hostShown && (await attr("daemon-host", "title")) === mine.hostname && (await style("daemon-host", "textOverflow")) === "ellipsis" && (await style("daemon-host", "whiteSpace")) === "nowrap",
  `${mine.hostname} / ${await attr("daemon-host", "title")} / ${await style("daemon-host", "textOverflow")} ${await style("daemon-host", "whiteSpace")}`);
check("the remove button names the computer it removes", (await attr("daemon-remove", "aria-label")) === "Remove laptop", String(await attr("daemon-remove", "aria-label")));
const listGap = await gapBelow("daemons", "ping");
check("the button that asks the agent has room below the list (8 px)", listGap >= 8, `${listGap.toFixed(1)} px`);
// The list refreshes every second; a button the keyboard is on must still be there afterwards.
if (await exists("daemon-remove")) await page.focus(q("daemon-remove"));
await sleep(2600);
check("keyboard focus stays on the remove button while the list refreshes", await page.evaluate(() => document.activeElement?.dataset?.testid === "daemon-remove"));
await page.evaluate(() => document.activeElement?.blur?.());
// A narrow screen: a 64-character name (no break in it) must not push the page sideways.
await page.cdp("Emulation.setDeviceMetricsOverride", { width: 360, height: 800, deviceScaleFactor: 1, mobile: false });
await rename(mine.id, "abcdefghij".repeat(6) + "abcd");
await waitRow((t) => t.startsWith("abcdefghij"), "the long name");
check("at 360 px wide a 64-character name does not push the page sideways", !(await hasHorizontalScroll()));
await shot("10-narrow-360");
await rename(mine.id, "laptop");
await waitRow((t) => t.startsWith("laptop: online"), "the name back");
await page.cdp("Emulation.clearDeviceMetricsOverride", {});
await page.click(q("ping"));
await page.waitForFunction(() => document.querySelector("[data-testid=ping-result]")?.textContent.includes("protocolVersion"), undefined, { timeout: 20000 });
await shot("05-ping");
check("the app opened an agent through sdk/goserver and read its answer", (await text("ping-result")).includes('"protocolVersion":1'), await text("ping-result"));
await a.destroy();
await waitRow((t) => /: offline/.test(t), "the daemon offline after it stopped");
await shot("06-offline");
check("a daemon that stopped shows offline", (await rows()).length === 1);
check("an agent's answer does not outlive the computer it came from", (await text("ping-result")) === "", await text("ping-result"));
await vocab("with a computer offline");

// A removal that fails is told in words, while the computer is still in the list. The page's own words replace the
// server's for "already gone", and any other failure shows the server's text after what was being done. The answers
// are stubbed so that each case is certain: the app's route itself is covered by examples/minimal-app's Go tests.
await page.evaluate(() => { window.__realFetch = window.fetch; });
const failDelete = (status, error) => page.evaluate(([s, e]) => { window.fetch = (u, o) => (o?.method === "DELETE" && String(u).startsWith("/api/daemons/")) ? Promise.resolve(new Response(JSON.stringify({ error: e }), { status: s, headers: { "content-type": "application/json" } })) : window.__realFetch(u, o); }, [status, error]);
const removeAndSeeError = async () => {
  await tryClick("daemon-remove"); await waitVisible("daemon-confirm"); await tryClick("daemon-remove-confirm");
  const told = await page.waitForFunction(() => (document.querySelector("[data-testid=daemons-error]")?.textContent ?? "") !== "", undefined, { timeout: 6000 }).then(() => true, () => false);
  return told ? await text("daemons-error") : "";
};
await failDelete(404, "The computer chosen is no longer paired: pick another");
const goneText = await removeAndSeeError();
check("removing a computer that is already gone says so in plain words", /already gone/.test(goneText) && !BANNED.test(goneText), goneText);
await failDelete(500, "disk full");
const failText = await removeAndSeeError();
check("any other failure of Remove says what was being done and why", /^Could not remove .+: disk full$/.test(failText), failText);
await shot("13-remove-error");
await page.evaluate(() => { window.fetch = window.__realFetch; });

// --- Remove: it asks first, in words; Keep leaves everything as it was; Remove removes the computer; the empty line returns
const clicked = await tryClick("daemon-remove");
const asked = await waitVisible("daemon-confirm");
const askText = asked ? await text("daemon-confirm") : "";
check("Remove asks first, naming the computer and what follows", clicked && asked && askText.includes("Remove laptop? It will have to be paired again."), askText);
await shot("11-confirm-remove");
const kept = await tryClick("daemon-keep");
check("Keep dismisses the question and leaves the computer in the list", asked && kept && (await waitGone("daemon-confirm")) && (await rows()).length === 1);
await tryClick("daemon-remove");
await waitVisible("daemon-confirm");
await tryClick("daemon-remove-confirm");
const removed = await page.waitForFunction(() => document.querySelectorAll("[data-testid=daemon]").length === 0, undefined, { timeout: 8000 }).then(() => true, () => false);
check("Remove takes the computer out of the list", removed, JSON.stringify(await rows()));
check("and the empty line is back", await waitVisible("daemons-empty"));
await shot("12-removed");

if (failures.length) { console.log(`\n${failures.length} FAILED:\n- ${failures.join("\n- ")}`); throw new Error("example failed"); }
console.log("\nALL PASSED");
await task.finish({ keep: [] });
