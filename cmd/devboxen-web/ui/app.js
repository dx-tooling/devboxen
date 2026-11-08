// devboxen: the overview of every host's boxes, and a box's workspace (VS Code, the box's
// browser or Claude's screen | terminal).
// Routes (the hash): #/ the overview, #/<host>/<box>/<session> a box with a tmux session.
"use strict";

const $ = (id) => document.getElementById(id);
const NAME = /^[a-z0-9][a-z0-9-]{0,10}$/;
const SESSION = /^[A-Za-z0-9_-]{1,32}$/;
const NARROW = window.matchMedia("(max-width: 820px)");

const store = {
  get(key, fallback) {
    try { return localStorage.getItem("devboxen." + key) ?? fallback; } catch { return fallback; }
  },
  set(key, value) {
    try { localStorage.setItem("devboxen." + key, value); } catch { /* private mode */ }
  },
};

let hosts = [];        // the last /api/hosts answer
let loaded = false;
let current = null;    // {host, box, session} while a box is open
let codeSrc = "";

function route() {
  const parts = location.hash.replace(/^#\/?/, "").split("/").filter(Boolean).map(decodeURIComponent);
  if (parts.length >= 2 && NAME.test(parts[0]) && NAME.test(parts[1])) {
    const session = parts[2] && SESSION.test(parts[2]) ? parts[2] : "shell";
    return { host: parts[0], box: parts[1], session };
  }
  return null;
}

function href(host, box, session) {
  return "#/" + [host, box, session].filter(Boolean).map(encodeURIComponent).join("/");
}

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v === undefined || v === null || v === false) continue;
    if (k === "class") node.className = v;
    else if (k === "text") node.textContent = v;
    else node.setAttribute(k, v === true ? "" : v);
  }
  for (const c of children.flat()) if (c != null) node.append(c);
  return node;
}

function sessionChip(host, box, s, isCurrent) {
  const label = s.command && s.command !== s.name ? el("span", { class: "cmd", text: s.command }) : null;
  return el("a", {
    class: "chip" + (isCurrent ? " current" : ""),
    href: href(host, box, s.name),
    title: s.attached ? `${s.name}: a terminal is attached` : `${s.name}: no terminal attached`,
  }, el("span", { class: "dot" + (s.attached ? " on" : "") }), s.name, label);
}

function findBox(host, box) {
  const h = hosts.find((x) => x.name === host);
  return h && h.boxes.find((b) => b.name === box);
}

// --- the overview -------------------------------------------------------------------

function renderOverview() {
  const main = $("overview");
  if (!loaded) return;
  if (!hosts.length) {
    main.replaceChildren(el("p", { class: "error", text: "No hosts answered." }));
    return;
  }
  const cards = hosts.map((h) => {
    const state = h.reachable
      ? el("span", { class: "state", text: `${h.boxes.length} box${h.boxes.length === 1 ? "" : "es"}` })
      : el("span", { class: "state bad", text: h.reason || "not reachable" });
    const body = [];
    if (!h.reachable) {
      body.push(el("p", { class: "hint" }, "Start it: ",
        el("code", { text: `devboxen host start ${h.name}` })));
    } else if (!h.boxes.length) {
      body.push(el("p", { class: "hint" }, "No boxes. ", el("code", { text: `devboxen create main@${h.name}` })));
    }
    for (const b of h.boxes) {
      const meta = [b.memory, b.cpus && `${b.cpus} CPUs`].filter(Boolean).join(" · ");
      const sessions = b.up
        ? b.sessions.map((s) => sessionChip(h.name, b.name, s, false))
        : [];
      body.push(el("div", { class: "box" + (b.up ? "" : " down") },
        el("div", { class: "head" },
          el("a", { class: "name", href: href(h.name, b.name, "shell"), text: b.name }),
          el("span", { class: "meta", text: meta })),
        b.up
          ? el("div", { class: "list" }, sessions.length ? sessions : el("span", { class: "meta", text: "no sessions yet" }))
          : el("div", { class: "reason", text: b.reason || "not running" })));
    }
    return el("section", { class: "host" }, el("header", {}, el("h2", { text: h.name }), state), body);
  });
  main.replaceChildren(el("div", { class: "hosts" }, cards));
}

