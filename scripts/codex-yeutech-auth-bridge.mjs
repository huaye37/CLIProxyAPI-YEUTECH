#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import http from "node:http";
import https from "node:https";
import { randomUUID } from "node:crypto";
import { isCodexStreamingResponse, rateLimitDisplay } from "./codex-yeutech-errors.mjs";
import { isCodexStandaloneSearch, readSearchBody, routeStandaloneSearch } from "./codex-yeutech-search.mjs";

const listenHost = process.env.YEUTECH_CODEX_BRIDGE_HOST || "127.0.0.1";
const listenPort = Number(process.env.YEUTECH_CODEX_BRIDGE_PORT || "18401");
const upstreamBase = new URL(process.env.YEUTECH_UPSTREAM_BASE_URL || "https://llm-api.yeutech.cn");
const liveBase = new URL(process.env.OPENAI_LIVE_BASE_URL || "https://api.openai.com");
const upstreamAgent = upstreamBase.protocol === "https:"
  ? new https.Agent({ keepAlive: true })
  : new http.Agent({ keepAlive: true });
const upstreamTransport = upstreamBase.protocol === "https:" ? https : http;
const liveAgent = liveBase.protocol === "https:"
  ? new https.Agent({ keepAlive: true })
  : new http.Agent({ keepAlive: true });
const liveTransport = liveBase.protocol === "https:" ? https : http;

let cachedToken = null;
let cachedTokenAt = 0;
let activeRequests = 0;

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

function targetPath(base, requestUrl) {
  const incoming = new URL(requestUrl || "/", "http://127.0.0.1");
  const prefix = base.pathname.replace(/\/$/, "");
  return `${prefix}${incoming.pathname}${incoming.search}`;
}

function isLiveRequest(requestUrl) {
  return new URL(requestUrl || "/", "http://127.0.0.1").pathname === "/v1/live";
}

function requestHeaders(headers, target, preserveAuthorization) {
  const result = { ...headers };
  for (const name of ["host", "connection", "proxy-connection", "keep-alive", "transfer-encoding", "upgrade"]) {
    delete result[name];
  }
  if (!preserveAuthorization) {
    delete result.authorization;
    result.authorization = `Bearer ${upstreamToken()}`;
    result["x-yeutech-consumer"] ||= "codex-desktop";
  }
  result.host = target.host;
  return result;
}

function responseHeaders(headers) {
  const result = { ...headers };
  for (const name of ["connection", "proxy-connection", "keep-alive", "transfer-encoding", "upgrade"]) {
    delete result[name];
  }
  return result;
}

function forwardResponse(request, response, upstreamResponse) {
  const status = upstreamResponse.statusCode || 502;
  const encoding = upstreamResponse.headers["content-encoding"];
  if (status !== 429 || !isCodexStreamingResponse(request) || (encoding && encoding !== "identity")) {
    response.writeHead(status, responseHeaders(upstreamResponse.headers));
    upstreamResponse.pipe(response);
    return;
  }

  // Bound only the error body; inference requests and successful streams stay unbuffered.
  const chunks = [];
  let bytes = 0;
  let passthrough = false;
  upstreamResponse.on("data", (chunk) => {
    if (passthrough) return;
    chunks.push(chunk);
    bytes += chunk.length;
    if (bytes > 64 * 1024) {
      passthrough = true;
      response.writeHead(status, responseHeaders(upstreamResponse.headers));
      for (const buffered of chunks) response.write(buffered);
      chunks.length = 0;
      upstreamResponse.pipe(response);
    }
  });
  upstreamResponse.on("end", () => {
    if (passthrough || response.destroyed) return;
    const body = Buffer.concat(chunks);
    const display = rateLimitDisplay(body.toString("utf8"), upstreamResponse.headers);
    if (!display) {
      response.writeHead(status, responseHeaders(upstreamResponse.headers));
      response.end(body);
      return;
    }
    // Codex discards HTTP 429 bodies. A failed Responses event preserves the
    // diagnostic while remaining a failed turn, never a synthetic model answer.
    const headers = responseHeaders(upstreamResponse.headers);
    for (const key of ["content-length", "content-encoding", "etag", "content-md5", "digest"]) delete headers[key];
    headers["content-type"] = "text/event-stream; charset=utf-8";
    headers["cache-control"] = "no-store";
    headers["x-yeutech-upstream-status"] = "429";
    const failure = { type: "response.failed", sequence_number: 0, response: {
      id: `resp_${randomUUID()}`, object: "response", status: "failed", output: [],
      error: { type: "rate_limit_error", code: "rate_limit_exceeded", message: display.message,
        reason: display.reason, upstream_status: 429, retry_after_seconds: display.retryAfterSeconds,
        resets_at: display.resetsAt === null ? null : Math.ceil(display.resetsAt / 1000) },
    } };
    response.writeHead(200, headers);
    response.end(`event: response.failed\ndata: ${JSON.stringify(failure)}\n\n`);
  });
  upstreamResponse.on("error", (error) => response.destroy(error));
}

const server = http.createServer(async (request, response) => {
  if (request.method === "GET" && request.url === "/_yeutech/health") {
    response.writeHead(200, { "content-type": "application/json" });
    response.end(JSON.stringify({ ok: true, version: "20261018-standalone-search-v1", activeRequests, upstream: upstreamBase.origin, liveUpstream: liveBase.origin }));
    return;
  }

  activeRequests += 1;
  let finished = false;
  const finishRequest = () => {
    if (!finished) activeRequests -= 1;
    finished = true;
  };
  response.once("finish", finishRequest);
  response.once("close", finishRequest);

  let searchBody;
  if (isCodexStandaloneSearch(request)) {
    try {
      if (request.headers["content-encoding"] && request.headers["content-encoding"] !== "identity") {
        throw Object.assign(new Error("Compressed standalone search is unsupported"), { statusCode: 415 });
      }
      searchBody = routeStandaloneSearch(await readSearchBody(request), process.env.YEUTECH_CODEX_SEARCH_MODEL);
    } catch (error) {
      if (!response.destroyed) {
        response.writeHead(error.statusCode || 400, { "content-type": "application/json" });
        response.end(JSON.stringify({ error: { message: "Invalid or oversized standalone search request", type: "bridge_search_error" } }));
      }
      request.resume();
      return;
    }
    if (request.aborted || response.destroyed) return;
  }

  const live = isLiveRequest(request.url);
  const target = live ? liveBase : upstreamBase;
  const transport = live ? liveTransport : upstreamTransport;
  const agent = live ? liveAgent : upstreamAgent;
  let headers;
  try {
    headers = requestHeaders(request.headers, target, live);
    if (searchBody) headers["content-length"] = String(searchBody.length);
  } catch (error) {
    response.writeHead(503, { "content-type": "application/json" });
    response.end(JSON.stringify({ error: { message: error.message, type: "bridge_auth_error" } }));
    return;
  }

  const upstreamRequest = transport.request({
    protocol: target.protocol,
    hostname: target.hostname,
    port: target.port || undefined,
    method: request.method,
    path: targetPath(target, request.url),
    headers,
    agent,
  }, (upstreamResponse) => {
    forwardResponse(request, response, upstreamResponse);
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
  if (searchBody) upstreamRequest.end(searchBody);
  else request.pipe(upstreamRequest);
});

server.listen(listenPort, listenHost, () => {
  process.stdout.write(`Codex YEUTECH auth bridge listening on http://${listenHost}:${listenPort}\n`);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => server.close(() => process.exit(0)));
}
