import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import http from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { PassThrough } from "node:stream";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { deflateSync } from "node:zlib";
import { imageDimensions, isImageRequest, normalizeClaudeImages, readImageBody, resizeImage } from "./codex-yeutech-images.mjs";

function crc(bytes) {
  let c = 0xffffffff;
  for (const b of bytes) { c ^= b; for (let n = 0; n < 8; n++) c = (c >>> 1) ^ ((c & 1) ? 0xedb88320 : 0); }
  return (c ^ 0xffffffff) >>> 0;
}
function chunk(type, data) {
  const head = Buffer.alloc(4); head.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type), data]); const tail = Buffer.alloc(4); tail.writeUInt32BE(crc(body));
  return Buffer.concat([head, body, tail]);
}
function png(width, height) {
  const ihdr = Buffer.alloc(13); ihdr.writeUInt32BE(width); ihdr.writeUInt32BE(height, 4); ihdr[8] = 8; ihdr[9] = 2;
  return Buffer.concat([Buffer.from([137,80,78,71,13,10,26,10]), chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(Buffer.alloc((width * 3 + 1) * height, 0))), chunk("IEND", Buffer.alloc(0))]);
}
const image = (bytes, mime = "image/png") => ({ type: "input_image", image_url: `data:${mime};base64,${bytes.toString("base64")}`, detail: "original" });
const small = image(png(10, 10));
const large = image(png(3024, 1900));
function payload(count = 21, model = "claude-opus-5-5-tokyo", largePart = large) {
  return { model, stream: true, service_tier: "priority", compaction_trigger: { type: "auto", token_threshold: 1234 },
    tools: [{ type: "function", name: "inspect", parameters: { type: "object", properties: {} } }],
    input: [{ type: "compaction", encrypted_content: "sealed-sentinel" },
      { type: "function_call", call_id: "c1", name: "inspect", arguments: "{}" },
      { type: "function_call_output", call_id: "c1", output: [{ type: "input_text", text: "tool result" }, largePart] },
      { role: "user", content: Array.from({ length: count - 1 }, () => ({ ...small })) }] };
}
const encode = x => Buffer.from(JSON.stringify(x));
const decodeImage = x => Buffer.from(x.image_url.split(",")[1], "base64");

test("only uncompressed JSON Responses paths are eligible", () => {
  const req = { method: "POST", url: "/v1/responses?trace=1", headers: { "content-type": "application/json; charset=utf-8" } };
  assert.equal(isImageRequest(req), true);
  assert.equal(isImageRequest({ ...req, url: "/v1/responses/compact" }), true);
  for (const patch of [{ method: "GET" }, { url: "/v1/live" }, { url: "/v1/alpha/search" },
    { headers: { ...req.headers, "content-encoding": "zstd" } }]) assert.equal(isImageRequest({ ...req, ...patch }), false);
});

test("non-target models, 20 images, compliant images and malformed JSON stay byte identical", async () => {
  for (const body of [Buffer.from("not JSON"), Buffer.from("null"), encode(payload(21, "gpt-6.1-sol")),
    encode(payload(21, "claude-opus-5-5-high")), encode(payload(20)), encode(payload(21, undefined, small))]) {
    assert.equal(await normalizeClaudeImages(body), body);
  }
});

test("real PNG resize preserves aspect, content order, tool IDs, compaction and parameters", async () => {
  const original = payload(); const body = encode(original); const before = Buffer.from(body);
  const result = JSON.parse(await normalizeClaudeImages(body));
  const output = result.input[2].output;
  assert.deepEqual(imageDimensions(decodeImage(output[2])), { width: 3000, height: 1885 });
  assert.match(output[1].text, /3024x1900 to 3000x1885/);
  assert.equal(output[0].text, "tool result"); assert.equal(output[2].detail, "original");
  assert.deepEqual(body, before); assert.deepEqual(original.input[2].output[1], large);
  for (const key of ["model", "stream", "tools", "service_tier", "compaction_trigger"]) assert.deepEqual(result[key], original[key]);
  assert.deepEqual(result.input[0], original.input[0]); assert.deepEqual(result.input[1], original.input[1]);
  assert.equal(result.input[2].call_id, "c1"); assert.deepEqual(result.input[3], original.input[3]);
  const second = encode(result); assert.equal(await normalizeClaudeImages(second), second);
});

test("real JPEG portrait conversion stays JPEG and below both dimension limits", async () => {
  const dir = await mkdtemp(join(tmpdir(), "yeutech-image-test-"));
  try {
    await writeFile(join(dir, "in.png"), png(1900, 3024));
    execFileSync("/usr/bin/sips", ["-s", "format", "jpeg", join(dir, "in.png"), "--out", join(dir, "in.jpg")], { stdio: "ignore" });
    const result = JSON.parse(await normalizeClaudeImages(encode(payload(21, undefined, image(await readFile(join(dir, "in.jpg")), "image/jpeg")))));
    const output = result.input[2].output[2]; assert.match(output.image_url, /^data:image\/jpeg;base64,/);
    assert.deepEqual(imageDimensions(decodeImage(output)), { width: 1885, height: 3000 });
  } finally { await rm(dir, { recursive: true, force: true }); }
});

