import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { once } from "node:events";
import http from "node:http";
import { fileURLToPath } from "node:url";
import { zstdCompressSync, zstdDecompressSync } from "node:zlib";
import test from "node:test";

async function listen(server) {
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  return server.address().port;
}

async function fixture(t, handler, env = {}) {
  const upstream = http.createServer(handler);
  const upstreamPort = await listen(upstream);
  t.after(() => { upstream.closeAllConnections(); upstream.close(); });
  const reservation = http.createServer();
  const port = await listen(reservation);
  await new Promise(resolve => reservation.close(resolve));
  const child = spawn(process.execPath, [fileURLToPath(new URL("./codex-yeutech-auth-bridge.mjs", import.meta.url))], {
    env: { ...process.env, YEUTECH_CODEX_BRIDGE_PORT: String(port),
      YEUTECH_UPSTREAM_BASE_URL: `http://127.0.0.1:${upstreamPort}`,
      YEUTECH_UPSTREAM_BEARER_TOKEN: "fixture-gateway", ...env },
    stdio: "ignore",
  });
  t.after(async () => { if (child.exitCode === null) { child.kill("SIGTERM"); await once(child, "exit"); } });
  for (let n = 0; ; n++) {
    try { if ((await fetch(`http://127.0.0.1:${port}/_yeutech/health`)).ok) break; } catch {}
    assert.ok(n < 100, "bridge ready");
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  return { port, child };
}

function upgrade(port, path) {
  return new Promise((resolve, reject) => {
    const req = http.request({ hostname: "127.0.0.1", port, path,
      headers: { connection: "Upgrade", upgrade: "websocket", "sec-websocket-version": "13",
        "sec-websocket-key": "Zml4dHVyZV9vbmx5X2tleQ==", authorization: "Bearer fixture-oauth" } }, res => {
      res.resume();
      res.on("end", () => resolve({ status: res.statusCode, version: res.httpVersion }));
    });
    req.on("error", reject);
    req.end();
  });
}

test("WebSocket probe falls back locally without forwarding auth or looking up a key", async t => {
  let calls = 0;
  const { port } = await fixture(t, (_req, res) => { calls++; res.writeHead(500).end(); }, { YEUTECH_UPSTREAM_BEARER_TOKEN: "" });
  for (const path of ["/v1/responses", "/v1/responses?probe=1"]) {
    assert.deepEqual(await upgrade(port, path), { status: 426, version: "1.1" });
  }
  assert.equal((await upgrade(port, "/v1/live")).status, 404);
  assert.equal(calls, 0);
  const health = await (await fetch(`http://127.0.0.1:${port}/_yeutech/health`)).json();
  assert.equal(health.activeRequests, 0);
});

test("zstd POST and upstream admission errors remain unchanged after fallback", async t => {
  const payload = Buffer.from(JSON.stringify({ model: "gemini-4-argon-high", input: "fixture", stream: true, service_tier: "priority" }));
  const compressed = zstdCompressSync(payload);
  let calls = 0;
  const error = '{"error":{"code":"proxy_memory_busy","type":"server_error"}}';
  const { port } = await fixture(t, async (req, res) => {
    calls++;
    const chunks = []; for await (const chunk of req) chunks.push(chunk);
    assert.equal(req.method, "POST");
    assert.equal(req.headers.authorization, "Bearer fixture-gateway");
    assert.equal(req.headers["content-encoding"], "zstd");
    assert.deepEqual(Buffer.concat(chunks), compressed);
    res.writeHead(503, { "content-type": "application/json", "retry-after": "2" }).end(error);
  });
  assert.equal((await upgrade(port, "/v1/responses")).status, 426);
  const response = await fetch(`http://127.0.0.1:${port}/v1/responses`, { method: "POST",
    headers: { "content-type": "application/json", "content-encoding": "zstd", authorization: "Bearer fixture-oauth" }, body: compressed });
  assert.equal(response.status, 503);
  assert.equal(response.headers.get("retry-after"), "2");
  assert.equal(await response.text(), error);
  assert.equal(calls, 1);
});

test("cancellation releases upstream HTTP request and bridge drain counter", { timeout: 10000 }, async t => {
  let ended;
  const closed = new Promise(resolve => ended = resolve);
  const { port } = await fixture(t, (req, res) => {
    req.resume();
    res.writeHead(200, { "content-type": "text/event-stream" }); res.write("data: {}\n\n");
    res.on("close", ended);
  });
  const abort = new AbortController();
  const response = await fetch(`http://127.0.0.1:${port}/v1/responses`, { method: "POST", body: "{}", signal: abort.signal });
  await response.body.getReader().read();
  assert.equal((await (await fetch(`http://127.0.0.1:${port}/_yeutech/health`)).json()).activeRequests, 1);
  abort.abort(); await closed;
  assert.equal((await (await fetch(`http://127.0.0.1:${port}/_yeutech/health`)).json()).activeRequests, 0);
});

test("SIGTERM drains an in-flight SSE response before exiting", { timeout: 10000 }, async t => {
  let finish;
  const { port, child } = await fixture(t, (req, res) => {
    req.resume();
    res.writeHead(200, { "content-type": "text/event-stream" }); res.write("data: begin\n\n");
    finish = () => res.end("data: end\n\n");
  });
  const response = await fetch(`http://127.0.0.1:${port}/v1/responses`, { method: "POST", body: "{}" });
  const exit = once(child, "exit");
  child.kill("SIGTERM");
  assert.equal(child.exitCode, null);
  finish();
  assert.equal(await response.text(), "data: begin\n\ndata: end\n\n");
  assert.equal((await exit)[0], 0);
});

test("installed built-in provider completes HTTP/SSE through bridge with both encodings", { skip: !process.env.CODEX_TEST_BIN, timeout: 90000 }, async t => {
  for (const compression of [true, false]) {
    await t.test(`compression=${compression}`, { timeout: 40000 }, async t => {
      const seen = [];
      const { port } = await fixture(t, async (req, res) => {
        const chunks = []; for await (const chunk of req) chunks.push(chunk);
        const bytes = Buffer.concat(chunks);
        const body = JSON.parse(req.headers["content-encoding"] === "zstd" ? zstdDecompressSync(bytes) : bytes);
        seen.push({ method: req.method, path: req.url, body, encoding: req.headers["content-encoding"], gatewayAuth: req.headers.authorization === "Bearer fixture-gateway" });
        const item = { type: "message", id: "msg_fixture", role: "assistant", status: "completed", content: [{ type: "output_text", text: "OK", annotations: [] }] };
        const response = { id: "resp_fixture", object: "response", status: "completed", model: body.model, output: [item], usage: { input_tokens: 10, output_tokens: 1, total_tokens: 11 } };
        const events = [{ type: "response.created", response: { ...response, status: "in_progress", output: [] } }, { type: "response.output_item.added", output_index: 0, item }, { type: "response.output_item.done", output_index: 0, item }, { type: "response.completed", response }];
        res.writeHead(200, { "content-type": "text/event-stream" }).end(events.map(event => `data: ${JSON.stringify(event)}\n\n`).join(""));
      });
      const child = spawn(process.env.CODEX_TEST_BIN, ["-c", 'model_provider="openai"', "-c", `openai_base_url="http://127.0.0.1:${port}/v1"`, "-c", `features.enable_request_compression=${compression}`, "app-server"], { stdio: ["pipe", "pipe", "ignore"] });
      t.after(async () => { if (child.exitCode === null) { child.kill("SIGTERM"); await once(child, "exit"); } });
      const pending = new Map(); let sequence = 0, buffer = "", complete;
      const completed = new Promise(resolve => complete = resolve);
      child.stdout.on("data", data => {
        buffer += data;
        let end;
        while ((end = buffer.indexOf("\n")) >= 0) {
          const line = buffer.slice(0, end); buffer = buffer.slice(end + 1);
          let msg; try { msg = JSON.parse(line); } catch { continue; }
          if (pending.has(msg.id)) { pending.get(msg.id)(msg); pending.delete(msg.id); }
          if (msg.method === "turn/completed") complete(msg.params.turn);
        }
      });
      const rpc = (method, params) => new Promise(resolve => {
        const id = ++sequence; pending.set(id, resolve);
        child.stdin.write(JSON.stringify({ id, method, params }) + "\n");
      });
      await rpc("initialize", { clientInfo: { name: "yeutech_transport_test", version: "1" }, capabilities: { experimentalApi: true } });
      child.stdin.write('{"method":"initialized"}\n');
      const config = await rpc("config/read", {});
      assert.equal(config.result.config.model_provider, "openai");
      const account = await rpc("account/read", { refreshToken: false });
      assert.equal(account.result.requiresOpenaiAuth, true);
      const thread = await rpc("thread/start", { model: "gemini-4-argon-high", modelProvider: "openai", cwd: "/tmp", ephemeral: true, approvalPolicy: "never", sandbox: "read-only", baseInstructions: "Reply only OK. Do not call tools.", developerInstructions: "Transport fixture. Do not inspect files or use tools." });
      assert.ok(thread.result?.thread?.id, "ephemeral thread created");
      await rpc("turn/start", { threadId: thread.result.thread.id, input: [{ type: "text", text: "Reply only OK." }] });
      const turn = await completed;
      assert.equal(turn.status, "completed");
      assert.equal(seen.length, 1, "no upstream GET, duplicate inference, or retry");
      assert.equal(seen[0].method, "POST");
      assert.equal(seen[0].path, "/v1/responses");
      assert.equal(seen[0].body.model, "gemini-4-argon-high");
      assert.equal(seen[0].encoding, compression ? "zstd" : undefined);
      assert.equal(seen[0].gatewayAuth, true, "only gateway credential may reach fixture upstream");
      assert.equal((await (await fetch(`http://127.0.0.1:${port}/_yeutech/health`)).json()).activeRequests, 0);
    });
  }
});
