// devboxen-web's screens in the box (docs/web.md): a VNC display (TigerVNC's Xvnc,
// on a Unix socket) to the browser. On a Unix socket of its own, this serves noVNC's files
// (/usr/share/novnc: the page imports core/rfb.js from here) and, on a WebSocket upgrade, pipes
// the WebSocket's binary messages to the display's socket and back. What websockify does, but
// on a Unix socket (Ubuntu's websockify 0.10 listens on TCP only). No dependencies.
//
//   node web-vnc.js LISTEN_SOCKET VNC_SOCKET [WEB_ROOT]
"use strict";

const crypto = require("crypto");
const fs = require("fs");
const http = require("http");
const net = require("net");
const path = require("path");

const [listen, vncSocket, webRoot = "/usr/share/novnc"] = process.argv.slice(2);
if (!listen || !vncSocket) {
  console.error("usage: node web-vnc.js LISTEN_SOCKET VNC_SOCKET [WEB_ROOT]");
  process.exit(2);
}

const TYPES = {
  ".js": "text/javascript; charset=utf-8",
  ".html": "text/html; charset=utf-8",
  ".css": "text/css; charset=utf-8",
  ".svg": "image/svg+xml",
  ".png": "image/png",
  ".ico": "image/x-icon",
  ".json": "application/json",
};

// Static files under the web root only, GET/HEAD only.
function serveFile(req, res) {
  if (req.method !== "GET" && req.method !== "HEAD") {
    res.writeHead(405).end();
    return;
  }
  let rel;
  try {
    rel = decodeURIComponent(new URL(req.url, "http://x").pathname);
  } catch {
    res.writeHead(400).end();
    return;
  }
  const file = path.join(webRoot, path.normalize(rel));
  if (!file.startsWith(webRoot + path.sep)) {
    res.writeHead(404).end();
    return;
  }
  fs.stat(file, (err, st) => {
    if (err || !st.isFile()) {
      res.writeHead(404).end();
      return;
    }
    res.writeHead(200, {
      "Content-Type": TYPES[path.extname(file)] || "application/octet-stream",
      "Content-Length": st.size,
      "Cache-Control": "no-cache",
      "X-Content-Type-Options": "nosniff",
    });
    if (req.method === "HEAD") res.end();
    else fs.createReadStream(file).pipe(res);
  });
}

// A WebSocket frame (server to client: unmasked, never fragmented).
function frame(opcode, payload) {
  const len = payload.length;
  let head;
  if (len < 126) {
    head = Buffer.from([0x80 | opcode, len]);
  } else if (len < 65536) {
    head = Buffer.alloc(4);
    head[0] = 0x80 | opcode; head[1] = 126; head.writeUInt16BE(len, 2);
  } else {
    head = Buffer.alloc(10);
    head[0] = 0x80 | opcode; head[1] = 127; head.writeBigUInt64BE(BigInt(len), 2);
  }
  return Buffer.concat([head, payload]);
}

function upgrade(req, client, head) {
  const key = req.headers["sec-websocket-key"];
  // Same origin only (the host's devbox-web checks too; like ttyd's -O).
  const origin = req.headers.origin;
  let sameOrigin = true;
  if (origin) {
    try { sameOrigin = new URL(origin).host === req.headers.host; } catch { sameOrigin = false; }
  }
  if (!key || req.headers["sec-websocket-version"] !== "13" || !sameOrigin) {
    client.end("HTTP/1.1 400 Bad Request\r\n\r\n");
    return;
  }
  const accept = crypto.createHash("sha1").update(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").digest("base64");
  const offered = (req.headers["sec-websocket-protocol"] || "").split(",").map((s) => s.trim());
  const lines = ["HTTP/1.1 101 Switching Protocols", "Upgrade: websocket", "Connection: Upgrade",
    `Sec-WebSocket-Accept: ${accept}`];
  if (offered.includes("binary")) lines.push("Sec-WebSocket-Protocol: binary");

  const vnc = net.createConnection(vncSocket);
  let closed = false;
  let upgraded = false;
  const close = () => {
    if (closed) return;
    closed = true;
    vnc.destroy();
    if (client.destroyed) return;
    client.end(upgraded ? frame(0x8, Buffer.alloc(0)) : "HTTP/1.1 502 Bad Gateway\r\n\r\n");
  };
  vnc.on("error", close);
  vnc.on("close", close);
  client.on("error", close);
  client.on("close", close);
  vnc.on("connect", () => {
    upgraded = true;
    client.write(lines.join("\r\n") + "\r\n\r\n");
  });

  // The display to the browser, with backpressure both ways.
  vnc.on("data", (d) => { if (!client.write(frame(0x2, d))) vnc.pause(); });
  client.on("drain", () => vnc.resume());
  vnc.on("drain", () => client.resume());

  // The browser to the display: parse frames (masked), forward data payloads in order (RFB
  // is a byte stream, so message boundaries do not matter), answer ping and close.
  let buf = head && head.length ? Buffer.from(head) : Buffer.alloc(0);
  const parse = () => {
    for (;;) {
      if (buf.length < 2) return;
      const opcode = buf[0] & 0x0f;
      const masked = (buf[1] & 0x80) !== 0;
      let len = buf[1] & 0x7f;
      let off = 2;
      if (len === 126) {
        if (buf.length < 4) return;
        len = buf.readUInt16BE(2); off = 4;
      } else if (len === 127) {
        if (buf.length < 10) return;
        const big = buf.readBigUInt64BE(2);
        if (big > 64n * 1024n * 1024n) { close(); return; }
        len = Number(big); off = 10;
      }
      if (!masked) { close(); return; } // clients must mask
      if (buf.length < off + 4 + len) return;
      const mask = buf.subarray(off, off + 4);
      const payload = Buffer.from(buf.subarray(off + 4, off + 4 + len));
      for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i & 3];
      buf = buf.subarray(off + 4 + len);
      if (opcode === 0x0 || opcode === 0x1 || opcode === 0x2) {
        if (!vnc.write(payload)) client.pause();
      } else if (opcode === 0x8) {
        close();
        return;
      } else if (opcode === 0x9) {
        client.write(frame(0xa, payload));
      }
    }
  };
  client.on("data", (d) => { buf = buf.length ? Buffer.concat([buf, d]) : d; parse(); });
  if (buf.length) vnc.once("connect", parse);
}

const server = http.createServer(serveFile);
server.on("upgrade", upgrade);
try { fs.unlinkSync(listen); } catch {}
server.listen(listen, () => fs.chmodSync(listen, 0o600));