// --- the workspace ------------------------------------------------------------------

function renderPicker() {
  $("picker-button").textContent = `${current.box}@${current.host}`;
  const items = [];
  for (const h of hosts) {
    items.push(el("h4", { text: h.name }));
    if (!h.reachable) items.push(el("p", { text: h.reason || "not reachable" }));
    for (const b of h.boxes) {
      items.push(el("a", {
        href: href(h.name, b.name, "shell"),
        class: b.up ? "" : "down",
        role: "menuitem",
      }, b.name, el("small", { text: b.up ? `${b.sessions.length} session${b.sessions.length === 1 ? "" : "s"}` : (b.reason || "down") })));
    }
  }
  items.push(el("h4", { text: "" }), el("a", { href: "#/", role: "menuitem" }, "All boxes"));
  $("picker-menu").replaceChildren(...items);
}

function renderSessions() {
  const b = findBox(current.host, current.box);
  const list = (b && b.up ? b.sessions : []).slice();
  if (!list.some((s) => s.name === current.session)) {
    list.push({ name: current.session, attached: 0, command: "" });
  }
  list.sort((x, y) => x.name.localeCompare(y.name));
  $("sessions").replaceChildren(...list.map((s) => sessionChip(current.host, current.box, s, s.name === current.session)));
}

function applyLayout() {
  let layout = store.get("layout", "split");
  if (NARROW.matches && layout === "split") layout = "term";
  const ws = $("workspace");
  ws.classList.remove("split", "code", "term");
  ws.classList.add(layout);
  for (const btn of document.querySelectorAll(".layout button")) {
    btn.classList.toggle("on", btn.dataset.layout === layout);
  }
  document.documentElement.style.setProperty("--split", store.get("split", "55") + "%");
}

function openBox(r) {
  const base = `/h/${r.host}/b/${r.box}`;
  const code = `${base}/code/`;
  if (codeSrc !== code) {
    codeSrc = code;
    $("code").src = code;
  }
  const term = `${base}/term/?arg=${encodeURIComponent(r.session)}`;
  if (!current || current.host !== r.host || current.box !== r.box || current.session !== r.session) {
    $("term").src = term;
  }
  current = r;
  document.title = `${r.session} · ${r.box}@${r.host} — devboxen`;
  renderPicker();
  renderSessions();
  applyLeft();
  if (leftTab() === "code" || store.get("layout", "split") === "term") setTimeout(() => $("term").focus(), 50);
}

// --- state ----------------------------------------------------------------------------

function show() {
  const r = route();
  const inBox = r !== null;
  $("overview").hidden = inBox;
  $("workspace").hidden = !inBox;
  $("picker").hidden = !inBox;
  $("sessions").hidden = !inBox;
  $("actions").hidden = !inBox;
  closeMenu();
  if (inBox) {
    applyLayout();
    openBox(r);
  } else {
    current = null;
    disconnectScreen("screen");
    disconnectScreen("watch");
    codeSrc = "";
    $("code").removeAttribute("src");
    $("term").removeAttribute("src");
    document.title = "devboxen";
    renderOverview();
  }
}

async function refresh() {
  try {
    const res = await fetch("api/hosts", { cache: "no-store" });
    if (!res.ok) throw new Error(res.status);
    const data = await res.json();
    hosts = data.hosts || [];
    loaded = true;
  } catch (e) {
    if (!loaded) {
      $("overview").replaceChildren(el("p", { class: "error", text: "The box list did not load (" + e.message + ")." }));
    }
    return;
  }
  if (current) {
    renderPicker();
    renderSessions();
  } else {
    renderOverview();
  }
}

// --- controls -------------------------------------------------------------------------

function closeMenu() {
  $("picker-menu").hidden = true;
  $("picker-button").setAttribute("aria-expanded", "false");
  closeNew();
}

// On a phone, the new-session field and buttons are behind a "+" (the bar has no room).
function closeNew() {
  document.querySelector(".bar").classList.remove("adding");
  $("new-toggle").setAttribute("aria-expanded", "false");
}
$("new-toggle").addEventListener("click", (e) => {
  e.stopPropagation();
  const bar = document.querySelector(".bar");
  const open = !bar.classList.contains("adding");
  closeMenu();
  bar.classList.toggle("adding", open);
  $("new-toggle").setAttribute("aria-expanded", String(open));
  if (open) $("new-name").focus();
});

