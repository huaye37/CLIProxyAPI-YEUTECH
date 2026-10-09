#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import http from "node:http";
import https from "node:https";

const listenHost = process.env.YEUTECH_CODEX_BRIDGE_HOST || "127.0.0.1";
const listenPort = Number(process.env.YEUTECH_CODEX_BRIDGE_PORT || "18401");
const upstreamBase = new URL(process.env.YEUTECH_UPSTREAM_BASE_URL || "https://llm-api.yeutech.cn");
const upstreamAgent = upstreamBase.protocol === "https:"
  ? new https.Agent({ keepAlive: true })
  : new http.Agent({ keepAlive: true });
const upstreamTransport = upstreamBase.protocol === "https:" ? https : http;

let cachedToken = null;
let cachedTokenAt = 0;

function upstreamToken() {
  const envToken = process.env.YEUTECH_UPSTREAM_BEARER_TOKEN?.trim();
  if (envToken) return envToken;
  if (cachedToken && Date.now() - cachedTokenAt < 60_000) return cachedToken;
  cachedToken = execFileSync(
    "/usr/bin/security",
    ["find-generic-password", "-a", "codex", "-s", "cn.yeutech.llm-api", "-w"],
    { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] },
  ).trim();
  if (!cachedToken) throw new Error("YEUTECH gateway credential is empty");
  cachedTokenAt = Date.now();
  return cachedToken;
}

function upstreamPath(requestUrl) {
  const incoming = new URL(requestUrl || "/", "http://127.0.0.1");
  const prefix = upstreamBase.pathname.replace(/\/$/, "");
  return `${prefix}${incoming.pathname}${incoming.search}`;
}

function requestHeaders(headers) {
  const result = { ...headers };
  for (const name of ["host", "authorization", "connection", "proxy-connection", "keep-alive", "transfer-encoding", "upgrade"]) {
    delete result[name];
  }
  result.authorization = `Bearer ${upstreamToken()}`;
  result.host = upstreamBase.host;
  result["x-yeutech-consumer"] ||= "codex-desktop";
  return result;
}

function responseHeaders(headers) {
  const result = { ...headers };
  for (const name of ["connection", "proxy-connection", "keep-alive", "transfer-encoding", "upgrade"]) {
    delete result[name];
  }
  return result;
}

const server = http.createServer((request, response) => {
  if (request.method === "GET" && request.url === "/_yeutech/health") {
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({ ok: true, upstream: upstreamBase.origin }));
    return;
  }

  let headers;
  try {
    headers = requestHeaders(request.headers);
  } catch (error) {
    response.writeHead(503, { "content-type": "application/json" });
    response.end(JSON.stringify({ error: { message: error.message, type: "bridge_auth_error" } }));
    return;
  }

  const upstreamRequest = upstreamTransport.request({
    protocol: upstreamBase.protocol,
    hostname: upstreamBase.hostname,
    port: upstreamBase.port || undefined,
    method: request.method,
    path: upstreamPath(request.url),
    headers,
    agent: upstreamAgent,
  }, (upstreamResponse) => {
    response.writeHead(upstreamResponse.statusCode || 502, responseHeaders(upstreamResponse.headers));
    upstreamResponse.pipe(response);
  });

  upstreamRequest.on("error", (error) => {
    if (response.headersSent) {
      response.destroy(error);
      return;
    }
    response.writeHead(502, { "content-type": "application/json" });
    response.end(JSON.stringify({ error: { message: error.message, type: "bridge_upstream_error" } }));
  });
  request.on("aborted", () => upstreamRequest.destroy());
  response.on("close", () => {
    if (!response.writableEnded) upstreamRequest.destroy();
  });
  request.pipe(upstreamRequest);
});

server.listen(listenPort, listenHost, () => {
  process.stdout.write(`Codex YEUTECH auth bridge listening on http://${listenHost}:${listenPort}\n`);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => server.close(() => process.exit(0)));
}
