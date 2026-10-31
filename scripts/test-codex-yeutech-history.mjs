import assert from "node:assert/strict";
import test from "node:test";
import { mkdtemp, mkdir, writeFile, readFile, rm, symlink, open } from "node:fs/promises";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { normalizeProxyCompaction } from "./codex-yeutech-compaction.mjs";
import { contextCost, modelContextLimit } from "./codex-yeutech-history.mjs";

const threadId = "11111111-2222-3333-4444-555555555555";
const capsule = { type: "compaction", id: "cmp_native", encrypted_content: "gAAAA-native-fixture" };
const reasoning = { type: "reasoning", id: "rs_fixture", encrypted_content: "private-fixture", summary: [{ type: "summary_text", text: "Visible progress." }] };
const kept = { type: "message", role: "user", content: [{ type: "input_text", text: "Keep this current instruction." }] };
const history = [
  { type: "message", role: "developer", content: [{ type: "input_text", text: "OLD SYSTEM DO NOT REPLAY" }] },
  { type: "message", role: "user", content: [{ type: "input_text", text: "Remember A and next step B." }] },
  { type: "function_call", call_id: "call_old", name: "echo", arguments: "{}" },
  { type: "function_call_output", call_id: "call_old", output: "Observed result C." },
  reasoning, kept,
];
async function fixture(t, model = "gpt-6-astra", overrides = {}) {
  const directory = await mkdtemp(join(tmpdir(), "yeutech-portable-history-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const day = join(directory, "2026/10/30"); await mkdir(day, { recursive: true });
  const path = join(day, `rollout-fixture-${threadId}.jsonl`);
  const rows = [
    { type: "session_meta", payload: { id: threadId } },
    { type: "event_msg", payload: { type: "task_started", turn_id: "source" } },
    { type: "compacted", payload: { replacement_history: [capsule], guardian_history: history, latest_token_usage_record: { turn_id: "source" }, ...overrides } },
    // Real native compaction is persisted BEFORE its turn_context.
    { type: "turn_context", payload: { turn_id: "source", model } },
    { type: "response_item", payload: reasoning },
  ];
  await writeFile(path, rows.map(r => JSON.stringify(r)).join("\n") + "\n", { mode: 0o600 });
  return { directory, path, rows, options: { threadId, sessionDirectory: directory } };
}

test("all verified cross-family directions restore visible context, preserve same-family native state", async t => {
  const models = ["gpt-6-astra", "grok-4.7", "claude-opus-5-5-tokyo", "gemini-4-argon-high"];
  for (const source of models) {
    const f = await fixture(t, source), originalFile = await readFile(f.path);
    for (const target of models) {
      const original = { model: target, stream: true, service_tier: "priority", input: [capsule, reasoning, kept] };
      const body = Buffer.from(JSON.stringify(original));
      const converted = await normalizeProxyCompaction(body, f.options);
      if (source === target) { assert.equal(converted, body); continue; }
      const value = JSON.parse(converted);
      assert.equal(value.input.length, original.input.length);
      assert.equal(value.input[0].role, "user");
      assert.match(value.input[0].content[0].text, /Remember A and next step B/);
      assert.match(value.input[0].content[0].text, /Observed result C/);
      assert.doesNotMatch(value.input[0].content[0].text, /OLD SYSTEM|private-fixture|Keep this current instruction/);
      assert.match(value.input[1].content[0].text, /Visible progress/);
      assert.deepEqual(value.input[2], kept);
      assert.equal(value.service_tier, "priority");
      assert.deepEqual(await readFile(f.path), originalFile);
    }
  }
});

test("unknown model/provenance/thread and altered ID or encrypted hash never guess a provider", async t => {
  const f = await fixture(t);
  for (const [input, options, model] of [
    [[{ ...capsule, id: "different" }], f.options],
    [[{ ...capsule, encrypted_content: "different" }], f.options],
    [[capsule], { ...f.options, threadId: "../../private" }],
    [[capsule], { ...f.options, threadId: "99999999-2222-3333-4444-555555555555" }],
    [[capsule], f.options, "unknown-model"],
  ]) {
    const body = Buffer.from(JSON.stringify({ model: model || "gemini-4-argon-high", input }));
    assert.equal(await normalizeProxyCompaction(body, options), body);
  }
  const unknown = await fixture(t, "unknown-model");
  const body = Buffer.from(JSON.stringify({ model: "gemini-4-argon-high", input: [capsule] }));
  assert.equal(await normalizeProxyCompaction(body, unknown.options), body);
});

test("foreign recovery fails closed on missing history, unsafe files and wrong owner thread", async t => {
  const body = Buffer.from(JSON.stringify({ model: "gemini-4-argon-high", input: [capsule] }));
  for (const overrides of [{ guardian_history: null }, { guardian_history: [] },
    { guardian_history: [{ type: "unknown_tool", input: "not silently discarded" }] }]) {
    const f = await fixture(t, "gpt-6-astra", overrides);
    await assert.rejects(normalizeProxyCompaction(body, f.options), /foreign_history_/);
  }
  const f = await fixture(t);
  f.rows[0].payload.id = "another-thread";
  await writeFile(f.path, f.rows.map(JSON.stringify).join("\n"));
  await assert.rejects(normalizeProxyCompaction(body, f.options), { code: "foreign_history_thread_mismatch" });
  await writeFile(f.path, '{"incomplete":');
  await assert.rejects(normalizeProxyCompaction(body, f.options), { code: "foreign_history_incomplete" });
  await rm(f.path); await symlink('/dev/null', f.path);
  await assert.rejects(normalizeProxyCompaction(body, f.options));
});

test("over-512-KiB recovery becomes an honest bounded checkpoint with exact source reference", async t => {
  const f = await fixture(t, "gpt-6-astra", { guardian_history: [
    { type: "message", role: "user", content: "Initial requirement. " + "x".repeat(600000) },
    { type: "message", role: "assistant", content: "Latest result: pending verification, not complete." },
  ] });
  const original = await readFile(f.path);
  const result = JSON.parse(await normalizeProxyCompaction(Buffer.from(JSON.stringify({ model: "gemini-4-argon-high", input: [capsule, kept] })), f.options));
  const text = result.input[0].content[0].text;
  assert.ok(Buffer.byteLength(text) < 65536);
  assert.match(text, /bounded-visible-history-checkpoint/);
  assert.match(text, /"complete":false/);
  assert.match(text, /pending verification, not complete/);
  assert.ok(text.includes(f.path));
  assert.match(text, /byteOffset/);
  assert.match(text, /capsuleBinding/);
  assert.deepEqual(result.input[1], kept);
  assert.deepEqual(await readFile(f.path), original);
});

test("unrelated oversized line never blocks same-family calls or poisons later provenance", async t => {
  const f = await fixture(t);
  const padding = JSON.stringify({ type: "event_msg", payload: { text: "x".repeat(17 * 1024 * 1024) } });
  await writeFile(f.path, [JSON.stringify(f.rows[0]), padding, ...f.rows.slice(1).map(JSON.stringify)].join("\r\n") + "\r\n");
  const same = Buffer.from(JSON.stringify({ model: "gpt-6-astra", input: [capsule, reasoning, kept] }));
  assert.equal(await normalizeProxyCompaction(same, f.options), same);
  const other = Buffer.from(JSON.stringify({ model: "gemini-4-argon-high", input: [capsule, kept] }));
  assert.match(JSON.parse(await normalizeProxyCompaction(other, f.options)).input[0].content[0].text, /next step B/);
});

test("an unfinished concurrent append is ignored after verified complete records", async t => {
  const f = await fixture(t);
  await writeFile(f.path, await readFile(f.path, "utf8") + '{"type":"event_msg","payload":');
  const same = Buffer.from(JSON.stringify({ model: "gpt-6-astra", input: [capsule, reasoning] }));
  assert.equal(await normalizeProxyCompaction(same, f.options), same);
});

test("target catalog budget checkpoints old prefix and retains current user, instructions and tool pairs", async t => {
  const f = await fixture(t);
  const catalogPath = join(f.directory, "catalog.json");
  await writeFile(catalogPath, JSON.stringify({ models: [{ slug: "claude-opus-5-5-tokyo", context_window: 100000, auto_compact_token_limit: 80000 }] }));
  const current = { type: "message", role: "user", content: "LATEST USER INPUT MUST STAY" };
  const call = { type: "function_call", call_id: "pending", name: "echo", arguments: "{}" };
  const output = { type: "function_call_output", call_id: "pending", output: "LATEST TOOL RESULT MUST STAY" };
  const instructions = { type: "message", role: "developer", content: "CURRENT APPLICATION INSTRUCTIONS" };
  const original = { model: "claude-opus-5-5-tokyo", instructions: "Preserve exact instructions.", tools: [{ type: "function", name: "echo", parameters: {} }],
    input: [instructions, capsule, { type: "message", role: "user", content: "OLD " + "x".repeat(300000) }, call, current, output], service_tier: "priority" };
  const result = JSON.parse(await normalizeProxyCompaction(Buffer.from(JSON.stringify(original)), { ...f.options, catalogPath }));
  assert.ok(contextCost(result) < 80000 - 8192);
  for (const item of [instructions, call, current, output]) assert.ok(result.input.some(x => JSON.stringify(x) === JSON.stringify(item)));
  assert.deepEqual(result.tools, original.tools);
  assert.equal(result.instructions, original.instructions);
  assert.equal(result.service_tier, "priority");
  assert.ok(result.input.some(x => x.content?.[0]?.text?.includes("Original history was not modified")));
  assert.equal(await modelContextLimit("unknown", catalogPath), null);
  const hugeCurrent = { ...original, input: [capsule, { ...current, content: "x".repeat(100000) }] };
  await assert.rejects(normalizeProxyCompaction(Buffer.from(JSON.stringify(hugeCurrent)), { ...f.options, catalogPath }), { code: "context_budget_current_input_too_large" });
});

test("retained duplicate multiplicity, visible images, missing reasoning summary and cancellation", async t => {
  const image = { type: "input_image", image_url: "data:image/png;base64,aW1hZ2U=" };
  const f = await fixture(t, "gpt-6-astra", { guardian_history: [kept, kept, { type: "message", role: "user", content: [image] }] });
  const result = JSON.parse(await normalizeProxyCompaction(Buffer.from(JSON.stringify({ model: "gemini-4-argon-high", input: [capsule, kept, { ...reasoning, summary: [] }] })), f.options));
  assert.match(result.input[0].content[0].text, /Keep this current instruction/);
  assert.deepEqual(result.input[0].content[1], image);
  assert.match(result.input[2].content[0].text, /No visible summary/);
  const abort = new AbortController(); abort.abort();
  await assert.rejects(normalizeProxyCompaction(Buffer.from('{}'), { ...f.options, signal: abort.signal }), { name: "AbortError" });
});

test("large rollout is scanned with bounded memory and CRLF offsets", async t => {
  const f = await fixture(t);
  const file = await open(f.path, "w");
  try {
    const padding = JSON.stringify({ type: "unrelated", payload: "x".repeat(1024 * 1024) }) + "\r\n";
    for (let i = 0; i < 129; i++) await file.write(padding);
    await file.write(f.rows.map(JSON.stringify).join("\r\n") + "\r\n");
  } finally { await file.close(); }
  const body = Buffer.from(JSON.stringify({ model: "gemini-4-argon-high", input: [capsule, kept] }));
  const result = JSON.parse(await normalizeProxyCompaction(body, f.options));
  assert.match(result.input[0].content[0].text, /Remember A and next step B/);
  assert.deepEqual(result.input[1], kept);
});