$("picker-button").addEventListener("click", (e) => {
  e.stopPropagation();
  const menu = $("picker-menu");
  menu.hidden = !menu.hidden;
  $("picker-button").setAttribute("aria-expanded", String(!menu.hidden));
});
document.addEventListener("click", (e) => {
  if (!$("picker").contains(e.target) && !$("new-form").contains(e.target)) closeMenu();
});
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") closeMenu();
});

function newSession(claude) {
  const input = $("new-name");
  const raw = input.value.trim();
  let name = raw;
  if (claude) name = raw ? (raw.startsWith("claude") ? raw : `claude-${raw}`) : "claude";
  if (!name) {
    input.focus();
    return;
  }
  if (!SESSION.test(name)) {
    input.setCustomValidity("Letters, digits, _ and - (at most 32)");
    input.reportValidity();
    return;
  }
  input.value = "";
  closeNew();
  location.hash = href(current.host, current.box, name);
}
$("new-name").addEventListener("input", (e) => e.target.setCustomValidity(""));
$("new-form").addEventListener("submit", (e) => { e.preventDefault(); newSession(false); });
$("new-claude").addEventListener("click", () => newSession(true));

for (const btn of document.querySelectorAll(".layout button")) {
  btn.addEventListener("click", () => {
    store.set("layout", btn.dataset.layout);
    applyLayout();
  });
}
NARROW.addEventListener("change", () => { if (current) applyLayout(); });

// Dragging the divider resizes the panes (in percent of the window, remembered).
(() => {
  const divider = $("divider");
  const setSplit = (pct) => {
    pct = Math.min(85, Math.max(15, pct));
    document.documentElement.style.setProperty("--split", pct + "%");
    return pct;
  };
  divider.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    divider.setPointerCapture(e.pointerId);
    divider.classList.add("drag");
    document.body.classList.add("dragging");
    const move = (ev) => setSplit((ev.clientX / window.innerWidth) * 100);
    const up = (ev) => {
      const pct = setSplit((ev.clientX / window.innerWidth) * 100);
      store.set("split", pct.toFixed(1));
      divider.classList.remove("drag");
      document.body.classList.remove("dragging");
      divider.removeEventListener("pointermove", move);
      divider.removeEventListener("pointerup", up);
    };
    divider.addEventListener("pointermove", move);
    divider.addEventListener("pointerup", up);
  });
  divider.addEventListener("keydown", (e) => {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    const now = parseFloat(store.get("split", "55"));
    store.set("split", setSplit(now + (e.key === "ArrowLeft" ? -2 : 2)).toFixed(1));
  });
})();

// --- the terminal's clipboard ---------------------------------------------------------
//
// tmux has the mouse (mouse on): a drag selects in tmux, and on release tmux copies the text
// and sends it to the terminal as OSC 52 (set-clipboard on), as it does to iTerm or Ghostty on
// a Mac. ttyd's terminal (xterm.js) ignores OSC 52, so the copy went nowhere. The terminal
// is same-origin here and ttyd exposes it as window.term: give it an OSC 52 handler that
// writes to the browser's clipboard. (A Shift-drag selects in the browser instead; ttyd copies
// that itself.)

let toastTimer = 0;
function toast(text, warn) {
  const t = $("toast");
  t.textContent = text;
  t.classList.toggle("warn", !!warn);
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, warn ? 4000 : 1200);
}

function decodeBase64(b64) {
  const bin = atob(b64);
  return new TextDecoder().decode(Uint8Array.from(bin, (c) => c.charCodeAt(0)));
}

async function writeClipboard(win, text) {
  try {
    await win.navigator.clipboard.writeText(text);
    return true;
  } catch {
    // The async API wants a recent click (Safari, Firefox are strict); the older
    // execCommand path works in more cases. Keep the terminal's focus.
    const doc = win.document;
    const had = doc.activeElement;
    const ta = doc.createElement("textarea");
    ta.value = text;
    ta.style.cssText = "position:fixed;top:0;left:0;opacity:0";
    doc.body.appendChild(ta);
    ta.select();
    let ok = false;
    try { ok = doc.execCommand("copy"); } catch { ok = false; }
    ta.remove();
    if (had && had.focus) had.focus();
    return ok;
  }
}

