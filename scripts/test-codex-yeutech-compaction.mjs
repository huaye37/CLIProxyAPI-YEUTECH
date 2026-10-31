import assert from "node:assert/strict";
import { createCipheriv, createHash, randomBytes } from "node:crypto";
import { spawn } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, mkdir, rm, readFile, writeFile, chmod, symlink } from "node:fs/promises";
import { tmpdir, homedir } from "node:os";
import { join } from "node:path";
import http from "node:http";
import { PassThrough, Readable } from "node:stream";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { gzipSync, deflateSync, brotliCompressSync, zstdCompressSync, zstdDecompressSync } from "node:zlib";
import { isResponsesBody, readResponseBody, normalizeProxyCompaction } from "./codex-yeutech-compaction.mjs";

function capsule(clear = JSON.stringify({ summary: "Keep project A; next step B. 中文摘要。", model: "claude-opus-5-5-tokyo", created_at: 1793242926 })) {
  const nonce = randomBytes(12);
  const cipher = createCipheriv("aes-256-gcm", createHash("sha256").update("CLIProxyAPI").digest(), nonce);
  const data = Buffer.concat([nonce, cipher.update(clear), cipher.final(), cipher.getAuthTag()]);
  return { type: "compaction", id: "cmp_ag_compact_fixture", encrypted_content: `cpa-ag-compact-v1:${data.toString("base64url")}` };
}
const native = { type: "compaction", id: "cmp_native", encrypted_content: "opaque-openai-native" };
const reasoning = { type: "reasoning", id: "rs_fixture", encrypted_content: "opaque-native-reasoning", summary: [] };
const json = obj => Buffer.from(JSON.stringify(obj));
const payload = () => ({ model: "gpt-6-astra", stream: true, reasoning: { effort: "high" }, service_tier: "priority",
  input: [{ role: "developer", content: "Unchanged application instructions." }, capsule(), native, reasoning,
    { type: "function_call", name: "fixture", call_id: "call_1", arguments: "{}" },
    { type: "function_call_output", call_id: "call_1", output: "Keep tool result." }, { role: "user", content: "Continue." }] });
const encoders = { identity: b => b, gzip: gzipSync, deflate: deflateSync, br: brotliCompressSync, zstd: zstdCompressSync };

test("select only JSON Responses and compact POST paths, not search or Live", () => {
  const request = { method: "POST", url: "/v1/responses?x=1", headers: { "content-type": "application/json; charset=utf-8", "content-encoding": "zstd" } };
  assert.equal(isResponsesBody(request), true);
  assert.equal(isResponsesBody({ ...request, url: "/v1/responses/compact" }), true);
  for (const url of ["/v1/live", "/v1/chat/completions", "/v1/alpha/search"]) assert.equal(isResponsesBody({ ...request, url }), false);
  assert.equal(isResponsesBody({ ...request, method: "GET" }), false);
  assert.equal(isResponsesBody({ ...request, headers: {} }), false);
});

test("GPT switch expands only CPA capsules, retaining order, tools, native state and speed", async () => {
  const original = payload();
  original.input.push(capsule(JSON.stringify({ summary: "Second summary." })));
  const body = json(original), before = Buffer.from(body);
  const result = await normalizeProxyCompaction(body);
  assert.deepEqual(body, before);
  const actual = JSON.parse(result);
  assert.deepEqual({ ...actual, input: null }, { ...original, input: null });
  assert.equal(actual.input.length, original.input.length);
  for (const index of [0, 2, 3, 4, 5, 6]) assert.deepEqual(actual.input[index], original.input[index]);
  for (const index of [1, 7]) {
    assert.equal(actual.input[index].role, "user");
    assert.equal(actual.input[index].type, "message");
    assert.equal(actual.input[index].content[0].type, "input_text");
    assert.equal(actual.input[index].encrypted_content, undefined);
    assert.equal(actual.input[index].id, undefined);
  }
  assert.match(actual.input[1].content[0].text, /Keep project A; next step B. 中文摘要。$/);
  assert.match(actual.input[7].content[0].text, /Second summary\.$/);
  assert.equal(await normalizeProxyCompaction(result), result, "idempotent on repeated compaction");
});

