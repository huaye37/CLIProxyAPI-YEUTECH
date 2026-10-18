// Standalone search is a separate Codex service, not an inference-model call.
// Never rewrite inference requests or the provider's token accounting.
export const SEARCH_BODY_LIMIT = 16 * 1024 * 1024;

export function isCodexStandaloneSearch(request) {
  const path = new URL(request.url || "/", "http://127.0.0.1").pathname;
  const originator = String(request.headers.originator || "");
  const userAgent = String(request.headers["user-agent"] || "");
  return request.method === "POST" && path === "/v1/alpha/search"
    && (/codex|chatgpt_cca/i.test(originator) || /codex/i.test(userAgent));
}

export function routeStandaloneSearch(body, searchModel = "gpt-6.1-sol") {
  const payload = JSON.parse(body.toString("utf8"));
  if (!payload || typeof payload !== "object" || Array.isArray(payload)
      || typeof payload.model !== "string" || !payload.model.trim()) {
    throw new Error("Invalid standalone search request");
  }
  // GPT already selects a Codex credential. Non-GPT inference models cannot
  // select one: keep their inference unchanged and select the verified search
  // service model only on /alpha/search. No fallback to another inference API.
  if (/^gpt-/i.test(payload.model)) return body;
  if (!/^gpt-[a-z0-9.-]+$/i.test(searchModel)) throw new Error("Invalid search service model");
  return Buffer.from(JSON.stringify({ ...payload, model: searchModel }));
}

export async function readSearchBody(request, limit = SEARCH_BODY_LIMIT) {
  const chunks = [];
  let bytes = 0;
  // Do not destroy the socket when rejecting a large body; the caller must be
  // able to deliver HTTP 413. Successful inference streams never enter here.
  return new Promise((resolve, reject) => {
    const cleanup = () => {
      request.off("data", onData);
      request.off("end", onEnd);
      request.off("error", onError);
      request.off("aborted", onAborted);
    };
    const fail = (error) => { cleanup(); chunks.length = 0; request.resume(); reject(error); };
    const onData = (chunk) => {
      bytes += chunk.length;
      if (bytes > limit) {
        fail(Object.assign(new Error("Standalone search body exceeds limit"), { statusCode: 413 }));
      } else chunks.push(chunk);
    };
    const onEnd = () => { cleanup(); resolve(Buffer.concat(chunks)); };
    const onError = () => fail(new Error("Unable to read standalone search request"));
    const onAborted = () => fail(new Error("Standalone search request cancelled"));
    request.on("data", onData);
    request.once("end", onEnd);
    request.once("error", onError);
    request.once("aborted", onAborted);
  });
}