function hookTerminalClipboard() {
  const frame = $("term");
  let tries = 0;
  const attempt = () => {
    let win;
    try { win = frame.contentWindow; } catch { return; }
    const term = win && win.term;
    if (!term || !term.parser) {
      if (++tries < 100) setTimeout(attempt, 100); // ttyd makes its terminal after load
      return;
    }
    if (term.__devboxOsc52) return;
    term.__devboxOsc52 = true;
    term.parser.registerOscHandler(52, (data) => {
      // "<selection>;<base64>": "?" is a read request (refused), anything else a copy.
      const b64 = data.slice(data.indexOf(";") + 1);
      if (b64 === "?" || data.indexOf(";") < 0) return true;
      let text;
      try { text = decodeBase64(b64); } catch { return true; }
      if (!text) return true;
      writeClipboard(win, text).then((ok) =>
        ok ? toast("copied") : toast("not copied: the browser refused the clipboard", true));
      return true;
    });
  };
  attempt();
}
$("term").addEventListener("load", hookTerminalClipboard);

// --- the left pane: VS Code, the box's browser, Claude's screen -------------------------
//
// "screen" is a Chrome on the box's display :98, "watch" the box's display :99 (Claude's
// browser, e2e tests). Both are VNC displays (TigerVNC's Xvnc), shown with noVNC, whose files
// and WebSocket the box serves itself (web-vnc.js, through /h/<host>/b/<box>/<svc>/). A
// display is connected while its tab is shown. The browser's size follows the pane; Claude's
// screen keeps its size and is scaled, and only watched unless "control" is on.

const IS_MAC = /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);
const XK_Control_L = 0xffe3;
const screens = { screen: null, watch: null }; // {key, rfb, timer, control}
let RFBClass = null;
let RFBFrom = "";

function leftTab() {
  const t = store.get("left", "code");
  return ["code", "screen", "watch"].includes(t) ? t : "code";
}

function vncState(text) { $("vnc-state").textContent = text || ""; }

async function loadRFB(base) {
  // noVNC from the box being opened (any box's copy speaks the same protocol).
  if (RFBClass && RFBFrom) return RFBClass;
  const mod = await import(`${base}/screen/core/rfb.js`);
  RFBClass = mod.default;
  RFBFrom = base;
  return RFBClass;
}

function disconnectScreen(svc) {
  const s = screens[svc];
  if (!s) return;
  clearTimeout(s.timer);
  s.closing = true;
  try { s.rfb && s.rfb.disconnect(); } catch { /* already gone */ }
  $(svc).replaceChildren();
  screens[svc] = null;
}

async function connectScreen(svc) {
  if (!current) return;
  const base = `/h/${encodeURIComponent(current.host)}/b/${encodeURIComponent(current.box)}`;
  const key = `${base}/${svc}`;
  if (screens[svc] && screens[svc].key === key) return;
  disconnectScreen(svc);
  const s = { key, rfb: null, timer: 0, closing: false, control: false };
  screens[svc] = s;
  vncState("connecting…");
  let RFB;
  try {
    RFB = await loadRFB(base);
  } catch (e) {
    if (screens[svc] === s) vncState("noVNC did not load (box not rebuilt yet?)");
    return;
  }
  if (screens[svc] !== s) return;
  const url = `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}${key}/websockify`;
  const rfb = new RFB($(svc), url, { shared: true });
  s.rfb = rfb;
  rfb.background = "#181818";
  if (svc === "screen") {
    rfb.resizeSession = true;
  } else {
    rfb.scaleViewport = true;
    rfb.viewOnly = true;
  }
  rfb.addEventListener("connect", () => {
    if (screens[svc] !== s) return;
    vncState("");
    if (svc === "screen") rfb.focus();
  });
  rfb.addEventListener("disconnect", () => {
    if (screens[svc] !== s || s.closing) return;
    vncState("reconnecting…");
    // A new connection, as long as this tab is still shown.
    s.timer = setTimeout(() => {
      if (screens[svc] !== s) return;
      screens[svc] = null;
      $(svc).replaceChildren();
      if (leftTab() === svc) connectScreen(svc);
    }, 2000);
  });
  rfb.addEventListener("clipboard", (e) => {
    // The box's clipboard changed (Ctrl-C there; the selection alone is not sent, Xvnc has
    // SendPrimary=0): into this computer's clipboard.
    const text = e.detail && e.detail.text;
    if (!text) return;
    writeClipboard(window, text).then((ok) =>
      ok ? toast("copied") : toast("not copied: the browser refused the clipboard", true));
  });
  applyWatchControl();
}

