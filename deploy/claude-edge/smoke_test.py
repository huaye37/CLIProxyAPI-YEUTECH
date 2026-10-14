#!/usr/bin/env python3
"""Exercise two isolated proxy layers against a local synthetic Claude upstream."""
import concurrent.futures
import http.server
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import time
import urllib.error
import urllib.request


class MockClaude(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    held = 0
    changed = threading.Condition()
    release = threading.Event()
    metadata_observations = []

    def log_message(self, *_args):
        pass

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        encoded = json.dumps(request)
        if "BODY_CONTEXT_SENTINEL" in encoded:
            with self.changed:
                self.metadata_observations.append({
                    "caller_header_leaked": any("CALLER_PRIVATE_SENTINEL" in value for value in self.headers.values()),
                    "body_context_preserved": "Asia/Shanghai /Users/example BODY_CONTEXT_SENTINEL" in encoded,
                })
        if "HOLD_TEST" in encoded:
            with self.changed:
                type(self).held += 1
                self.changed.notify_all()
            self.release.wait(15)
        has_result = '"type": "tool_result"' in encoded
        call_tool = "CALL_TOOL_TEST" in encoded and not has_result
        content = ([{"type": "tool_use", "id": "tool_bridge_1", "name": "echo", "input": {"text": "BRIDGE_TOOL_OK"}}]
                   if call_tool else [{"type": "text", "text": "BRIDGE_TOOL_OK" if has_result else "BRIDGE_TEXT_OK"}])
        stop = "tool_use" if call_tool else "end_turn"
        message = {"id": "msg_bridge_1", "type": "message", "role": "assistant",
                   "model": request["model"], "content": content, "stop_reason": stop,
                   "stop_sequence": None, "usage": {"input_tokens": 10, "output_tokens": 5}}
        if not request.get("stream"):
            body = json.dumps(message).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        start = dict(message, content=[], stop_reason=None, usage={"input_tokens": 10, "output_tokens": 0})
        events = [{"type": "message_start", "message": start}]
        for index, block in enumerate(content):
            initial = dict(block)
            if block["type"] == "text":
                initial["text"] = ""
                delta = {"type": "text_delta", "text": block["text"]}
            else:
                initial["input"] = {}
                delta = {"type": "input_json_delta", "partial_json": json.dumps(block["input"])}
            events.extend([{"type": "content_block_start", "index": index, "content_block": initial},
                           {"type": "content_block_delta", "index": index, "delta": delta},
                           {"type": "content_block_stop", "index": index}])
        events.extend([{"type": "message_delta", "delta": {"stop_reason": stop, "stop_sequence": None},
                        "usage": {"output_tokens": 5}}, {"type": "message_stop"}])
        for event in events:
            self.wfile.write(("event: " + event["type"] + "\ndata: " + json.dumps(event) + "\n\n").encode())
            self.wfile.flush()
        self.close_connection = True


def request(port, path, body=None, key="smoke-front-key", extra_headers=None):
    data = json.dumps(body).encode() if body is not None else None
    headers = {"Authorization": "Bearer " + key, "Content-Type": "application/json"}
    headers.update(extra_headers or {})
    req = urllib.request.Request(f"http://127.0.0.1:{port}{path}", data=data,
                                 headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=20) as response:
            return response.status, response.read().decode()
    except urllib.error.HTTPError as error:
        return error.code, error.read().decode()


def configuration(port, access_key, upstream, provider_key):
    return {"config-version": 8,
            "server": {"host": "127.0.0.1", "port": port, "max-request-body-bytes": 524288, "commercial-mode": True},
            "access": {"api-keys": [access_key]},
            "management": {"secret-key": "", "disable-control-panel": True},
            "oauth": {"auth-dir": "/auth"},
            "routing": {"retry": {"request-retry": 0, "max-retry-credentials": 1}},
            "upstream": {"claude": {"disable-claude-cloak-mode": True, "disable-cloaking-model-list": True}},
            "api-keys": {"claude": [{"name": "mock", "base-url": upstream,
                                      "models": [{"name": "claude-sonnet-4-6", "alias": "claude-sonnet-4-6"}],
                                      "keys": [{"api-key": provider_key, "cloak": {"mode": "never"}}]}]}}


def main():
    image = sys.argv[1]
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), MockClaude)
    server.daemon_threads = True
    threading.Thread(target=server.serve_forever, daemon=True).start()
    names = []
    results = []
    try:
        with tempfile.TemporaryDirectory(prefix="claude-edge-smoke-") as temporary:
            root = Path(temporary)
            control = root / "control"
            control.mkdir()
            gate = control / "gate.json"
            gate.write_text(json.dumps({"enabled": True, "maintenance": False}))
            layers = [(19862, "smoke-edge-key", f"http://127.0.0.1:{server.server_port}", "synthetic-provider-key"),
                      (19863, "smoke-front-key", "http://127.0.0.1:19862", "smoke-edge-key")]
            for port, key, upstream, provider_key in layers:
                config = root / f"{port}.json"
                config.write_text(json.dumps(configuration(port, key, upstream, provider_key)))
                name = f"claude-edge-smoke-{port}"
                subprocess.run(["docker", "run", "-d", "--name", name, "--network", "host",
                                "--memory=768m", "--memory-swap=768m", "--read-only", "--cap-drop=ALL",
                                "--tmpfs", "/auth:rw,noexec,nosuid,size=1m", "--tmpfs", "/tmp:rw,noexec,nosuid,size=16m",
                                "-e", "YEUTECH_MEMORY_GUARD=1", "-e", "YEUTECH_MEMORY_MAX_REQUESTS=2",
                                "-e", f"YEUTECH_STRIP_CLIENT_METADATA={int(port == 19862)}",
                                "-e", "YEUTECH_EDGE_CONTROL_FILE=/edge-control/gate.json" if port == 19862 else "YEUTECH_EDGE_CONTROL_FILE=",
                                "-v", f"{control}:/edge-control:ro",
                                "-e", "GOMEMLIMIT=512MiB", "-v", f"{config}:/run/config.json:ro", image,
                                "./CLIProxyAPI", "-config", "/run/config.json", "-local-model"],
                               check=True, stdout=subprocess.DEVNULL)
                names.append(name)
                deadline = time.monotonic() + 15
                while True:
                    try:
                        status, _ = request(port, "/healthz", key=key)
                        if status == 200:
                            break
                    except OSError:
                        pass
                    if time.monotonic() > deadline:
                        raise RuntimeError("smoke proxy did not become ready")
                    threading.Event().wait(0.1)
            assert request(19863, "/v1/models", key="wrong-key")[0] == 401
            results.append("unauthorized_rejected")
            edge_health = json.loads(request(19862, "/healthz", key="smoke-edge-key")[1])
            assert edge_health["clientMetadataPrivacy"]["bodyRedaction"] is False
            assert "clientMetadataPrivacy" not in json.loads(request(19863, "/healthz")[1])
            private_headers = {name: "CALLER_PRIVATE_SENTINEL" for name in (
                "User-Agent", "X-Stainless-OS", "X-Stainless-Arch", "X-Stainless-Runtime-Version",
                "X-Forwarded-For", "X-Real-IP", "X-Timezone", "X-Geo-City", "Sec-CH-UA-Platform",
                "X-Client-App", "X-Claude-Remote-Container-Id")}
            private_body = "Asia/Shanghai /Users/example BODY_CONTEXT_SENTINEL"
            for endpoint, payload in (
                ("/v1/messages", {"model": "claude-sonnet-4-6", "max_tokens": 64,
                                  "messages": [{"role": "user", "content": private_body}]}),
                ("/v1/responses", {"model": "claude-sonnet-4-6", "input": private_body, "stream": True}),
            ):
                status, body = request(19863, endpoint, payload, extra_headers=private_headers)
                assert status == 200 and "BRIDGE_TEXT_OK" in body, (status, body[:1000])
            assert len(MockClaude.metadata_observations) == 2
            assert all(not item["caller_header_leaked"] and item["body_context_preserved"]
                       for item in MockClaude.metadata_observations), MockClaude.metadata_observations
            results.append("caller_environment_headers_stripped_body_context_preserved")
            message = {"model": "claude-sonnet-4-6", "max_tokens": 64,
                       "messages": [{"role": "user", "content": "TEXT_TEST"}]}
            status, body = request(19863, "/v1/messages", message)
            assert status == 200 and "BRIDGE_TEXT_OK" in body, (status, body[:1000])
            results.append("messages_two_layers")
            response = {"model": "claude-sonnet-4-6", "input": "TEXT_TEST", "stream": False}
            status, body = request(19863, "/v1/responses", response)
            assert status == 200 and "BRIDGE_TEXT_OK" in body, (status, body[:1000])
            results.append("responses_two_layers")
            status, body = request(19863, "/v1/responses", dict(response, stream=True))
            assert status == 200 and "response.completed" in body and "BRIDGE_TEXT_OK" in body, (status, body[:1000])
            results.append("responses_stream_completed")
            original = {"role": "user", "content": "CALL_TOOL_TEST"}
            tool_request = {"model": "claude-sonnet-4-6", "input": [original],
                            "tools": [{"type": "function", "name": "echo", "description": "Echo text",
                                       "parameters": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}}]}
            status, body = request(19863, "/v1/responses", tool_request)
            assert status == 200, (status, body[:1000])
            call = next(item for item in json.loads(body)["output"] if item["type"] == "function_call")
            assert call["name"] == "echo" and json.loads(call["arguments"])["text"] == "BRIDGE_TOOL_OK"
            tool_request["input"] = [original, call, {"type": "function_call_output", "call_id": call["call_id"], "output": "BRIDGE_TOOL_OK"}]
            status, body = request(19863, "/v1/responses", tool_request)
            assert status == 200 and "BRIDGE_TOOL_OK" in body, (status, body[:1000])
            results.append("tool_call_result_round_trip")
            assert request(19863, "/v1/responses", dict(response, input="x" * 524289))[0] == 413
            results.append("oversize_rejected")
            with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
                futures = [pool.submit(request, 19863, "/v1/messages", dict(message, messages=[{"role": "user", "content": "HOLD_TEST"}])) for _ in range(2)]
                with MockClaude.changed:
                    assert MockClaude.changed.wait_for(lambda: MockClaude.held == 2, timeout=10), "two requests did not reach the upstream"
                status, body = request(19863, "/v1/messages", message)
                MockClaude.release.set()
                assert status == 503 and "proxy_memory_busy" in body, (status, body[:1000])
                assert all(f.result()[0] == 200 for f in futures)
            results.append("concurrency_rejected_and_recovered")
            gate.write_text(json.dumps({"enabled": True, "maintenance": True}))
            status, body = request(19862, "/v1/messages", message, key="smoke-edge-key")
            assert status == 503 and "edge_maintenance" in body
            closed = json.loads(request(19862, "/healthz", key="smoke-edge-key")[1])["edgeControl"]
            assert closed["accepting"] is False and closed["activeRequests"] == 0
            gate.write_text(json.dumps({"enabled": True, "maintenance": False}))
            assert request(19862, "/v1/messages", message, key="smoke-edge-key")[0] == 200
            results.append("edge_admission_close_and_reopen")
            assert request(19863, "/v1/messages", message)[0] == 200
            print(json.dumps({"status": "passed", "checks": results, "external_model_calls": 0}))
    finally:
        MockClaude.release.set()
        server.shutdown()
        for name in names:
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
