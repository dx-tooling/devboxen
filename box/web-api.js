// The box's session API for devbox-web (../devbox-web/): GET /sessions lists the tmux
// sessions of dev as JSON. Listens on /run/devbox-web/api.sock (the host directory
// /var/lib/devbox/web/boxes/<box>); started as dev by entrypoint.sh. No dependencies.
"use strict";
const fs = require("fs");
const http = require("http");
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

const server = http.createServer((req, res) => {
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
