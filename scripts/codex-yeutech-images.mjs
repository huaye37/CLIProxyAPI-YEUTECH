// Local outbound copies only: never rewrite conversation storage or source images.
import { execFile } from "node:child_process";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { promisify } from "node:util";

const run = promisify(execFile);
const maxImageBytes = 20 * 1024 * 1024;
const maxPixels = 64 * 1024 * 1024;
export const imageBodyLimit = 32 * 1024 * 1024;

export function isImageRequest(request) {
  const path = new URL(request.url || "/", "http://127.0.0.1").pathname;
  return request.method === "POST" && ["/v1/responses", "/v1/responses/compact"].includes(path)
    && /^application\/json(?:;|$)/i.test(request.headers["content-type"] || "")
    && (!request.headers["content-encoding"] || request.headers["content-encoding"] === "identity");
}

// On overflow, leave the unread stream intact and replay the prefix verbatim.
// Large/encoded/non-target requests retain their existing upstream admission rules.
export function readImageBody(request, limit = imageBodyLimit) {
  return new Promise((resolve, reject) => {
    let bytes = 0;
    const chunks = [];
    const cleanup = () => {
      request.off("data", data); request.off("end", end);
      request.off("error", error); request.off("aborted", aborted);
    };
    const end = () => { cleanup(); resolve({ body: Buffer.concat(chunks), complete: true }); };
    const error = err => { cleanup(); reject(err); };
    const aborted = () => error(new Error("Request aborted"));
    const data = chunk => {
      chunks.push(chunk); bytes += chunk.length;
      if (bytes > limit) {
        request.pause(); cleanup();
        resolve({ body: Buffer.concat(chunks), complete: false });
      }
    };
    request.on("data", data); request.once("end", end);
    request.once("error", error); request.once("aborted", aborted);
    if (request.aborted) aborted();
  });
}

export function imageDimensions(b) {
  if (b.length >= 24 && b.subarray(0, 8).equals(Buffer.from([137,80,78,71,13,10,26,10]))) {
    return { width: b.readUInt32BE(16), height: b.readUInt32BE(20) };
  }
  if (b.length >= 10 && /^GIF8[79]a$/.test(b.toString("ascii", 0, 6))) {
    return { width: b.readUInt16LE(6), height: b.readUInt16LE(8) };
  }
  if (b.length >= 30 && b.toString("ascii", 0, 4) === "RIFF" && b.toString("ascii", 8, 12) === "WEBP") {
    const kind = b.toString("ascii", 12, 16);
    if (kind === "VP8X") return { width: 1 + b.readUIntLE(24, 3), height: 1 + b.readUIntLE(27, 3) };
    if (kind === "VP8 " && b.subarray(23, 26).equals(Buffer.from([157,1,42]))) {
      return { width: b.readUInt16LE(26) & 16383, height: b.readUInt16LE(28) & 16383 };
    }
    if (kind === "VP8L" && b[20] === 47) {
      const bits = b.readUInt32LE(21);
      return { width: 1 + (bits & 16383), height: 1 + ((bits >>> 14) & 16383) };
    }
  }
  if (b.length < 4 || b[0] !== 255 || b[1] !== 216) return null;
  let p = 2;
  while (p + 4 <= b.length) {
    if (b[p++] !== 255) return null;
    while (b[p] === 255) p++;
    const marker = b[p++];
    if (marker === 217 || marker === 218 || p + 2 > b.length) return null;
    if (marker === 1 || (marker >= 208 && marker <= 215)) continue;
    const length = b.readUInt16BE(p);
    if (length < 2 || p + length > b.length) return null;
    if ([192,193,194,195,197,198,199,201,202,203,205,206,207].includes(marker) && length >= 8) {
      return { width: b.readUInt16BE(p + 5), height: b.readUInt16BE(p + 3) };
    }
    p += length;
  }
  return null;
}