test("native state, unrelated fields and CPA-compatible requests remain byte-identical", async () => {
  for (const value of [
    { ...payload(), input: [native, reasoning] },
    { ...payload(), model: "claude-opus-5-5-tokyo" },
    { ...payload(), model: "gemini-4-argon" },
    { ...payload(), model: "grok-4.7-build-fast", input: [native, reasoning] },
    { model: "gpt-6.1-sol", input: "Plain text." },
    { model: "gpt-6.1-sol", input: [{ type: "message", role: "user", content: "cpa-ag-compact-v1: user quoted text" }] },
    null,
  ]) {
    for (const [encoding, encode] of Object.entries(encoders)) {
      const wire = encode(json(value));
      assert.equal(await normalizeProxyCompaction(wire, { encoding }), wire);
    }
  }
});

test("identity, gzip, deflate, Brotli and zstd all convert the same summary", async () => {
  const body = json(payload());
  for (const [encoding, encode] of Object.entries(encoders)) {
    assert.deepEqual(await normalizeProxyCompaction(encode(body), { encoding }), await normalizeProxyCompaction(body));
  }
});

test("damaged or unsupported CPA capsules fail closed without disclosing context", async () => {
  const valid = capsule();
  const damaged = valid.encrypted_content.slice(0, -3) + "AAA";
  const items = [
    { ...valid, encrypted_content: damaged },
    { ...valid, encrypted_content: "cpa-ag-compact-v1:????" },
    { ...valid, encrypted_content: "cpa-ag-compact-v2:future" },
    { ...valid, encrypted_content: "cpa-ag-compact-v1:YQ" },
    { ...valid, encrypted_content: undefined },
    { ...valid, encrypted_content: valid.encrypted_content + "=" },
    capsule("private-invalid-json"), capsule('{"summary":""}'), capsule('{"summary":42}'), capsule("null"),
    capsule('{"summary":"  "}'), capsule(Buffer.from([0xff])),
  ];
  for (const item of items) {
    await assert.rejects(normalizeProxyCompaction(json({ model: "gpt-6.1-sol", input: [item] })), error => {
      assert.equal(error.code, "proxy_compaction_invalid");
      assert.equal(error.message, "proxy_compaction_invalid");
      assert.equal(error.statusCode, 400);
      return true;
    });
  }
});

test("bounded wire/decompression, malformed encoding, cancellation and chunked input", async () => {
  assert.equal((await readResponseBody(Readable.from([Buffer.from("ab"), Buffer.from("cd")]), { limit: 4 })).toString(), "abcd");
  await assert.rejects(readResponseBody(Readable.from([Buffer.from("abcde")]), { limit: 4 }), { statusCode: 413 });
  const body = json({ ...payload(), padding: "x".repeat(10000) });
  for (const [encoding, encode] of Object.entries(encoders)) {
    await assert.rejects(normalizeProxyCompaction(encode(body), { encoding, limit: 1000 }), { statusCode: 413 });
  }
  await assert.rejects(normalizeProxyCompaction(json(payload()), { encoding: "gzip, br" }), { statusCode: 415 });
  await assert.rejects(normalizeProxyCompaction(Buffer.from("private-invalid-zip"), { encoding: "gzip" }), { code: "responses_encoding_invalid" });
  const signal = new AbortController();
  const stream = new PassThrough();
  const pending = readResponseBody(stream, { signal: signal.signal });
  signal.abort();
  await assert.rejects(pending, { code: "request_aborted" });
  assert.equal(stream.listenerCount("data"), 0);
  stream.destroy();
  await assert.rejects(normalizeProxyCompaction(json(payload()), { signal: signal.signal }), { name: "AbortError" });
});

