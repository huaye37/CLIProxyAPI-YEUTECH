import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import http from "node:http";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { isCodexStreamingResponse, rateLimitDisplay } from "./codex-yeutech-errors.mjs";

async function listen(server) {
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  return server.address().port;
}

async function waitForHealth(port) {
  for (let attempt = 0; attempt < 50; attempt += 1) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/_yeutech/health`);
      if (response.ok) return;
    } catch {}
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  throw new Error("bridge health endpoint did not become ready");
}

test("strips ChatGPT auth and preserves the Fast request payload", async (t) => {
  let observed;
  const upstream = http.createServer((request, response) => {
    const chunks = [];
    request.on("data", (chunk) => chunks.push(chunk));
    request.on("end", () => {
      observed = {
        authorization: request.headers.authorization,
        consumer: request.headers["x-yeutech-consumer"],
        path: request.url,
        body: JSON.parse(Buffer.concat(chunks).toString("utf8")),
      };
      response.writeHead(200, { "content-type": "application/json" });
      response.end('{"service_tier":"priority"}');
    });
  });
  const upstreamPort = await listen(upstream);
  t.after(() => upstream.close());

  const bridgePort = upstreamPort + 1;
  const child = spawn(process.execPath, [fileURLToPath(new URL("./codex-yeutech-auth-bridge.mjs", import.meta.url))], {
    env: {
      ...process.env,
      YEUTECH_CODEX_BRIDGE_PORT: String(bridgePort),
      YEUTECH_UPSTREAM_BASE_URL: `http://127.0.0.1:${upstreamPort}`,
      YEUTECH_UPSTREAM_BEARER_TOKEN: "gateway-token",
    },
    stdio: ["ignore", "ignore", "inherit"],
  });
  t.after(() => child.kill("SIGTERM"));
  await waitForHealth(bridgePort);

  const response = await fetch(`http://127.0.0.1:${bridgePort}/v1/responses?trace=1`, {
    method: "POST",
    headers: {
      authorization: "Bearer chatgpt-oauth-token",
      "content-type": "application/json",
      "x-yeutech-consumer": "codex-desktop",
    },
    body: JSON.stringify({ model: "gpt-6.1-sol", service_tier: "fast", input: "ok" }),
  });

  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { service_tier: "priority" });
  assert.deepEqual(observed, {
    authorization: "Bearer gateway-token",
    consumer: "codex-desktop",
    path: "/v1/responses?trace=1",
    body: { model: "gpt-6.1-sol", service_tier: "fast", input: "ok" },
  });
});

test("routes Codex Live directly with ChatGPT auth and preserves Location", async (t) => {
  let gatewayCalled = false;
  const gateway = http.createServer((_request, response) => {
    gatewayCalled = true;
    response.writeHead(500).end();
  });
  const gatewayPort = await listen(gateway);
  t.after(() => gateway.close());

  let observed;
  const live = http.createServer((request, response) => {
    const chunks = [];
    request.on("data", (chunk) => chunks.push(chunk));
    request.on("end", () => {
      observed = {
        authorization: request.headers.authorization,
        account: request.headers["chatgpt-account-id"],
        consumer: request.headers["x-yeutech-consumer"],
        contentType: request.headers["content-type"],
        path: request.url,
        body: Buffer.concat(chunks).toString("utf8"),
      };
      response.writeHead(201, {
        "content-type": "application/sdp",
        location: "/v1/live/rtc_test",
      });
      response.end("v=answer\r\n");
    });
  });
  const livePort = await listen(live);
  t.after(() => live.close());

  const bridgePort = livePort + 1;
  const child = spawn(process.execPath, [fileURLToPath(new URL("./codex-yeutech-auth-bridge.mjs", import.meta.url))], {
    env: {
      ...process.env,
      YEUTECH_CODEX_BRIDGE_PORT: String(bridgePort),
      YEUTECH_UPSTREAM_BASE_URL: `http://127.0.0.1:${gatewayPort}`,
      OPENAI_LIVE_BASE_URL: `http://127.0.0.1:${livePort}`,
      YEUTECH_UPSTREAM_BEARER_TOKEN: "gateway-token",
    },
    stdio: ["ignore", "ignore", "inherit"],
  });
  t.after(() => child.kill("SIGTERM"));
  await waitForHealth(bridgePort);

  const response = await fetch(`http://127.0.0.1:${bridgePort}/v1/live`, {
    method: "POST",
    headers: {
      authorization: "Bearer chatgpt-oauth-token",
      "chatgpt-account-id": "account-id",
      "content-type": "multipart/form-data; boundary=test-boundary",
      "x-yeutech-consumer": "codex-desktop",
    },
    body: "--test-boundary--\r\n",
  });

  assert.equal(response.status, 201);
  assert.equal(response.headers.get("location"), "/v1/live/rtc_test");
  assert.equal(await response.text(), "v=answer\r\n");
  assert.equal(gatewayCalled, false);
  assert.deepEqual(observed, {
    authorization: "Bearer chatgpt-oauth-token",
    account: "account-id",
    consumer: "codex-desktop",
    contentType: "multipart/form-data; boundary=test-boundary",
    path: "/v1/live",
    body: "--test-boundary--\r\n",
  });
});