test("remote/file images count but are never fetched; duplicate copies resize once", async () => {
  const request = payload(); request.input[3].content = [{ ...large }, ...Array.from({ length: 19 }, (_, n) =>
    n % 2 ? { type: "input_image", file_id: "file-sentinel" } : { type: "input_image", image_url: "https://example.invalid/private" })];
  let calls = 0;
  const result = JSON.parse(await normalizeClaudeImages(encode(request), { resize: async () => {
    calls++; return { bytes: png(3000, 1885), mime: "image/png", width: 3000, height: 1885 };
  } }));
  assert.equal(calls, 1); assert.deepEqual(result.input[3].content.slice(2), request.input[3].content.slice(1));
});

test("embedded tool schemas/text are not scanned for image-like objects", async () => {
  const request = payload(1); request.tools[0].parameters.examples = Array.from({ length: 30 }, () => large);
  const body = encode(request); assert.equal(await normalizeClaudeImages(body), body);
});

test("native computer screenshots retain their coordinate contract", async () => {
  const request = payload(21, undefined, small);
  request.input.push({ type: "computer_call_output", call_id: "computer", output: { ...large, type: "computer_screenshot" } });
  const body = encode(request); assert.equal(await normalizeClaudeImages(body), body);
});

test("invalid headers, large pixel budgets and cancellation are bounded", async () => {
  assert.equal(imageDimensions(Buffer.from([255,216,255,192,0])), null);
  const huge = Buffer.from(decodeImage(large)); huge.writeUInt32BE(100000, 16);
  await assert.rejects(normalizeClaudeImages(encode(payload(21, undefined, image(huge)))), /pixel budget/);
  const abort = new AbortController(); abort.abort();
  await assert.rejects(normalizeClaudeImages(encode(payload()), { signal: abort.signal }), { name: "AbortError" });
  await assert.rejects(resizeImage(decodeImage(large), "image/png", 3000, abort.signal), { name: "AbortError" });
});

test("bounded body reader returns complete buffers or a lossless paused prefix", async () => {
  const full = new PassThrough(); const fullRead = readImageBody(full, 10); full.end("12345");
  assert.deepEqual(await fullRead, { body: Buffer.from("12345"), complete: true });
  const stream = new PassThrough(); const read = readImageBody(stream, 4); stream.write("12345");
  const prefix = await read; assert.equal(prefix.complete, false); assert.equal(stream.isPaused(), true);
  stream.end("6789"); const chunks = [prefix.body]; for await (const c of stream) chunks.push(c);
  assert.equal(Buffer.concat(chunks).toString(), "123456789");
});

test("aborted body buffering rejects without leaving listeners", async () => {
  const stream = new PassThrough(); const read = readImageBody(stream); stream.emit("aborted");
  await assert.rejects(read, /aborted/); assert.equal(stream.listenerCount("data"), 0);
});

test("loopback bridge sends corrected images with gateway auth and keeps SSE intact", async t => {
  let observed;
  const upstream = http.createServer(async (req, res) => {
    const chunks = []; for await (const c of req) chunks.push(c);
    const body = Buffer.concat(chunks); observed = { headers: req.headers, body: JSON.parse(body), bytes: body.length };
    res.writeHead(200, { "content-type": "text/event-stream" }).end('event: response.completed\ndata: {"type":"response.completed","response":{"status":"completed","output":[]}}\n\n');
  });
  await new Promise(resolve => upstream.listen(0, "127.0.0.1", resolve));
  t.after(() => { upstream.closeAllConnections(); upstream.close(); });
  const reserve = http.createServer(); await new Promise(resolve => reserve.listen(0, "127.0.0.1", resolve));
  const port = reserve.address().port; await new Promise(resolve => reserve.close(resolve));
  const child = spawn(process.execPath, [fileURLToPath(new URL("./codex-yeutech-auth-bridge.mjs", import.meta.url))], {
    env: { ...process.env, YEUTECH_CODEX_BRIDGE_PORT: String(port), YEUTECH_UPSTREAM_BASE_URL: `http://127.0.0.1:${upstream.address().port}`,
      YEUTECH_UPSTREAM_BEARER_TOKEN: "test-gateway" }, stdio: "ignore" });
  t.after(async () => { if (child.exitCode === null) { child.kill("SIGTERM"); await once(child, "exit"); } });
  for (let n = 0; ; n++) {
    try { if ((await fetch(`http://127.0.0.1:${port}/_yeutech/health`)).ok) break; } catch {}
    assert.ok(n < 100); await new Promise(resolve => setTimeout(resolve, 20));
  }
  const response = await fetch(`http://127.0.0.1:${port}/v1/responses`, { method: "POST", headers: {
    "content-type": "application/json", authorization: "Bearer test-oauth", digest: "stale-digest" }, body: encode(payload()) });
  assert.equal(response.status, 200); assert.match(await response.text(), /\}\n\n$/);
  assert.equal(observed.headers.authorization, "Bearer test-gateway"); assert.equal(observed.headers.digest, undefined);
  assert.equal(Number(observed.headers["content-length"]), observed.bytes);
  assert.deepEqual(imageDimensions(decodeImage(observed.body.input[2].output[2])), { width: 3000, height: 1885 });
});
