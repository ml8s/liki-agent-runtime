#!/usr/bin/env node
// Minimal development-only origin for the official AG-UI browser client. It
// serves static files and proxies the AG-UI byte stream without interpretation.
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { extname, join, normalize } from "node:path";
import { Readable } from "node:stream";

const agentURL = new URL(process.env.LIKI_AGENT_RUNTIME_URL ?? "http://127.0.0.1:8083");
const [listenHost, listenPortText] = (
  process.env.LIKI_CHAT_UI_ADDR ?? "127.0.0.1:8084"
).split(":");
const listenPort = Number(listenPortText);

if (!listenHost || !Number.isInteger(listenPort) || listenPort < 1 || listenPort > 65535) {
  console.error(`invalid chat UI address: ${process.env.LIKI_CHAT_UI_ADDR}`);
  process.exit(1);
}

const root = fileURLToPath(new URL(".", import.meta.url));
const contentTypes = {
  ".css": "text/css; charset=utf-8",
  ".html": "text/html; charset=utf-8",
  ".js": "text/javascript; charset=utf-8",
};

function sendFile(res, pathname) {
  const filename = normalize(pathname).replace(/^([/\\])+/, "") || "index.html";
  if (filename.includes("..")) {
    res.writeHead(404).end();
    return;
  }

  readFile(join(root, filename))
    .then((body) => {
      res.writeHead(200, {
        "cache-control": "no-store",
        "content-type": contentTypes[extname(filename)] ?? "text/plain",
      });
      res.end(body);
    })
    .catch(() => res.writeHead(404).end());
}

async function proxyAGUI(req, res) {
  if (req.method !== "POST") {
    res.writeHead(405, { allow: "POST" }).end();
    return;
  }

  const upstreamAbort = new AbortController();
  res.on("close", () => {
    if (!res.writableEnded) upstreamAbort.abort();
  });

  const body = await new Promise((resolve, reject) => {
    const chunks = [];
    req.on("data", (chunk) => chunks.push(chunk));
    req.on("end", () => resolve(Buffer.concat(chunks)));
    req.on("error", reject);
  });

  const headers = {};
  for (const name of ["authorization", "content-type", "x-liki-user-id"]) {
    const value = req.headers[name];
    if (typeof value === "string") headers[name] = value;
  }

  const upstream = await fetch(new URL("/ag-ui", agentURL), {
    body: new Uint8Array(body),
    headers,
    method: "POST",
    signal: upstreamAbort.signal,
  });
  if (!upstream.body) {
    res.writeHead(502).end();
    return;
  }

  res.writeHead(upstream.status, {
    "content-type": upstream.headers.get("content-type") ?? "text/event-stream",
  });
  Readable.fromWeb(upstream.body).pipe(res);
}

createServer((req, res) => {
  const url = new URL(req.url ?? "/", "http://localhost");
  if (url.pathname === "/ag-ui") {
    proxyAGUI(req, res).catch(() => {
      if (!res.headersSent) res.writeHead(502).end();
      else res.end();
    });
    return;
  }
  sendFile(res, url.pathname);
}).listen(listenPort, listenHost, () => {
  const displayHost = listenHost === "0.0.0.0" ? "127.0.0.1" : listenHost;
  console.log(`liki-agent-runtime development chat UI: http://${displayHost}:${listenPort}`);
});
