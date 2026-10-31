#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import http from "node:http";
import https from "node:https";
import { randomUUID } from "node:crypto";
import { isCodexStreamingResponse, rateLimitDisplay } from "./codex-yeutech-errors.mjs";
import { isCodexStandaloneSearch, readSearchBody, routeStandaloneSearch } from "./codex-yeutech-search.mjs";
import { isImageRequest, imageBodyLimit, normalizeClaudeImages } from "./codex-yeutech-images.mjs";
import { isResponsesBody, readResponseBody, normalizeProxyCompaction } from "./codex-yeutech-compaction.mjs";

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
let preparingImages = 0;
let preparingResponses = 0;
let preparingLargeResponses = 0;

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
    response.end(JSON.stringify({ ok: true, version: "20261031-budgeted-history-v4", activeRequests, preparingImages, preparingResponses, upstream: upstreamBase.origin, liveUpstream: liveBase.origin }));
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
  let responseBody;
  let bodyChanged = false;
  if (isResponsesBody(request)) {
    // Two bounded preprocessing slots, at most one large/encoded/unknown body,
    // independent of long-running inference.
    // Never bypass capsule validation under load. Clients may retry safely.
    const length = Number(request.headers["content-length"]);
    const large = !Number.isSafeInteger(length) || length > imageBodyLimit
      || Boolean(request.headers["content-encoding"] && request.headers["content-encoding"] !== "identity");
    if (preparingResponses >= 2 || (large && preparingLargeResponses >= 1)) {
      response.writeHead(503, { "content-type": "application/json", "retry-after": "1" });
      response.end(JSON.stringify({ error: { message: "请求预处理繁忙，请稍后重试；历史记录未修改。", type: "bridge_request_error", code: "responses_preprocessing_busy" } }));
      request.resume(); return;
    }
    preparingResponses += 1;
    if (large) preparingLargeResponses += 1;
    const cancellation = new AbortController();
    const cancel = () => cancellation.abort();
    response.once("close", cancel);
    request.once("aborted", cancel);
    try {
      const original = await readResponseBody(request, { signal: cancellation.signal });
      if (request.headers["thread-id"] && request.headers["session-id"] && request.headers["thread-id"] !== request.headers["session-id"]) {
        throw Object.assign(new Error("foreign_history_thread_conflict"), { code: "foreign_history_thread_conflict" });
      }
      responseBody = await normalizeProxyCompaction(original, { encoding: request.headers["content-encoding"] || "identity", signal: cancellation.signal,
        threadId: request.headers["thread-id"] || request.headers["session-id"], sessionDirectory: process.env.YEUTECH_CODEX_SESSIONS_DIR });
      if (isImageRequest(request) && original.length <= imageBodyLimit) {
        preparingImages += 1;
        try { responseBody = await normalizeClaudeImages(responseBody, { signal: cancellation.signal }); }
        catch { throw Object.assign(new Error("image_preprocessing_failed"), { code: "image_preprocessing_failed" }); }
        finally { preparingImages -= 1; }
      }
      bodyChanged = responseBody !== original;
    } catch (error) {
      if (!response.destroyed) {
        response.writeHead(error.statusCode || 400, { "content-type": "application/json" });
        response.end(JSON.stringify({ error: { message: error.code === "image_preprocessing_failed"
          ? "无法处理发往 Claude 的超限图片；原图与历史记录未修改。"
          : error.code === "context_budget_current_input_too_large"
          ? "当前指令或未完成工具结果超出目标模型的保守上下文预算，未截断本轮输入；完整历史仍保留。"
          : error.code === "context_budget_history_unavailable"
          ? "恢复内容超过目标模型的上下文预算，但未找到可核验的原会话记录；未删除或截断历史。"
          : "无法安全转换请求中的压缩上下文，请检查请求格式或大小；原会话历史未修改。",
          type: "invalid_request_error", code: error.code || "responses_preprocessing_failed" } }));
      }
      request.resume();
      return;
    } finally {
      preparingResponses -= 1;
      if (large) preparingLargeResponses -= 1;
      response.off("close", cancel); request.off("aborted", cancel);
    }
    if (request.aborted || response.destroyed) return;
  }
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
    if (responseBody) headers["content-length"] = String(responseBody.length);
    if (bodyChanged) for (const name of ["content-encoding", "content-md5", "digest", "content-digest"]) delete headers[name];
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
  else if (responseBody) upstreamRequest.end(responseBody);
  else request.pipe(upstreamRequest);
});

// The built-in OpenAI provider probes Responses WebSocket before HTTP/SSE.
// Without an upgrade listener, Node treats that handshake as an ordinary GET;
// stripping Upgrade and forwarding it causes an upstream 400 (a fatal turn).
// Codex recognizes 426 as an unsupported WebSocket transport and falls back to
// POST/SSE. Reject locally, before acquiring or forwarding any credential.
server.on("upgrade", (request, socket) => {
  const responses = request.method === "GET"
    && new URL(request.url || "/", "http://127.0.0.1").pathname === "/v1/responses";
  socket.on("error", () => {});
  socket.end(`HTTP/1.1 ${responses ? "426 Upgrade Required" : "404 Not Found"}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n`);
});

server.listen(listenPort, listenHost, () => {
  process.stdout.write(`Codex YEUTECH auth bridge listening on http://${listenHost}:${listenPort}\n`);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => server.close(() => process.exit(0)));
}