test("quota, cooldown, concurrency and ordinary 429 remain distinct", () => {
  const now = Date.parse("2026-10-15T17:25:00Z");
  const cases = [
    ["insufficient_quota", "", "quota_exhausted", "可用额度已耗尽"],
    ["usage_limit_reached", "", "quota_exhausted", "可用额度已耗尽"],
    ["model_cooldown", "All credentials are cooling down", "model_cooldown", "上游限额冷却期"],
    ["concurrency_limit_exceeded", "", "concurrency_limit", "并发请求已满"],
    ["rate_limit_error", "Usage credits are required for fast mode", "speed_entitlement", "切回标准速度"],
    ["rate_limit_exceeded", "Too many requests", "rate_limit", "限制请求频率"],
  ];
  for (const [code, message, reason, expected] of cases) {
    const result = rateLimitDisplay(JSON.stringify({ error: { code, message, model: "claude-opus-5-5-tokyo" } }), { "retry-after": "1500" }, now);
    assert.equal(result.reason, reason);
    assert.ok(result.message.includes(expected));
    assert.match(result.message, /Claude · 东京/);
    assert.match(result.message, /01:50:00（北京时间）/);
    assert.equal(result.retryAfterSeconds, 1500);
    if (reason !== "quota_exhausted") assert.ok(!result.message.includes("额度已耗尽"));
  }
});

test("display uses bounded retry advice and never forwards sensitive upstream text", () => {
  const now = Date.parse("2026-10-15T17:25:00Z");
  const body = JSON.stringify({ error: { code: "model_cooldown", reset_seconds: 120,
    message: "Authorization: Bearer secret-value; /private/account.json person@example.com",
    model: "malicious account secret" } });
  const result = rateLimitDisplay(body, {}, now);
  assert.equal(result.retryAfterSeconds, 120);
  assert.match(result.message, /^模型服务：/);
  assert.doesNotMatch(result.message, /secret|private|example|Authorization|malicious/);
  assert.equal(rateLimitDisplay(body, { "retry-after": "Thu, 15 Oct 2026 17:50:00 GMT" }, now).retryAfterSeconds, 1500);
  for (const reset_seconds of [null, -1, "60", 99999999]) {
    const unknown = rateLimitDisplay(JSON.stringify({ error: { code: "model_cooldown", reset_seconds } }), {}, now);
    assert.equal(unknown.resetsAt, null);
    assert.doesNotMatch(unknown.message, /北京时间/);
  }
  assert.equal(rateLimitDisplay("<html>429</html>", {}, now), null);
  assert.equal(rateLimitDisplay('{"error":null}', {}, now), null);
});

test("only official Codex streaming Responses requests opt into the display adapter", () => {
  const request = { method: "POST", url: "/v1/responses?trace=1", headers: { accept: "text/event-stream", originator: "Codex Desktop" } };
  assert.equal(isCodexStreamingResponse(request), true);
  assert.equal(isCodexStreamingResponse({ ...request, method: "GET" }), false);
  assert.equal(isCodexStreamingResponse({ ...request, url: "/v1/chat/completions" }), false);
  assert.equal(isCodexStreamingResponse({ ...request, headers: { accept: "application/json", originator: "Codex Desktop" } }), false);
  assert.equal(isCodexStreamingResponse({ ...request, headers: { accept: "text/event-stream", originator: "other-client" } }), false);
});

