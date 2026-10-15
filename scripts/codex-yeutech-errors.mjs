// Convert a known rate-limit failure into a safe, provider-neutral display message.
const quotaCodes = new Set(["insufficient_quota", "quota_exhausted", "usage_limit_reached", "credit_balance_exhausted"]);
const concurrencyCodes = new Set(["concurrency_limit", "concurrency_limit_exceeded", "too_many_concurrent_requests"]);
const maximumRetrySeconds = 8 * 24 * 60 * 60;

function retrySeconds(headers, error, now) {
  const raw = headers["retry-after"];
  let seconds;
  if (typeof raw === "string" && /^\d+(?:\.\d+)?$/.test(raw.trim())) seconds = Number(raw);
  else if (typeof raw === "string" && Number.isFinite(Date.parse(raw))) seconds = (Date.parse(raw) - now) / 1000;
  if (seconds === undefined && typeof error.reset_seconds === "number") seconds = error.reset_seconds;
  return Number.isFinite(seconds) && seconds >= 0 && seconds <= maximumRetrySeconds ? Math.ceil(seconds) : null;
}

export function rateLimitDisplay(body, headers, now = Date.now()) {
  let payload;
  try { payload = JSON.parse(body); } catch { return null; }
  const error = payload?.error;
  if (!error || typeof error !== "object" || Array.isArray(error)) return null;
  const code = typeof error.code === "string" ? error.code.toLowerCase() : "";
  const type = typeof error.type === "string" ? error.type.toLowerCase() : "";
  const upstreamMessage = typeof error.message === "string" ? error.message.toLowerCase() : "";
  const model = typeof error.model === "string" ? error.model : "";
  const label = /^claude-[a-z0-9.-]+-tokyo$/.test(model) ? "Claude · 东京" : "模型服务";
  let reason = "rate_limit";
  let message;
  if (concurrencyCodes.has(code) || /too many concurrent|concurrency limit|maximum concurrent/.test(upstreamMessage)) {
    reason = "concurrency_limit";
    message = "并发请求已满，请等待现有请求结束后重试。";
  } else if (/fast/.test(upstreamMessage) && /credits|required|not included/.test(upstreamMessage)) {
    reason = "speed_entitlement";
    message = "当前账户未获准使用加速模式，请切回标准速度后重试。";
  } else if (quotaCodes.has(code) || quotaCodes.has(type)) {
    reason = "quota_exhausted";
    message = "可用额度已耗尽。";
  } else if (code === "model_cooldown") {
    reason = "model_cooldown";
    message = "当前模型处于上游限额冷却期，暂时无法调用。";
  } else {
    message = "上游正在限制请求频率，请稍后重试。";
  }
  const seconds = retrySeconds(headers, error, now);
  const resetsAt = seconds === null ? null : now + seconds * 1000;
  if (resetsAt !== null) {
    const when = new Intl.DateTimeFormat("zh-CN", { timeZone: "Asia/Shanghai", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hourCycle: "h23" }).format(resetsAt);
    message += `上游建议重试时间：${when}（北京时间）。`;
  }
  if (reason !== "speed_entitlement" && reason !== "concurrency_limit") message += "可到 API 平台额度中心查看详情，或切换其他渠道继续。";
  return { message: `${label}：${message}`, reason, retryAfterSeconds: seconds, resetsAt };
}

export function isCodexStreamingResponse(request) {
  const pathname = new URL(request.url || "/", "http://127.0.0.1").pathname;
  const originator = String(request.headers.originator || "");
  const userAgent = String(request.headers["user-agent"] || "");
  return request.method === "POST" && pathname === "/v1/responses"
    && String(request.headers.accept || "").toLowerCase().includes("text/event-stream")
    && (/^codex(?: desktop|[-_]tui|_cli_rs)(?:\/|$)/i.test(originator)
      || /^codex(?: desktop|[-_]cli_rs|_cli_rs)(?:\/|$)/i.test(userAgent));
}
