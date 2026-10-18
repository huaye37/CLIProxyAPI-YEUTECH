#!/usr/bin/env python3
"""Verify the edge's 128 MiB limit using its exact image and a synthetic provider.

No production configuration or credentials are mounted. Run on a host with enough
spare memory for a separate 3 GiB container, not alongside traffic on a small edge.
"""
import argparse
import concurrent.futures
import http.client
import http.server
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time
import uuid

LIMIT = 128 * 1024 * 1024


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--docker", default="docker")
    args = parser.parse_args()
    name = "edge-body-probe-" + uuid.uuid4().hex[:12]
    key = uuid.uuid4().hex
    hold = threading.Event()
    hold.set()
    received = threading.Event()
    calls = []

    class Upstream(http.server.BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            size = int(self.headers.get("Content-Length", "0"))
            remaining = size
            streaming = False
            while remaining:
                chunk = self.rfile.read(min(1024 * 1024, remaining))
                if not chunk:
                    return
                if b'"stream":true' in chunk or b'"stream": true' in chunk:
                    streaming = True
                remaining -= len(chunk)
            calls.append(size)
            received.set()
            if not hold.wait(150):
                return
            body = json.dumps({"id": "msg_probe", "type": "message", "role": "assistant",
                               "model": "claude-sonnet-4-6", "content": [{"type": "text", "text": "EDGE_PROBE_OK"}],
                               "stop_reason": "end_turn", "stop_sequence": None,
                               "usage": {"input_tokens": 1, "output_tokens": 1}}).encode()
            if streaming:
                message = json.loads(body)
                events = [
                    {"type": "message_start", "message": {**message, "content": [], "stop_reason": None}},
                    {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}},
                    {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "EDGE_PROBE_OK"}},
                    {"type": "content_block_stop", "index": 0},
                    {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": 1}},
                    {"type": "message_stop"},
                ]
                body = "".join("event: " + event["type"] + "\ndata: " + json.dumps(event) + "\n\n" for event in events).encode()
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream" if streaming else "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    server.daemon_threads = True
    threading.Thread(target=server.serve_forever, daemon=True).start()
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    started = False
    peak = [0]
    stop = threading.Event()

    def docker(*argv):
        return subprocess.check_output([args.docker, *argv], text=True).strip()

    def request(size, *, send=True, endpoint="/v1/chat/completions"):
        prefix = b'{"model":"claude-sonnet-4-6","max_tokens":8,"messages":[{"role":"user","content":"'
        suffix = b'"}],"stream":false}'
        if endpoint == "/v1/responses":
            prefix = b'{"model":"claude-sonnet-4-6","input":"'
            suffix = b'","stream":false}'
        c = http.client.HTTPConnection("127.0.0.1", port, timeout=180)
        try:
            c.putrequest("POST", endpoint)
            c.putheader("Authorization", "Bearer " + key)
            c.putheader("Content-Type", "application/json")
            c.putheader("Content-Length", str(size))
            c.endheaders()
            if send:
                c.send(prefix)
                remaining = size - len(prefix) - len(suffix)
                chunk = b"x" * (1024 * 1024)
                while remaining:
                    count = min(len(chunk), remaining)
                    c.send(chunk[:count])
                    remaining -= count
                c.send(suffix)
            response = c.getresponse()
            return response.status, response.read(65536), response.getheader("Retry-After")
        finally:
            c.close()

    def health():
        c = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
        try:
            c.request("GET", "/healthz")
            return json.loads(c.getresponse().read())
        finally:
            c.close()

    try:
        with tempfile.TemporaryDirectory(prefix=name) as temp:
            root = Path(temp)
            config = {"config-version": 8,
                      "server": {"host": "127.0.0.1", "port": port, "max-request-body-bytes": 64 * 1024 * 1024},
                      "access": {"api-keys": [key]}, "oauth": {"auth-dir": "/auth"},
                      "management": {"disable-control-panel": True},
                      "observability": {"logs": {"request-log": False, "error-logs-max-files": 0}},
                      "routing": {"retry": {"request-retry": 0}},
                      "api-keys": {"claude": [{"name": "synthetic", "base-url": f"http://127.0.0.1:{server.server_port}",
                                              "models": [{"name": "claude-sonnet-4-6"}],
                                              "keys": [{"api-key": "synthetic-only", "cloak": {"mode": "never"}}]}]}}
            file = root / "config.json"
            file.write_text(json.dumps(config))
            docker("run", "-d", "--name", name, "--network", "host", "--memory=3g", "--memory-swap=3g",
                   "--read-only", "--cap-drop=ALL", "--tmpfs", "/auth:rw,size=1m", "--tmpfs", "/tmp:rw,size=64m",
                   "-e", "GOMEMLIMIT=2GiB", "-e", "GOMAXPROCS=2", "-e", "YEUTECH_MEMORY_GUARD=1",
                   "-e", "YEUTECH_MEMORY_MAX_REQUESTS=2", "-v", f"{root}:/probe:ro",
                   args.image, "./CLIProxyAPI", "-config", "/probe/config.json", "-local-model")
            started = True
            for _ in range(100):
                try:
                    if health().get("status") == "ok":
                        break
                except (OSError, ValueError):
                    pass
                stop.wait(0.1)
            else:
                raise RuntimeError("probe failed to start")
            # Health becomes ready before the asynchronous config watcher.
            # Production has a settled watcher; wait for the same condition.
            for _ in range(100):
                if "file watcher started" in docker("logs", name):
                    break
                stop.wait(0.1)
            else:
                raise RuntimeError("config watcher did not start")
            original = json.loads(docker("inspect", name))[0]
            assert request(97919776, send=False)[0] == 413
            config["server"]["max-request-body-bytes"] = LIMIT
            # The current runtime watches the file inode, not its parent.
            # Preserve that inode; atomic rename does not reload this build.
            fd = os.open(file, os.O_RDWR)
            try:
                data = json.dumps(config).encode()
                assert os.write(fd, data) == len(data)
                os.ftruncate(fd, len(data))
                os.fsync(fd)
            finally:
                os.close(fd)
            stop.wait(2)

            def sample_memory():
                while not stop.wait(0.25):
                    try:
                        raw = docker("exec", name, "sh", "-c", "cat /sys/fs/cgroup/memory.current 2>/dev/null || cat /sys/fs/cgroup/memory/memory.usage_in_bytes")
                        peak[0] = max(peak[0], int(raw))
                    except (subprocess.SubprocessError, ValueError):
                        return

            sampler = threading.Thread(target=sample_memory, daemon=True)
            sampler.start()
            hold.clear()
            with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
                pending = pool.submit(request, 97919776)
                deadline = time.monotonic() + 120
                while not received.wait(0.1):
                    if pending.done():
                        raise RuntimeError("request failed before mock upstream: " + str(pending.result()[0]))
                    if time.monotonic() >= deadline:
                        raise RuntimeError("original-size request did not reach mock Claude")
                count = len(calls)
                busy = request(97919776, send=False)
                assert busy[0] == 503 and b"proxy_memory_busy" in busy[1] and busy[2]
                assert len(calls) == count
                hold.set()
                result = pending.result(timeout=150)
                assert result[0] == 200 and b"EDGE_PROBE_OK" in result[1], (result[0], result[1][:500])
            for endpoint in ("/v1/chat/completions", "/v1/responses"):
                result = request(LIMIT, endpoint=endpoint)
                assert result[0] == 200 and b"EDGE_PROBE_OK" in result[1], (endpoint, result[0])
            assert request(LIMIT + 1, send=False)[0] == 413
            assert health()["memoryGuard"]["activeRequests"] == 0
            state = json.loads(docker("inspect", name))[0]
            assert not state["State"]["OOMKilled"] and state["RestartCount"] == 0
            assert state["State"]["StartedAt"] == original["State"]["StartedAt"]
            stop.set()
            sampler.join(timeout=3)
            try:
                actual_peak = docker("exec", name, "sh", "-c", "cat /sys/fs/cgroup/memory.peak 2>/dev/null || cat /sys/fs/cgroup/memory/memory.max_usage_in_bytes")
                peak[0] = max(peak[0], int(actual_peak))
            except (subprocess.SubprocessError, ValueError):
                pass
            assert 0 < peak[0] < 3 * 1024**3
            print(json.dumps({"imageId": state["Image"], "originalSizeBytes": 97919776,
                              "originalRejection": 413, "hotReloadedWithoutRestart": True,
                              "originalSizeStatus": 200, "chat128MiBStatus": 200, "responses128MiBStatus": 200,
                              "parallelLargeStatus": 503, "above128MiBStatus": 413,
                              "peakMemoryBytes": peak[0], "oom": False, "paidCalls": 0, "productionChanged": False}))
    finally:
        hold.set()
        stop.set()
        if started:
            subprocess.run([args.docker, "rm", "-f", name], check=True, stdout=subprocess.DEVNULL)
        server.shutdown()
        server.server_close()


if __name__ == "__main__":
    main()
