import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import http from "node:http";
import test from "node:test";
import { fileURLToPath } from "node:url";

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