function applyWatchControl() {
  const s = screens.watch;
  const on = !!(s && s.control);
  if (s && s.rfb) s.rfb.viewOnly = !on;
  const btn = $("watch-control");
  btn.textContent = on ? "controlling" : "watching";
  btn.classList.toggle("on", on);
}

function applyLeft() {
  const tab = leftTab();
  for (const btn of document.querySelectorAll(".tabs [data-left]")) {
    btn.classList.toggle("on", btn.dataset.left === tab);
    btn.setAttribute("aria-selected", String(btn.dataset.left === tab));
  }
  $("code").hidden = tab !== "code";
  $("screen").hidden = tab !== "screen";
  $("watch").hidden = tab !== "watch";
  $("watch-control").hidden = tab !== "watch";
  vncState("");
  for (const svc of ["screen", "watch"]) {
    if (tab === svc && current) connectScreen(svc);
    else disconnectScreen(svc);
  }
}

for (const btn of document.querySelectorAll(".tabs [data-left]")) {
  btn.addEventListener("click", () => {
    store.set("left", btn.dataset.left);
    // The left pane hidden by the layout: show it.
    if (store.get("layout", "split") === "term") { store.set("layout", "split"); applyLayout(); }
    applyLeft();
  });
}
$("watch-control").addEventListener("click", () => {
  if (!screens.watch) return;
  screens.watch.control = !screens.watch.control;
  applyWatchControl();
});

// Keys. The box's Chrome is a Linux one: its shortcuts are Ctrl-…. On a Mac, Cmd-<key> is
// sent as Ctrl-<key> (Cmd itself is not sent), so Cmd-C, Cmd-A, Cmd-F, Cmd-L… work; the
// shortcuts the Mac's browser keeps for itself (Cmd-T, Cmd-W, Cmd-N, Cmd-Q) never reach the
// page: use Ctrl-T, Ctrl-W there. Paste (Cmd-V, elsewhere Ctrl-V): see below.
function sendCtrl(rfb, e) {
  const ch = e.key.length === 1 ? e.key.toLowerCase() : "";
  if (!ch) return false;
  rfb.sendKey(XK_Control_L, "ControlLeft", true);
  rfb.sendKey(ch.charCodeAt(0), e.code, true);
  rfb.sendKey(ch.charCodeAt(0), e.code, false);
  rfb.sendKey(XK_Control_L, "ControlLeft", false);
  return true;
}

// Paste: a canvas is not editable, so browsers paste nowhere and some fire no paste event.
// For the moment of Cmd/Ctrl-V, a hidden text field has the focus: the browser pastes into
// it, its paste event hands the text to the box's clipboard, Ctrl-V goes to the box, and the
// focus returns to the screen.
const pasteField = el("textarea", { class: "paste-field", "aria-hidden": "true", tabindex: "-1" });
document.body.append(pasteField);
let pasteTarget = null; // {svc, timer}

function endPaste() {
  if (!pasteTarget) return;
  clearTimeout(pasteTarget.timer);
  const s = screens[pasteTarget.svc];
  pasteTarget = null;
  pasteField.value = "";
  if (s && s.rfb) s.rfb.focus();
}

pasteField.addEventListener("paste", (e) => {
  const target = pasteTarget;
  e.preventDefault();
  const s = target && screens[target.svc];
  const text = e.clipboardData && e.clipboardData.getData("text/plain");
  if (s && s.rfb && text) {
    s.rfb.clipboardPasteFrom(text);
    s.rfb.sendKey(XK_Control_L, "ControlLeft", true);
    s.rfb.sendKey(0x76, "KeyV", true);
    s.rfb.sendKey(0x76, "KeyV", false);
    s.rfb.sendKey(XK_Control_L, "ControlLeft", false);
  }
  endPaste();
});
pasteField.addEventListener("keyup", (e) => {
  // The V (or Cmd) released while the field had the focus: nothing for the box.
  if (pasteTarget && !e.metaKey && !e.ctrlKey) endPaste();
});