export async function resizeImage(bytes, mime, limit, signal) {
  const dir = await mkdtemp(join(tmpdir(), "yeutech-image-copy-"));
  try {
    const input = join(dir, "input");
    const output = join(dir, "output");
    await writeFile(input, bytes, { mode: 0o600 });
    signal?.throwIfAborted();
    const jpeg = mime === "image/jpeg";
    await run("/usr/bin/sips", ["--resampleHeightWidthMax", String(limit), "--setProperty", "format", jpeg ? "jpeg" : "png",
      ...(jpeg ? ["--setProperty", "formatOptions", "90"] : []), input, "--out", output],
    { signal, timeout: 15000, maxBuffer: 64 * 1024 });
    const result = await readFile(output);
    const size = imageDimensions(result);
    if (!size || size.width < 1 || size.height < 1 || Math.max(size.width, size.height) > limit || result.length > maxImageBytes) {
      throw new Error("Invalid resized image");
    }
    return { bytes: result, mime: jpeg ? "image/jpeg" : "image/png", ...size };
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
}

function imageArrays(input) {
  if (!Array.isArray(input)) return [];
  const arrays = [];
  for (const item of input) {
    if (!item || typeof item !== "object") continue;
    if ((!item.type || item.type === "message") && Array.isArray(item.content)) arrays.push(item.content);
    if (item.type === "function_call_output" && Array.isArray(item.output)) arrays.push(item.output);
    if (item.type === "computer_call_output" && item.output?.type === "computer_screenshot") arrays.push([item.output]);
  }
  return arrays;
}

export async function normalizeClaudeImages(body, { signal, resize = resizeImage } = {}) {
  let payload;
  try { payload = JSON.parse(body.toString("utf8")); } catch { return body; }
  // Only the existing, verified Tokyo aliases have the observed 3000px limit.
  if (!/^claude-[a-z0-9.-]+-tokyo$/.test(payload?.model || "")) return body;
  const arrays = imageArrays(payload.input);
  const images = arrays.flat().filter(part => part?.type === "input_image" || part?.type === "computer_screenshot");
  if (images.length <= 20) return body;
  const copies = new Map();
  let changed = false;
  for (const part of images) {
    signal?.throwIfAborted();
    // Native computer tool coordinates have a separate contract; do not rescale
    // their screenshots without a matching coordinate adapter.
    if (part.type !== "input_image") continue;
    const url = part.image_url;
    if (typeof url !== "string") continue;
    const match = /^data:(image\/(?:png|jpeg|gif|webp));base64,([A-Za-z0-9+/]+={0,2})$/.exec(url);
    if (!match) continue; // Never fetch remote URLs or dereference file IDs.
    if (match[2].length > Math.ceil(maxImageBytes / 3) * 4) throw new Error("Image exceeds local processing budget");
    const bytes = Buffer.from(match[2], "base64");
    const size = imageDimensions(bytes);
    if (!size || Math.max(size.width, size.height) <= 3000) continue;
    if (size.width * size.height > maxPixels) throw new Error("Image exceeds local pixel budget");
    let copy = copies.get(url);
    if (!copy) {
      copy = await resize(bytes, match[1], 3000, signal);
      copy.url = `data:${copy.mime};base64,${copy.bytes.toString("base64")}`;
      copies.set(url, copy);
    }
    part.image_url = copy.url;
    // Pixel-coordinate tools need the original dimensions, even for historical images.
    const array = arrays.find(parts => parts.includes(part));
    if (part.type === "input_image") array.splice(array.indexOf(part), 0, { type: "input_text", text:
      `[Image transmission copy resized from ${size.width}x${size.height} to ${copy.width}x${copy.height} to meet the provider's multi-image limit. Original screen/file coordinates are unchanged; map image coordinates by x*${size.width}/${copy.width}, y*${size.height}/${copy.height} when using coordinate tools.]` });
    changed = true;
  }
  return changed ? Buffer.from(JSON.stringify(payload)) : body;
}
