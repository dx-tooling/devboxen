// devboxen-web's exposed ports in the box (docs/box.md, "Showing an app in the browser"):
// for every port in the registry that `devbox-expose` keeps, a Unix socket
// <SOCKET_DIR>/port-<port>.sock whose connections it relays to that port inside the box.
// The front reaches the socket (the box's /run/devbox-web is a host directory) and serves it
// as https://<port>--<box>--<host>.<domain>. Nothing listens on the network.
//
// Bytes are passed through unchanged, so HTTP/1.1, WebSockets and server-sent events work.
// For a port marked "tls" (the app itself speaks HTTPS, e.g. with a mkcert certificate), the
// relay is the TLS client: the front speaks plain HTTP into the socket, the relay wraps it
// for the app (certificate not checked: it is the box's own app on its own loopback).
//
// The registry, one port per line (`1234` or `8443 tls`; `#` comments), is read again on
// every change. Started as dev by entrypoint.sh. No dependencies.
//
//   node web-expose.js SOCKET_DIR REGISTRY
"use strict";

const fs = require("fs");
const net = require("net");
const path = require("path");
const tls = require("tls");

const [socketDir, registry] = process.argv.slice(2);
if (!socketDir || !registry) {
  console.error("usage: node web-expose.js SOCKET_DIR REGISTRY");
  process.exit(2);
}

const SOCKET_RE = /^port-([0-9]{1,5})\.sock$/;
const servers = new Map(); // port -> {server, tls}

function parse(text) {
  const ports = new Map();
  for (const raw of text.split("\n")) {
    const line = raw.replace(/#.*/, "").trim();
    if (!line) continue;
    const [p, ...flags] = line.split(/\s+/);
    const port = Number(p);
    if (!/^[1-9][0-9]{0,4}$/.test(p) || port > 65535) continue;
    ports.set(port, { tls: flags.includes("tls") });
  }
  return ports;
}

function read() {
  try {
    return parse(fs.readFileSync(registry, "utf8"));
  } catch {
    return new Map(); // no registry yet: nothing exposed
  }
}

// The app's side: 127.0.0.1 first, then ::1 (a dev server bound to the IPv6 loopback only).
function connectApp(port, useTLS, cb) {
  const hosts = ["127.0.0.1", "::1"];
  const attempt = (i) => {
    const opts = { host: hosts[i], port };
    const sock = useTLS
      ? tls.connect({ ...opts, servername: "localhost", rejectUnauthorized: false, ALPNProtocols: ["http/1.1"] })
      : net.connect(opts);
    const ready = useTLS ? "secureConnect" : "connect";
    const fail = (err) => {
      sock.destroy();
      if (i + 1 < hosts.length && err.code === "ECONNREFUSED") attempt(i + 1);
      else cb(err);
    };
    sock.once("error", fail);
    sock.once(ready, () => {
      sock.removeListener("error", fail);
      cb(null, sock);
    });
  };
  attempt(0);
}

// What the browser sees when nothing answers on the port.
function unavailable(client, port, err) {
  const body = `devboxen: nothing answers on port ${port} in this box (${err.code || err.message}).\n` +
    `Start the app, or stop exposing the port: devbox-expose off ${port}\n`;
  client.end("HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain; charset=utf-8\r\n" +
    `Content-Length: ${Buffer.byteLength(body)}\r\nConnection: close\r\n\r\n${body}`);
}

function open(port, useTLS) {
  const file = path.join(socketDir, `port-${port}.sock`);
  const server = net.createServer((client) => {
    client.pause();
    client.on("error", () => {});
    connectApp(port, useTLS, (err, app) => {
      if (err) {
        unavailable(client, port, err);
        return;
      }
      app.on("error", () => client.destroy());
      client.on("error", () => app.destroy());
      app.on("close", () => client.destroy());
      client.on("close", () => app.destroy());
      client.pipe(app);
      app.pipe(client);
      client.resume();
    });
  });
  server.on("error", (err) => console.error(`port ${port}: ${err.message}`));
  try { fs.unlinkSync(file); } catch { /* not there */ }
  server.listen(file, () => {
    fs.chmodSync(file, 0o600);
    console.log(`port ${port}${useTLS ? " (tls)" : ""}: ${file}`);
  });
  servers.set(port, { server, tls: useTLS });
}

function close(port) {
  const s = servers.get(port);
  servers.delete(port);
  // Server.close() keeps open connections running; it removes the socket file once they end.
  // Unlink now, so the front stops finding the port at once.
  s.server.close();
  try { fs.unlinkSync(path.join(socketDir, `port-${port}.sock`)); } catch { /* gone */ }
  console.log(`port ${port}: closed`);
}

function sync() {
  const want = read();
  for (const [port, s] of servers) {
    const w = want.get(port);
    if (!w || w.tls !== s.tls) close(port);
  }
  for (const [port, w] of want) {
    if (!servers.has(port)) open(port, w.tls);
  }
}

// Sockets left over from an earlier run whose ports are no longer exposed.
for (const name of fs.readdirSync(socketDir)) {
  if (SOCKET_RE.test(name)) {
    try { fs.unlinkSync(path.join(socketDir, name)); } catch { /* gone */ }
  }
}

sync();
// The registry's directory may not exist yet, and editors replace files: polling the file's
// metadata is what keeps working in every case (cheap: one stat a second).
fs.watchFile(registry, { interval: 1000 }, sync);
