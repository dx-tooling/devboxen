// The box's session API for devbox-web (../devbox-web/): GET /sessions lists the tmux
// sessions of dev as JSON, GET /exposed the ports devbox-expose shows in the browser. Listens on /run/devbox-web/api.sock (the host directory
// /var/lib/devbox/web/boxes/<box>); started as dev by entrypoint.sh. No dependencies.
"use strict";
const fs = require("fs");
const http = require("http");
const net = require("net");
const { execFile } = require("child_process");

const SOCKET = process.argv[2] || "/run/devbox-web/api.sock";
const FORMAT = ["#{session_name}", "#{session_attached}", "#{session_windows}",
  "#{session_activity}", "#{pane_current_command}"].join("\t");

function sessions(cb) {
  execFile("tmux", ["list-sessions", "-F", FORMAT], { timeout: 2000 }, (err, stdout) => {
    // No tmux server yet (no session): an empty list.
    if (err) return cb([]);
    cb(stdout.split("\n").filter(Boolean).map((line) => {
      const [name, attached, windows, activity, command] = line.split("\t");
      return { name, attached: +attached, windows: +windows, activity: +activity, command };
    }));
  });
}

// The registry devbox-expose writes (web-expose.js reads it the same way), and whether
// something answers on each port.
const REGISTRY = "/home/dev/.config/devbox/exposed";

function answers(port) {
  return new Promise((resolve) => {
    const s = net.connect({ host: "127.0.0.1", port, timeout: 300 });
    const done = (ok) => { s.destroy(); resolve(ok); };
    s.once("connect", () => done(true));
    s.once("timeout", () => done(false));
    s.once("error", () => {
      const s6 = net.connect({ host: "::1", port, timeout: 300 });
      const done6 = (ok) => { s6.destroy(); resolve(ok); };
      s6.once("connect", () => done6(true));
      s6.once("timeout", () => done6(false));
      s6.once("error", () => done6(false));
    });
  });
}

async function exposed() {
  let text = "";
  try { text = fs.readFileSync(REGISTRY, "utf8"); } catch { return []; }
  const list = [];
  for (const raw of text.split("\n")) {
    const [p, ...flags] = raw.replace(/#.*/, "").trim().split(/\s+/);
    const port = Number(p);
    if (!/^[1-9][0-9]{0,4}$/.test(p) || port > 65535 || list.some((e) => e.port === port)) continue;
    list.push({ port, tls: flags.includes("tls") });
  }
  await Promise.all(list.map(async (e) => { e.listening = await answers(e.port); }));
  return list.sort((a, b) => a.port - b.port);
}

const server = http.createServer((req, res) => {
  if (req.method === "GET" && req.url === "/exposed") {
    exposed().then((list) => {
      res.writeHead(200, { "Content-Type": "application/json", "Cache-Control": "no-store" });
      res.end(JSON.stringify(list));
    });
    return;
  }
  if (req.method === "GET" && req.url === "/sessions") {
    sessions((list) => {
      res.writeHead(200, { "Content-Type": "application/json", "Cache-Control": "no-store" });
      res.end(JSON.stringify(list));
    });
    return;
  }
  res.writeHead(404).end();
});

try { fs.unlinkSync(SOCKET); } catch { /* not there */ }
server.listen(SOCKET, () => fs.chmodSync(SOCKET, 0o600));