async function errorFixture(t, status, body, extraHeaders = {}) {
  const upstream = http.createServer((request, response) => {
    request.resume();
    response.writeHead(status, { "content-type": "application/json", "content-length": Buffer.byteLength(body), ...extraHeaders });
    response.end(body);
  });
  const upstreamPort = await listen(upstream);
  t.after(() => upstream.close());
  const reservation = http.createServer();
  const bridgePort = await listen(reservation);
  await new Promise(resolve => reservation.close(resolve));
  const child = spawn(process.execPath, [fileURLToPath(new URL("./codex-yeutech-auth-bridge.mjs", import.meta.url))], {
    env: { ...process.env, YEUTECH_CODEX_BRIDGE_PORT: String(bridgePort), YEUTECH_UPSTREAM_BASE_URL: `http://127.0.0.1:${upstreamPort}`, YEUTECH_UPSTREAM_BEARER_TOKEN: "fixture-only-token" },
    stdio: ["ignore", "ignore", "inherit"],
  });
  t.after(() => child.kill("SIGTERM"));
  await waitForHealth(bridgePort);
  return (headers = {}, path = "/v1/responses") => fetch(`http://127.0.0.1:${bridgePort}${path}`, {
    method: "POST", headers: { "content-type": "application/json", accept: "text/event-stream", originator: "Codex Desktop", ...headers },
    body: JSON.stringify({ model: "claude-opus-5-5-tokyo", input: "fixture", stream: true }),
  });
}

test("Codex receives one failed event with retry advice and no fabricated model output", async (t) => {
  const request = await errorFixture(t, 429, JSON.stringify({ error: { code: "model_cooldown", model: "claude-opus-5-5-tokyo" } }), { "retry-after": "120", etag: "old-body" });
  const response = await request();
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("x-yeutech-upstream-status"), "429");
  assert.match(response.headers.get("content-type"), /text\/event-stream/);
  assert.equal(response.headers.get("etag"), null);
  const body = await response.text();
  assert.equal((body.match(/event: response.failed/g) || []).length, 1);
  assert.doesNotMatch(body, /response.completed|output_text|fixture-only-token/);
  const event = JSON.parse(body.split("data: ")[1]);
  assert.equal(event.response.status, "failed");
  assert.deepEqual(event.response.output, []);
  assert.equal(event.response.error.reason, "model_cooldown");
  assert.equal(event.response.error.upstream_status, 429);
  assert.equal(event.response.error.retry_after_seconds, 120);
});

test("other clients, non-streaming calls and other endpoints retain HTTP 429", async (t) => {
  const body = '{"error":{"code":"model_cooldown","message":"original"}}';
  const request = await errorFixture(t, 429, body, { "retry-after": "10" });
  for (const [headers, path] of [[{ originator: "other" }, "/v1/responses"], [{ accept: "application/json" }, "/v1/responses"], [{}, "/v1/chat/completions"]]) {
    const response = await request(headers, path);
    assert.equal(response.status, 429);
    assert.equal(await response.text(), body);
  }
});

test("malformed and oversized error bodies pass through without truncation", async (t) => {
  for (const body of ["<html>upstream rejected</html>", JSON.stringify({ error: { message: "x".repeat(100000) } })]) {
    await t.test(String(body.length), async (t) => {
      const request = await errorFixture(t, 429, body);
      const response = await request();
      assert.equal(response.status, 429);
      assert.equal(await response.text(), body);
    });
  }
});

test("authentication and service failures preserve status and body", async (t) => {
  for (const status of [401, 403, 503]) {
    await t.test(String(status), async (t) => {
      const body = '{"error":{"message":"unchanged"}}';
      const request = await errorFixture(t, status, body);
      const response = await request();
      assert.equal(response.status, status);
      assert.equal(await response.text(), body);
    });
  }
});