for (const svc of ["screen", "watch"]) {
  const box = $(svc);
  const swallowed = new Set(); // keys whose keyup noVNC must not see either
  const active = () => {
    const s = screens[svc];
    return s && s.rfb && !(svc === "watch" && !s.control) ? s.rfb : null;
  };
  box.addEventListener("keydown", (e) => {
    const rfb = active();
    if (!rfb) return;
    const mod = IS_MAC ? e.metaKey : e.ctrlKey;
    if (IS_MAC && (e.key === "Meta" || e.key === "OS")) {
      e.stopPropagation(); // Cmd alone: nothing for the box
      return;
    }
    if (!mod || e.altKey) return;
    if (e.key.toLowerCase() === "v" && !e.shiftKey) {
      // Not prevented: the browser pastes, into the field.
      e.stopPropagation();
      pasteTarget = { svc, timer: setTimeout(endPaste, 1000) };
      pasteField.value = "";
      pasteField.focus();
      return;
    }
    if (IS_MAC && sendCtrl(rfb, e)) {
      e.preventDefault();
      e.stopPropagation();
      swallowed.add(e.code);
    }
  }, true);
  box.addEventListener("keyup", (e) => {
    if (IS_MAC && (e.key === "Meta" || e.key === "OS")) { e.stopPropagation(); return; }
    if (swallowed.delete(e.code)) e.stopPropagation();
  }, true);
}

// --- a dropped terminal ---------------------------------------------------------------
//
// ttyd reconnects by itself after a lost connection, but not after a normal close (code
// 1000: what a phone's browser does to a tab in the background, or the session ended): then
// it shows "Press ⏎ to Reconnect" and waits for Enter. Instead: back in the tab (or within
// moments of it), the terminal reconnects by itself; otherwise one tap anywhere on it does.
// Reconnecting is loading the terminal again (devbox-web-attach attaches to the session).

let termDown = false;
let visibleAt = Date.now();
let autoUsed = false; // one reconnect by itself per return to the tab (no loop)

function reconnectTerminal() {
  termDown = false;
  $("reconnect").hidden = true;
  try { $("term").contentWindow.location.reload(); } catch { $("term").src = $("term").src; }
}

function terminalDown() {
  if (termDown) return;
  termDown = true;
  $("reconnect").hidden = false;
  if (document.hidden) return; // on the return to the tab
  if (!autoUsed && Date.now() - visibleAt < 5000) {
    autoUsed = true;
    reconnectTerminal();
  }
}

function watchTerminal() {
  termDown = false;
  $("reconnect").hidden = true;
  let tries = 0;
  const attempt = () => {
    let win;
    try { win = $("term").contentWindow; } catch { return; }
    const term = win && win.term;
    if (!term || !term.element) {
      if (++tries < 100) setTimeout(attempt, 100);
      return;
    }
    let queued = false;
    const check = () => {
      queued = false;
      for (const node of term.element.children) {
        if (node.textContent && node.textContent.includes("to Reconnect")) { terminalDown(); return; }
      }
    };
    new win.MutationObserver(() => {
      if (!queued) { queued = true; win.setTimeout(check, 50); }
    }).observe(term.element, { childList: true, subtree: true, characterData: true });
    check();
  };
  attempt();
}
$("term").addEventListener("load", watchTerminal);
$("reconnect").addEventListener("click", reconnectTerminal);

const backInTab = () => {
  visibleAt = Date.now();
  autoUsed = false;
  if (termDown) {
    autoUsed = true;
    reconnectTerminal();
  }
};
document.addEventListener("visibilitychange", () => { if (!document.hidden) backInTab(); });
window.addEventListener("pageshow", (e) => { if (e.persisted) backInTab(); });