async function listen(server) {
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  return server.address().port;
}
async function fixture(t, handler, env = {}) {
  const seen = [];
  const server = http.createServer(async (request, response) => {
    const chunks = []; for await (const chunk of request) chunks.push(chunk);
    const record = { path: request.url, headers: request.headers, body: Buffer.concat(chunks) };
    seen.push(record);
    if (handler) { handler(record, response); return; }
    response.writeHead(200, { "content-type": "application/json" }).end('{"ok":true}');
  });
  const port = await listen(server);
  t.after(() => { server.closeAllConnections(); server.close(); });
  const reservation = http.createServer(), bridgePort = await listen(reservation);
  await new Promise(resolve => reservation.close(resolve));
  const child = spawn(process.execPath, [fileURLToPath(new URL("./codex-yeutech-auth-bridge.mjs", import.meta.url))], {
    env: { ...process.env, YEUTECH_CODEX_BRIDGE_PORT: String(bridgePort), YEUTECH_UPSTREAM_BASE_URL: `http://127.0.0.1:${port}`, YEUTECH_UPSTREAM_BEARER_TOKEN: "fixture-gateway", ...env }, stdio: "ignore",
  });
  t.after(() => child.kill("SIGTERM"));
  const base = `http://127.0.0.1:${bridgePort}`;
  for (let n = 0; ; n++) {
    try { if ((await fetch(`${base}/_yeutech/health`)).ok) break; } catch {}
    assert.ok(n < 100, "bridge ready"); await new Promise(r => setTimeout(r, 20));
  }
  return { seen, base, bridgePort };
}

test("bridge converts stream/nonstream and compact bodies, corrects framing, preserves native bytes", async t => {
  const { seen, base } = await fixture(t);
  for (const path of ["/v1/responses?trace=1", "/v1/responses/compact"]) {
    for (const stream of [true, false]) {
      for (const [encoding, encode] of Object.entries(encoders)) {
        const body = encode(json({ ...payload(), stream }));
        const response = await fetch(base + path, { method: "POST", headers: { "content-type": "application/json", "content-encoding": encoding,
          authorization: "Bearer fixture-oauth", digest: "old", "content-md5": "old", "content-digest": "old" }, body });
        assert.equal(response.status, 200);
        await response.text();
        const request = seen.at(-1);
        assert.equal(request.path, path);
        assert.equal(request.headers.authorization, "Bearer fixture-gateway");
        assert.equal(Number(request.headers["content-length"]), request.body.length);
        for (const key of ["content-encoding", "content-md5", "digest", "content-digest"]) assert.equal(request.headers[key], undefined);
        assert.match(JSON.parse(request.body).input[1].content[0].text, /next step B/);
        assert.equal(JSON.parse(request.body).stream, stream);
      }
    }
  }
  const raw = zstdCompressSync(json({ ...payload(), input: [native, reasoning] }));
  const response = await fetch(base + "/v1/responses", { method: "POST", headers: { "content-type": "application/json", "content-encoding": "zstd", digest: "original" }, body: raw });
  await response.text();
  assert.deepEqual(seen.at(-1).body, raw);
  assert.equal(seen.at(-1).headers.digest, "original");
  assert.equal(seen.at(-1).headers["content-encoding"], "zstd");
  const count = seen.length;
  const failed = await fetch(base + "/v1/responses/compact", { method: "POST", headers: { "content-type": "application/json" },
    body: JSON.stringify({ model: "gpt-6-astra", input: [{ ...capsule(), encrypted_content: "cpa-ag-compact-v1:private-invalid" }] }) });
  assert.equal(failed.status, 400);
  assert.doesNotMatch(await failed.text(), /private-invalid/);
  assert.equal(seen.length, count, "corruption never reaches upstream");
});

test("busy admission never bypasses conversion; abort releases slot and chunked retries succeed", async t => {
  const { base, bridgePort, seen } = await fixture(t);
  const held = http.request({ hostname: "127.0.0.1", port: bridgePort, path: "/v1/responses", method: "POST", headers: { "content-type": "application/json" } });
  held.on("error", () => {}); held.write('{"model":');
  t.after(() => held.destroy());
  for (let n = 0; ; n++) {
    const health = await (await fetch(base + "/_yeutech/health")).json();
    if (health.preparingResponses === 1) break;
    assert.ok(n < 100); await new Promise(r => setTimeout(r, 10));
  }
  const rejected = await fetch(base + "/v1/responses", { method: "POST", headers: { "content-type": "application/json", "content-encoding": "zstd" }, body: zstdCompressSync(json(payload())) });
  assert.equal(rejected.status, 503); assert.equal(rejected.headers.get("retry-after"), "1");
  await rejected.text(); assert.equal(seen.length, 0);
  // A small known-length request can still use the second slot.
  const parallel = await fetch(base + "/v1/responses", { method: "POST", headers: { "content-type": "application/json" }, body: json(payload()) });
  assert.equal(parallel.status, 200); await parallel.text();
  held.destroy();
  for (let n = 0; ; n++) {
    const health = await (await fetch(base + "/_yeutech/health")).json();
    if (health.preparingResponses === 0) break;
    assert.ok(n < 100); await new Promise(r => setTimeout(r, 10));
  }
  const body = json(payload());
  await new Promise((resolve, reject) => {
    const request = http.request({ hostname: "127.0.0.1", port: bridgePort, path: "/v1/responses/compact", method: "POST", headers: { "content-type": "application/json" } }, response => {
      assert.equal(response.statusCode, 200); response.resume(); response.on("end", resolve);
    });
    request.on("error", reject); request.write(body.subarray(0, 30)); request.end(body.subarray(30));
  });
  assert.match(JSON.parse(seen.at(-1).body).input[1].content[0].text, /next step B/);
});

