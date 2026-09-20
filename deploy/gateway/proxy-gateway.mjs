import fs from 'node:fs';
import http from 'node:http';

const stateFile = process.env.GATEWAY_STATE_FILE || '/state/active.json';
const listenHost = '127.0.0.1';
const listenPort = Number(process.env.GATEWAY_PORT || 18319);
const adminPort = Number(process.env.GATEWAY_ADMIN_PORT || 18322);
const allowedPorts = new Set((process.env.GATEWAY_BACKEND_PORTS || '18317,18318,18315').split(',').map(Number));
const active = new Map([...allowedPorts].map((port) => [port, 0]));
const requests = new Map();
const totals = { completed: 0, disconnected: 0, upstreamErrors: 0 };
let sequence = 0;

function backendPort() {
  const value = JSON.parse(fs.readFileSync(stateFile, 'utf8'));
  const port = Number(value.port);
  if (!allowedPorts.has(port)) throw new Error('active backend port is not allowed');
  return port;
}

function enter(port, kind = 'http') {
  const id = ++sequence;
  requests.set(id, { port, kind, startedAt: Date.now() });
  active.set(port, (active.get(port) || 0) + 1);
  let released = false;
  return (reason = 'completed') => {
    if (released) return;
    released = true;
    requests.delete(id);
    totals[reason] = (totals[reason] || 0) + 1;
    active.set(port, Math.max(0, (active.get(port) || 1) - 1));
  };
}

function proxyRequest(req, res) {
  let port;
  try { port = backendPort(); } catch { res.writeHead(503, { 'content-type': 'application/json' }); return res.end('{"error":"proxy backend unavailable"}'); }
  const leave = enter(port);
  let response;
  const cancel = (reason) => {
    leave(reason);
    req.unpipe(upstream);
    response?.destroy();
    upstream.destroy();
  };
  const upstream = http.request({ host: listenHost, port, method: req.method, path: req.url, headers: req.headers }, (response) => {
    attachResponse(response);
  });
  function attachResponse(incoming) {
    response = incoming;
    if (res.destroyed) { cancel('disconnected'); return; }
    res.writeHead(response.statusCode || 502, response.headers);
    response.pipe(res);
    response.once('error', () => { cancel('upstreamErrors'); res.destroy(); });
    response.once('close', () => {
      if (!response.complete) { cancel('upstreamErrors'); res.destroy(); }
    });
  }
  upstream.once('error', () => {
    leave('upstreamErrors');
    if (res.destroyed) return;
    if (res.headersSent) { res.destroy(); return; }
    res.writeHead(502, { 'content-type': 'application/json' });
    res.end('{"error":"proxy backend unavailable"}');
  });
  res.once('finish', () => leave('completed'));
  res.once('close', () => {
    if (!res.writableFinished) cancel('disconnected');
  });
  res.once('error', () => cancel('disconnected'));
  req.once('aborted', () => { cancel('disconnected'); res.destroy(); });
  req.once('error', () => { cancel('disconnected'); res.destroy(); });
  req.pipe(upstream);
}

const gateway = http.createServer(proxyRequest);
gateway.on('upgrade', (req, socket, head) => {
  let port;
  try { port = backendPort(); } catch { return socket.destroy(); }
  const leave = enter(port, 'websocket');
  let peer;
  const upstream = http.request({ host: listenHost, port, method: req.method, path: req.url, headers: req.headers });
  const close = (reason) => {
    leave(reason);
    peer?.destroy();
    upstream.destroy();
    socket.destroy();
  };
  socket.once('close', () => close('disconnected'));
  socket.once('end', () => close('disconnected'));
  socket.once('error', () => close('disconnected'));
  upstream.on('upgrade', (response, upstreamSocket, upstreamHead) => {
    peer = upstreamSocket;
    if (socket.destroyed) { close('disconnected'); return; }
    upstreamSocket.once('error', () => close('upstreamErrors'));
    upstreamSocket.once('close', () => close('completed'));
    upstreamSocket.once('end', () => close('completed'));
    const lines = [`HTTP/1.1 ${response.statusCode || 101} ${response.statusMessage || 'Switching Protocols'}`];
    for (let index = 0; index < response.rawHeaders.length; index += 2) lines.push(`${response.rawHeaders[index]}: ${response.rawHeaders[index + 1]}`);
    socket.write(lines.join('\r\n') + '\r\n\r\n');
    if (head.length) upstreamSocket.write(head);
    if (upstreamHead.length) socket.write(upstreamHead);
    socket.pipe(upstreamSocket).pipe(socket);
  });
  upstream.once('response', (response) => { response.destroy(); close('upstreamErrors'); });
  upstream.once('error', () => close('upstreamErrors'));
  upstream.end();
});

const admin = http.createServer((req, res) => {
  if (req.method !== 'GET' || req.url !== '/status') { res.writeHead(404); return res.end(); }
  let port = null;
  try { port = backendPort(); } catch {}
  const counts = Object.fromEntries([...active].map(([key, value]) => [String(key), value]));
  const now = Date.now();
  const oldestRequestAgeMs = Object.fromEntries([...allowedPorts].map((port) => [String(port), 0]));
  for (const request of requests.values()) oldestRequestAgeMs[request.port] = Math.max(oldestRequestAgeMs[request.port], now - request.startedAt);
  const body = JSON.stringify({ ok: port !== null, version: 'lifecycle-v3', activePort: port,
    activeRequests: counts, oldestRequestAgeMs, totals });
  res.writeHead(200, { 'content-type': 'application/json', 'content-length': Buffer.byteLength(body), 'cache-control': 'no-store' });
  res.end(body);
});

gateway.listen(listenPort, listenHost);
admin.listen(adminPort, listenHost);

for (const signal of ['SIGTERM', 'SIGINT']) process.on(signal, () => {
  gateway.close(() => admin.close(() => process.exit(0)));
});