// --- keys a phone's keyboard lacks ----------------------------------------------------
//
// On a touch screen, a row under the terminal (the way Termux and Blink Shell do it): Esc,
// Tab, Shift-Tab, Ctrl and Alt (each for the next key, from the row or the keyboard), the
// arrows, Ctrl-C, tmux's prefix (Ctrl-B) and the characters the keyboard hides. The keys go
// into the terminal as its own input (xterm.js's term.input), as typed. Tapping the row does
// not take the focus from the terminal, so the keyboard stays open. And since iOS lays the
// keyboard over the page instead of shrinking it, the page follows the visible part
// (visualViewport): the terminal, its prompt and the row stay above the keyboard.

const TOUCH = window.matchMedia("(pointer: coarse)");
const KEYS = [
  ["esc", "Esc"], ["tab", "Tab"], ["stab", "⇧Tab"], ["ctrl", "Ctrl"], ["alt", "Alt"],
  ["left", "←"], ["up", "↑"], ["down", "↓"], ["right", "→"],
  ["^c", "^C"], ["^b", "^B", "tmux prefix"],
  ["|"], ["~"], ["/"], ["-"], ["_"], ["\\"], ["`"],
];
const mods = { ctrl: false, alt: false };

function terminal() {
  try {
    const win = $("term").contentWindow;
    return win && win.term && typeof win.term.input === "function" ? win.term : null;
  } catch { return null; }
}

// Ctrl-<char> as the byte a terminal sends: letters and @[\]^_ (and space, ?).
function ctrlChar(ch) {
  const c = ch.toLowerCase();
  if (c >= "a" && c <= "z") return String.fromCharCode(c.charCodeAt(0) - 96);
  const special = { "@": 0, " ": 0, "[": 27, "\\": 28, "]": 29, "^": 30, "_": 31, "?": 127 };
  return c in special ? String.fromCharCode(special[c]) : ch;
}

function setMod(name, on) {
  mods[name] = on;
  const btn = document.querySelector(`#keys [data-key="${name}"]`);
  if (btn) btn.classList.toggle("on", on);
}

// The data for a key of the row or a character typed while Ctrl or Alt is on; uses up the
// modifiers.
function keyData(key, term) {
  const arrows = { up: "A", down: "B", right: "C", left: "D" };
  let data;
  if (key in arrows) {
    const m = (mods.alt ? 2 : 0) + (mods.ctrl ? 4 : 0);
    const app = term && term.modes && term.modes.applicationCursorKeysMode;
    data = m ? `\x1b[1;${m + 1}${arrows[key]}` : `\x1b${app ? "O" : "["}${arrows[key]}`;
  } else if (key === "esc") data = "\x1b";
  else if (key === "tab") data = "\t";
  else if (key === "stab") data = "\x1b[Z";
  else if (key === "^c") data = "\x03";
  else if (key === "^b") data = "\x02";
  else {
    data = mods.ctrl ? ctrlChar(key) : key;
    if (mods.alt) data = "\x1b" + data;
  }
  setMod("ctrl", false);
  setMod("alt", false);
  return data;
}

(() => {
  const bar = $("keys");
  bar.replaceChildren(...KEYS.map(([key, label, title]) =>
    el("button", { type: "button", "data-key": key, title: title || label || key, tabindex: "-1", text: label || key })));
  // No focus for the row: the terminal keeps it (and the keyboard stays). With pointerdown
  // prevented, WebKit fires no click after a tap: the key is pressed on pointerup (a swipe
  // along the row is a pointercancel, no key).
  let down = null;
  bar.addEventListener("pointerdown", (e) => {
    e.preventDefault();
    down = e.target.closest("button");
  });
  bar.addEventListener("pointercancel", () => { down = null; });
  bar.addEventListener("pointerup", (e) => {
    const btn = e.target.closest("button");
    const was = down;
    down = null;
    if (!btn || btn !== was) return;
    const key = btn.dataset.key;
    if (key === "ctrl" || key === "alt") {
      setMod(key, !mods[key]);
      return;
    }
    const term = terminal();
    if (!term) return;
    term.input(keyData(key, term), true);
  });
  const applyTouch = () => { bar.hidden = !TOUCH.matches; };
  TOUCH.addEventListener("change", applyTouch);
  applyTouch();
})();

