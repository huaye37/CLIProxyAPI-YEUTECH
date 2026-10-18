import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import http from "node:http";
import test from "node:test";
import { Readable } from "node:stream";
import { fileURLToPath } from "node:url";
import { isCodexStandaloneSearch, routeStandaloneSearch, readSearchBody } from "./codex-yeutech-search.mjs";

test("only Codex standalone search opts in, never inference or Live", () => {
  const request = { method: "POST", url: "/v1/alpha/search?test=1", headers: { originator: "codex_cli_rs" } };
  assert.ok(isCodexStandaloneSearch(request));
  for (const url of ["/v1/responses", "/v1/live", "/v1/responses/compact"]) assert.equal(isCodexStandaloneSearch({ ...request, url }), false);
  assert.equal(isCodexStandaloneSearch({ ...request, method: "GET" }), false);
  assert.equal(isCodexStandaloneSearch({ ...request, headers: {} }), false);
});

test("search routing preserves commands, filters, session and actual usage", () => {
  const original = { id: "fixture", model: "grok-4.7-build-fast", input: [{ role: "user", content: "test" }], commands: { search_query: [{ q: "test", domains: ["example.com"] }] }, settings: { external_web_access: true }, usage: { input_tokens: 1128483, output_tokens: 2434, total_tokens: 1130917 } };
  const routed = JSON.parse(routeStandaloneSearch(Buffer.from(JSON.stringify(original))));
  assert.deepEqual(routed, { ...original, model: "gpt-6.1-sol" });
  for (const model of ["gpt-6.1-sol", "gpt-5.6-luna"]) {
    const body = Buffer.from(JSON.stringify({ ...original, model }));
    assert.equal(routeStandaloneSearch(body), body);
  }
  for (const value of ["null", "[]", "{}", '{"model":null}', "invalid"]) assert.throws(() => routeStandaloneSearch(Buffer.from(value)));
  assert.throws(() => routeStandaloneSearch(Buffer.from(JSON.stringify(original)), "invalid/route"));
});

test("search input buffering is bounded and leaves the rejection socket open", async () => {
  assert.equal((await readSearchBody(Readable.from([Buffer.from("ab"), Buffer.from("cd")]), 4)).toString(), "abcd");
  await assert.rejects(readSearchBody(Readable.from([Buffer.from("abcde")]), 4), { statusCode: 413 });
});

async function listen(server) {
  await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
  return server.address().port;
}

test("bridge rewrites search only and keeps inference usage byte-for-byte", async (t) => {
  const seen = [];
  const usage = 'data: {"type":"response.completed","response":{"usage":{"input_tokens":1128483,"output_tokens":2434,"total_tokens":1130917}}}\n\n';
  const upstream = http.createServer(async (req, res) => {
    const chunks = []; for await (const chunk of req) chunks.push(chunk);
    seen.push({ url: req.url, auth: req.headers.authorization, body: JSON.parse(Buffer.concat(chunks)) });
    res.writeHead(200, { "content-type": "text/event-stream" }).end(usage);
  });
  const port = await listen(upstream); t.after(() => upstream.close());
  const reservation = http.createServer(); const bridgePort = await listen(reservation);
  await new Promise(resolve => reservation.close(resolve));
  const child = spawn(process.execPath, [fileURLToPath(new URL("./codex-yeutech-auth-bridge.mjs", import.meta.url))], {
    env: { ...process.env, YEUTECH_CODEX_BRIDGE_PORT: String(bridgePort), YEUTECH_UPSTREAM_BASE_URL: `http://127.0.0.1:${port}`, YEUTECH_UPSTREAM_BEARER_TOKEN: "fixture-gateway" }, stdio: "ignore",
  });
  t.after(() => child.kill("SIGTERM"));
  for (let n = 0; ; n++) {
    try { if ((await fetch(`http://127.0.0.1:${bridgePort}/_yeutech/health`)).ok) break; } catch {}
    assert.ok(n < 80, "bridge ready"); await new Promise(r => setTimeout(r, 20));
  }
  const original = { model: "grok-4.7-build-fast", commands: { search_query: [{ q: "fixture" }] } };
  for (const url of ["/v1/alpha/search", "/v1/responses"]) {
    const response = await fetch(`http://127.0.0.1:${bridgePort}${url}`, { method: "POST", headers: { originator: "Codex Desktop", authorization: "Bearer fixture-oauth", "content-type": "application/json" }, body: JSON.stringify(original) });
    assert.equal(response.status, 200); assert.equal(await response.text(), usage);
  }
  assert.equal(seen[0].body.model, "gpt-6.1-sol");
  assert.deepEqual(seen[1].body, original);
  assert.ok(seen.every(r => r.auth === "Bearer fixture-gateway"));
  const malformed = await fetch(`http://127.0.0.1:${bridgePort}/v1/alpha/search`, { method: "POST", headers: { originator: "Codex Desktop" }, body: "secret-invalid-json" });
  assert.equal(malformed.status, 400);
  assert.ok(!(await malformed.text()).includes("secret-invalid-json"));
  assert.equal(seen.length, 2);
});