// Installed app-server, real model switch and remote compact, all against a
// loopback mock. Only ephemeral test threads are created; no task is resumed.
test("installed engine switches both directions, continues and recompacts state", { skip: !process.env.CODEX_TEST_BIN, timeout: 480000 }, async t => {
  for (const [sourceModel, targetModel] of [
    ["claude-opus-5-5-tokyo", "gpt-6-astra"], ["grok-4.7-build-fast", "gpt-6-astra"],
    ["gpt-6-astra", "gemini-4-argon-high"], ["gpt-6-astra", "claude-opus-5-5-tokyo"],
    ["gpt-6-astra", "grok-4.7-build-fast"], ["grok-4.7-build-fast", "gemini-4-argon-high"],
    ["claude-opus-5-5-tokyo", "grok-4.7-build-fast"], ["gemini-4-argon-high", "gpt-6-astra"],
  ]) {
  for (const compression of [false, true]) {
    await t.test(`${sourceModel} -> ${targetModel} compression=${compression}`, { timeout: 50000 }, async t => {
      const directory = await mkdtemp(join(tmpdir(), "yeutech-compaction-engine-"));
      t.after(() => rm(directory, { recursive: true, force: true }));
      const sourceCapsule = sourceModel.startsWith("grok-")
        ? { type: "compaction", id: "cmp_fixture_grok", encrypted_content: randomBytes(64).toString("base64").replace(/=+$/, "") }
        : sourceModel.startsWith("gpt-") ? native : capsule();
      const targetCapsule = targetModel.startsWith("gpt-") ? native : targetModel.startsWith("grok-")
        ? { type: "compaction", id: "cmp_grok_target", encrypted_content: "opaque-grok-target" } : capsule();
      if (sourceModel.startsWith("grok-")) await registerCheckpoint(directory, sourceCapsule);
      const calls = [];
      let stage = "start", lastMethods = [];
      t.after(() => { if (stage !== "verified") console.log(JSON.stringify({ stage, requests: calls.map(c => ({ path: c.path, model: c.body.model })), lastMethods })); });
      const { base, seen } = await fixture(t, (record, res) => {
        const body = JSON.parse(record.headers["content-encoding"] === "zstd" ? zstdDecompressSync(record.body) : record.body);
        calls.push({ path: record.path, body, encoding: record.headers["content-encoding"] });
        if (record.path === "/v1/responses/compact") {
          res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify({ id: "cmp_fixture_response", object: "response.compaction", created_at: 1793242926,
            output: [{ role: "user", content: [{ type: "input_text", text: "Fixture task context." }] }, targetCapsule], usage: { input_tokens: 30, output_tokens: 10, total_tokens: 40 } }));
          return;
        }
        const compacting = body.input.some(item => item.type === "compaction_trigger");
        const item = compacting
          ? { ...(body.model === sourceModel ? sourceCapsule : targetCapsule), status: "completed" }
          : { type: "message", id: `msg_fixture_${calls.length}`, role: "assistant", status: "completed", content: [{ type: "output_text", text: "OK", annotations: [] }] };
        const response = { id: `resp_fixture_${calls.length}`, object: "response", status: "completed", model: body.model, output: [item], usage: { input_tokens: 10, output_tokens: 1, total_tokens: 11 } };
        const events = [{ type: "response.created", response: { ...response, status: "in_progress", output: [] } },
          { type: "response.output_item.added", output_index: 0, item }, { type: "response.output_item.done", output_index: 0, item }, { type: "response.completed", response }];
        res.writeHead(200, { "content-type": "text/event-stream" }).end(events.map(event => `data: ${JSON.stringify(event)}\n\n`).join(""));
      }, { YEUTECH_CODEX_RECOVERY_DIR: directory, YEUTECH_CODEX_SESSIONS_DIR: join(directory, "sessions") });
      const args = ["-c", 'model_provider="openai"', "-c", `openai_base_url="${base}/v1"`,
        "-c", `features.enable_request_compression=${compression}`, "-c", "features.memories=false", "-c", "model_context_window=500000", "-c", "model_auto_compact_token_limit=400000", "app-server"];
      if (process.env.CODEX_TEST_CATALOG) args.unshift("-c", `model_catalog_json=${JSON.stringify(process.env.CODEX_TEST_CATALOG)}`);
      // Disable integrations only in this child, never change the user's config.
      const config = await readFile(join(homedir(), ".codex/config.toml"), "utf8").catch(() => "");
      for (const match of config.matchAll(/^\[((?:plugins|mcp_servers)\.(?:"[^"]+"|[^.\]\s]+))\]$/gm)) args.unshift("-c", `${match[1]}.enabled=false`);
      args.unshift("-c", "features.apps=false", "-c", "features.hooks=false", "-c", "features.plugins=false");
      const child = spawn(process.env.CODEX_TEST_BIN, args, { stdio: ["pipe", "pipe", "ignore"] });
      t.after(async () => { if (child.exitCode === null) { child.kill("SIGTERM"); await once(child, "exit"); } });
      let sequence = 0, buffer = "";
      const pending = new Map(), notifications = [];
      let wake;
      child.stdout.on("data", data => {
        buffer += data;
        let end;
        while ((end = buffer.indexOf("\n")) >= 0) {
          const line = buffer.slice(0, end); buffer = buffer.slice(end + 1);
          let message; try { message = JSON.parse(line); } catch { continue; }
          lastMethods.push(message.method || `rpc:${message.id}${message.error ? ":error" : ""}`);
          lastMethods = lastMethods.slice(-15);
          if (process.env.CODEX_TEST_DEBUG) console.log(JSON.stringify({ stage, event: lastMethods.at(-1) }));
          if (pending.has(message.id)) { pending.get(message.id)(message); pending.delete(message.id); }
          else { notifications.push(message); wake?.(); }
        }
      });
      const rpc = async (method, params) => {
        const message = await new Promise(resolve => {
          const id = ++sequence; pending.set(id, resolve);
          child.stdin.write(JSON.stringify({ id, method, params }) + "\n");
        });
        assert.equal(message.error, undefined, `${method}: ${JSON.stringify(message.error)}`);
        return message.result;
      };
      const notification = async predicate => {
        for (;;) {
          const error = notifications.find(m => m.method === "error");
          if (error) throw new Error(`Fixture engine error: ${error.params?.error?.message || "unknown"}`);
          const index = notifications.findIndex(predicate);
          if (index >= 0) return notifications.splice(index, 1)[0];
          await new Promise(resolve => { wake = resolve; }); wake = null;
        }
      };
      await rpc("initialize", { clientInfo: { name: "yeutech_compaction_test", version: "1" }, capabilities: { experimentalApi: true } });
      stage = "thread/start";
      child.stdin.write('{"method":"initialized"}\n');
      const result = await rpc("thread/start", { model: sourceModel, modelProvider: "openai", cwd: directory, ephemeral: true,
        approvalPolicy: "never", sandbox: "read-only", baseInstructions: "Reply only OK. Never call tools.", developerInstructions: "Loopback fixture only. Never inspect files or call tools." });
      const threadId = result.thread.id;
      const turn = async model => {
        const started = await rpc("turn/start", { threadId, model, input: [{ type: "text", text: "Reply only OK." }] });
        const completed = await notification(m => m.method === "turn/completed" && m.params.turn.id === started.turn.id);
        assert.equal(completed.params.turn.status, "completed");
      };
      const compactThread = async () => {
        await rpc("thread/compact/start", { threadId });
        const item = await notification(m => m.method === "item/completed" && m.params.item.type === "contextCompaction");
        const completed = await notification(m => m.method === "turn/completed" && m.params.turn.id === item.params.turnId);
        assert.equal(completed.params.turn.status, "completed");
      };
      stage = "source turn";
      await turn(sourceModel);
      stage = "source compact";
      await compactThread();
      assert.ok(seen.every(record => record.headers["thread-id"] === threadId && record.headers["session-id"] === threadId), "real engine supplies matching exact thread/session identities");
      const day = join(directory, "sessions/2026/10/30"); await mkdir(day, { recursive: true });
      await writeFile(join(day, `rollout-fixture-${threadId}.jsonl`), [
        { type: "session_meta", payload: { id: threadId } },
        { type: "compacted", payload: { replacement_history: [sourceCapsule], guardian_history: [
          { type: "message", role: "user", content: [{ type: "input_text", text: "Keep project A; next step B."
            + (sourceModel.startsWith("gpt-") && targetModel.startsWith("claude-") ? "x".repeat(600000) : "") }] }
        ], latest_token_usage_record: { turn_id: "source" } } },
        { type: "turn_context", payload: { turn_id: "source", model: sourceModel } },
      ].map(JSON.stringify).join("\n") + "\n", { mode: 0o600 });
      stage = "target turn";
      await turn(targetModel);
      const gpt = calls.find(call => call.body.model === targetModel && call.path === "/v1/responses");
      assert.ok(gpt, "real target continuation reached bridge");
      assert.ok(gpt.body.input.some(item => item.role === "user" && item.content?.some(part => part.text?.includes("next step B"))));
      assert.ok(!JSON.stringify(gpt.body.input).includes("cpa-ag-compact-"));
      assert.ok(!JSON.stringify(gpt.body.input).includes(sourceCapsule.encrypted_content));
      stage = "target compact";
      await compactThread();
      const compact = calls.find(call => call.body.model === targetModel && call.body.input.some(item => item.type === "compaction_trigger"));
      assert.ok(compact, "real engine used streamed remote compaction v2");
      assert.ok(compact.body.input.some(item => item.role === "user" && item.content?.some(part => part.text?.includes("next step B"))));
      assert.ok(!JSON.stringify(compact.body.input).includes("cpa-ag-compact-"));
      stage = "target native continuation";
      await turn(targetModel);
      assert.ok(calls.at(-1).body.input.some(item => item.type === "compaction" && item.encrypted_content === targetCapsule.encrypted_content));
      assert.ok(seen.every(record => record.headers.authorization === "Bearer fixture-gateway"));
      assert.equal(calls.length, 5, "three turns and two compactions, no retries or external calls");
      stage = "verified";
      console.log(JSON.stringify({ sourceModel, targetModel, compression, inferenceCalls: 3, compactCalls: 2, switchedAndCompacted: true, nativeCapsulePreserved: true, paidCalls: 0 }));
    });
  }
  }
});

async function registerCheckpoint(directory, item, overrides = {}) {
  const hash = createHash("sha256").update(item.encrypted_content).digest("hex");
  const path = join(directory, `${hash}.json`);
  const record = { schemaVersion: 1, mode: "reviewed-visible-checkpoint", sourceProvider: "xai", sourceModel: "grok-4.7",
    targetFamily: "gpt", itemType: item.type, itemId: item.id, encryptedSha256: hash, sourceRolloutSha256: "a".repeat(64),
    summary: "Keep project A; next step B. This checkpoint uses visible task records, not decrypted reasoning.", ...overrides };
  await writeFile(path, JSON.stringify(record), { mode: 0o600 });
  return path;
}

test("exact reviewed Grok checkpoints restore compaction/reasoning without provider guessing", async t => {
  const directory = await mkdtemp(join(tmpdir(), "yeutech-recovery-test-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const compact = { type: "compaction", id: "cmp_grok", encrypted_content: randomBytes(48).toString("base64") };
  const thought = { type: "reasoning", id: "rs_grok", encrypted_content: randomBytes(48).toString("base64"), summary: [] };
  await registerCheckpoint(directory, compact); await registerCheckpoint(directory, thought);
  const original = { ...payload(), input: [compact, thought, native, reasoning, ...payload().input.slice(4)] };
  const body = json(original), copy = Buffer.from(body);
  const converted = JSON.parse(await normalizeProxyCompaction(body, { recoveryDirectory: directory }));
  assert.deepEqual(body, copy);
  assert.equal(converted.input.length, original.input.length);
  for (const index of [0, 1]) {
    assert.equal(converted.input[index].role, "user");
    assert.match(converted.input[index].content[0].text, /not a decryption/);
    assert.equal(converted.input[index].encrypted_content, undefined);
  }
  assert.deepEqual(converted.input.slice(2), original.input.slice(2));
  for (const model of ["grok-4.7", "claude-opus-5-5-tokyo"]) {
    const nonGPT = json({ ...original, model });
    assert.equal(await normalizeProxyCompaction(nonGPT, { recoveryDirectory: directory }), nonGPT);
  }
  const unregistered = json({ model: "gpt-6.1-sol", input: [{ ...compact, encrypted_content: compact.encrypted_content + "A" }] });
  assert.equal(await normalizeProxyCompaction(unregistered, { recoveryDirectory: directory }), unregistered);
  const nativeOpenAI = { type: "compaction", id: "cmp_native", encrypted_content: "gAAAAABnative-fixture" };
  await registerCheckpoint(directory, nativeOpenAI);
  const nativeBody = json({ model: "gpt-6.1-sol", input: [nativeOpenAI] });
  assert.equal(await normalizeProxyCompaction(nativeBody, { recoveryDirectory: directory }), nativeBody);
});

test("checkpoint schema, item binding, permissions, symlinks and size fail closed", async t => {
  const directory = await mkdtemp(join(tmpdir(), "yeutech-recovery-invalid-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const item = { type: "compaction", id: "cmp_grok", encrypted_content: randomBytes(48).toString("base64") };
  const body = json({ model: "gpt-6.1-sol", input: [item] });
  for (const overrides of [{ encryptedSha256: "b".repeat(64) }, { sourceProvider: "openai" }, { sourceModel: "unknown" },
    { itemType: "reasoning" }, { itemId: "wrong-id" }, { targetFamily: "all" }, { mode: "decrypted" }, { schemaVersion: 2 }, { summary: "" }, { sourceRolloutSha256: "missing" }]) {
    await registerCheckpoint(directory, item, overrides);
    await assert.rejects(normalizeProxyCompaction(body, { recoveryDirectory: directory }), { code: "compaction_recovery_invalid" });
  }
  const path = await registerCheckpoint(directory, item);
  await chmod(path, 0o644);
  await assert.rejects(normalizeProxyCompaction(body, { recoveryDirectory: directory }), { code: "compaction_recovery_invalid" });
  await chmod(path, 0o600); await writeFile(path, "x".repeat(65537));
  await assert.rejects(normalizeProxyCompaction(body, { recoveryDirectory: directory }), { code: "compaction_recovery_invalid" });
  await rm(path); await symlink(join(directory, "missing"), path);
  await assert.rejects(normalizeProxyCompaction(body, { recoveryDirectory: directory }), { code: "compaction_recovery_unavailable" });
});

test("bridge restores reviewed checkpoints for standalone and streamed compact with every encoding", async t => {
  const directory = await mkdtemp(join(tmpdir(), "yeutech-recovery-http-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const item = { type: "compaction", id: "cmp_grok", encrypted_content: randomBytes(48).toString("base64") };
  await registerCheckpoint(directory, item);
  const { base, seen } = await fixture(t, undefined, { YEUTECH_CODEX_RECOVERY_DIR: directory });
  for (const path of ["/v1/responses", "/v1/responses/compact"]) for (const [encoding, encode] of Object.entries(encoders)) {
    const response = await fetch(base + path, { method: "POST", headers: { "content-type": "application/json", "content-encoding": encoding },
      body: encode(json({ model: "gpt-6.1-sol", input: [item, { type: "compaction_trigger" }] })) });
    assert.equal(response.status, 200); await response.text();
    const input = JSON.parse(seen.at(-1).body).input;
    assert.equal(input[0].role, "user"); assert.match(input[0].content[0].text, /next step B/);
    assert.deepEqual(input[1], { type: "compaction_trigger" });
    assert.equal(seen.at(-1).headers["content-encoding"], undefined);
  }
});