// Ctrl or Alt on, then a key on the phone's keyboard: that key, with them. Before xterm.js
// sees it (capturing, in the terminal's page).
function hookTerminalKeys() {
  let tries = 0;
  const attempt = () => {
    let win;
    try { win = $("term").contentWindow; } catch { return; }
    const term = win && win.term;
    if (!term || typeof term.input !== "function") {
      if (++tries < 100) setTimeout(attempt, 100);
      return;
    }
    if (win.__devboxKeys) return;
    win.__devboxKeys = true;
    const take = (e, ch) => {
      e.preventDefault();
      e.stopImmediatePropagation();
      term.input(keyData(ch, term), true);
    };
    win.addEventListener("keydown", (e) => {
      if ((mods.ctrl || mods.alt) && e.key && e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) take(e, e.key);
    }, true);
    // Keyboards that send no usable keydown (Android: "Unidentified"): the text instead.
    win.addEventListener("beforeinput", (e) => {
      if ((mods.ctrl || mods.alt) && e.inputType === "insertText" && e.data) take(e, e.data[0]);
    }, true);
  };
  attempt();
}
$("term").addEventListener("load", hookTerminalKeys);

// Scrolling with a finger. xterm.js scrolls only its own buffer on a touch drag, and does
// nothing while the program has the mouse (tmux: mouse on), so dragging scrolled nothing.
// A vertical drag becomes mouse wheel events on the terminal instead, which xterm.js hands on
// like a real wheel: to tmux (into its copy mode, scrolling the session's history), else
// its own scrollback. tmux scrolls 5 lines a wheel step: one step per 1.5 rows dragged.
function hookTerminalTouch() {
  let tries = 0;
  const attempt = () => {
    let win;
    try { win = $("term").contentWindow; } catch { return; }
    const term = win && win.term;
    if (!term || !term.element) {
      if (++tries < 100) setTimeout(attempt, 100);
      return;
    }
    if (win.__devboxTouch) return;
    win.__devboxTouch = true;
    const doc = win.document;
    let startY = null, lastY = 0, acc = 0, dragging = false;
    const wheel = (x, y, dir) => {
      const target = doc.elementFromPoint(x, y) || term.element;
      target.dispatchEvent(new win.WheelEvent("wheel", {
        deltaY: dir, deltaMode: 1, clientX: x, clientY: y, bubbles: true, cancelable: true,
      }));
    };
    doc.addEventListener("touchstart", (e) => {
      const t = e.touches[0];
      startY = t.clientY;
      lastY = t.clientY;
      acc = 0;
      dragging = false;
    }, { capture: true, passive: true });
    doc.addEventListener("touchmove", (e) => {
      if (startY === null) return;
      const t = e.touches[0];
      const dy = t.clientY - lastY;
      lastY = t.clientY;
      if (!dragging && Math.abs(t.clientY - startY) < 8) return;
      dragging = true;
      e.preventDefault();
      e.stopImmediatePropagation();
      acc += dy;
      const step = 1.5 * Math.max(8, term.element.clientHeight / Math.max(1, term.rows));
      while (Math.abs(acc) >= step) {
        const down = acc > 0; // the finger down: back in the history, a wheel up
        acc += down ? -step : step;
        wheel(t.clientX, t.clientY, down ? -1 : 1);
      }
    }, { capture: true, passive: false });
    const end = () => { startY = null; };
    doc.addEventListener("touchend", end, { capture: true, passive: true });
    doc.addEventListener("touchcancel", end, { capture: true, passive: true });
  };
  attempt();
}
$("term").addEventListener("load", hookTerminalTouch);

// The page in the visible part of the window, above an on-screen keyboard (iOS).
(() => {
  const vv = window.visualViewport;
  if (!vv) return;
  const fit = () => {
    if (!TOUCH.matches) {
      document.body.style.removeProperty("height");
      document.body.style.removeProperty("transform");
      return;
    }
    document.body.style.height = `${Math.round(vv.height)}px`;
    document.body.style.transform = vv.offsetTop ? `translateY(${Math.round(vv.offsetTop)}px)` : "";
  };
  vv.addEventListener("resize", fit);
  vv.addEventListener("scroll", fit);
  TOUCH.addEventListener("change", fit);
  fit();
})();

window.addEventListener("hashchange", show);
show();
refresh();
setInterval(() => { if (!document.hidden) refresh(); }, 5000);
document.addEventListener("visibilitychange", () => { if (!document.hidden) refresh(); });