// Use the installed engine, a loopback-only fake provider and ephemeral threads.
// No credential, real model, user history or production configuration is used.
test("real Codex engine separates search from inference without disabling compaction", { skip: !process.env.CODEX_TEST_BIN, timeout: 90000 }, async (t) => {
  const directory = await mkdtemp(path.join(tmpdir(), "yeutech-search-regression-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  async function scenario(standalone, realLargeContext = false) {
    const requests = []; let searchCount = 0; let inferenceCount = 0;
    const server = http.createServer(async (req, res) => {
      if (req.method !== "POST") { res.writeHead(200, { "content-type": "application/json" }).end('{"models":[]}'); return; }
      const chunks = []; for await (const chunk of req) chunks.push(chunk);
      const body = JSON.parse(Buffer.concat(chunks));
      requests.push({ url: req.url, body });
      if (req.url === "/v1/alpha/search") {
        searchCount++; res.writeHead(200, { "content-type": "application/json" }).end(JSON.stringify({ output: "Search fixture evidence.", results: [] })); return;
      }
      inferenceCount++;
      const id = `resp_fixture_${inferenceCount}`;
      const first = inferenceCount === 1;
      const item = first
        ? { type: "function_call", id: "fc_fixture", call_id: "call_fixture", name: standalone ? "run" : "fixture_noop", ...(standalone ? { namespace: "web" } : {}), arguments: standalone ? JSON.stringify({ search_query: [{ q: "fixture" }] }) : "{}" }
        : { type: "message", id: `msg_${inferenceCount}`, role: "assistant", status: "completed", content: [{ type: "output_text", text: "Fixture complete.", annotations: [] }] };
      const input = first && (!standalone || realLargeContext) ? 1128483 : 43035;
      const response = { id, object: "response", status: "completed", model: "grok-4.7-build-fast", output: [item], usage: { input_tokens: input, output_tokens: 643, total_tokens: input + 643 } };
      const events = [{ type: "response.created", response: { ...response, status: "in_progress", output: [] } }, { type: "response.output_item.added", output_index: 0, item }, { type: "response.output_item.done", output_index: 0, item }, { type: "response.completed", response }];
      res.writeHead(200, { "content-type": "text/event-stream" }).end(events.map(e => `data: ${JSON.stringify(e)}\n\n`).join(""));
    });
    const port = await listen(server);
    try {
      const args = ["exec", "--ignore-user-config", "--ephemeral", "--skip-git-repo-check", "--json", "-C", directory, "-m", "grok-4.7-build-fast",
        "-c", 'model_provider="fixture"', "-c", `model_providers.fixture={name="Fixture",base_url="http://127.0.0.1:${port}/v1",wire_api="responses",requires_openai_auth=false,supports_standalone_web_search=true}`, "-c", `features.standalone_web_search=${standalone}`,
        "-c", "features.memories=false", "-c", "model_context_window=500000", "-c", "model_auto_compact_token_limit=400000", "-c", 'web_search="live"', "-c", 'approval_policy="never"', "-c", 'sandbox_mode="read-only"', "Fixture only."];
      if (process.env.CODEX_TEST_CATALOG) args.splice(-1, 0, "-c", `model_catalog_json=${JSON.stringify(process.env.CODEX_TEST_CATALOG)}`);
      const child = spawn(process.env.CODEX_TEST_BIN, args, { stdio: ["ignore", "pipe", "pipe"] });
      let output = "", stderr = "";
      child.stdout.on("data", c => output += c); child.stderr.on("data", c => { stderr += c; if (process.env.CODEX_TEST_DEBUG) process.stderr.write(c); });
      const timer = setTimeout(() => child.kill("SIGTERM"), 35000);
      const code = await new Promise(resolve => child.on("close", resolve)); clearTimeout(timer);
      if (code !== 0) console.error(JSON.stringify({ fixtureExitCode: code, diagnostic: stderr.slice(-1800) }));
      assert.equal(code, 0, stderr.slice(-1800));
      const firstTools = requests.find(r => r.url === "/v1/responses").body.tools;
      const compactRequests = requests.filter(r => r.url.includes("compact") || (r.url === "/v1/responses" && !r.body.tools?.length));
      return { standalone, searchCount, inferenceCount, compactCount: compactRequests.length, hosted: firstTools.some(tool => tool.type === "web_search"), toolNames: firstTools.map(tool => tool.name || tool.type), output };
    } finally { server.closeAllConnections(); await new Promise(resolve => server.close(resolve)); }
  }
  const before = await scenario(false);
  const after = await scenario(true);
  const full = await scenario(true, true);
  assert.equal(before.hosted, true);
  assert.ok(before.compactCount > 0, "cumulative hosted-search usage reproduces compaction");
  assert.equal(after.hosted, false);
  assert.equal(after.searchCount, 1);
  assert.equal(after.compactCount, 0);
  assert.equal(after.inferenceCount, 2);
  assert.ok(full.compactCount > 0, "genuinely full context must still compact");
  console.log(JSON.stringify({ before: { hosted: before.hosted, compactCount: before.compactCount }, after: { hosted: after.hosted, compactCount: after.compactCount, searchCount: after.searchCount, inferenceCount: after.inferenceCount }, realLargeContextCompactCount: full.compactCount, unchangedContextWindow: 500000, unchangedCompactionLimit: 400000 }));
});
