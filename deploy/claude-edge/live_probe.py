#!/usr/bin/env python3
"""Run explicit, small acceptance probes using a bridge key read only at runtime."""
import argparse
import http.client
import json
from pathlib import Path
import time


def connect(args):
    connection = http.client.HTTPConnection("127.0.0.1", args.port, timeout=10)
    connection.connect()
    connection.sock.settimeout(None)
    return connection


def call(args, path, body=None):
    started = time.monotonic()
    connection = connect(args)
    headers = {"Authorization": "Bearer " + args.key, "Content-Type": "application/json"}
    try:
        connection.request("POST" if body is not None else "GET", path,
                           body=json.dumps(body).encode() if body is not None else None,
                           headers=headers)
        response = connection.getresponse()
        raw = response.read()
        if response.status != 200:
            try:
                payload = json.loads(raw)
                error = payload.get("error", payload)
                message = str(error.get("message", error) if isinstance(error, dict) else error)
            except (ValueError, TypeError):
                message = "Non-JSON error response"
            raise RuntimeError("HTTP %d: %s" % (response.status, message.replace(args.key, "[redacted]")[:500]))
        return json.loads(raw), round(time.monotonic() - started, 3)
    finally:
        connection.close()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=["catalog", "text", "stream", "tools"])
    parser.add_argument("--key-file", required=True)
    parser.add_argument("--port", type=int, default=18340)
    parser.add_argument("--model")
    args = parser.parse_args()
    args.key = Path(args.key_file).read_text().strip()
    if args.mode == "catalog":
        result, elapsed = call(args, "/v1/models")
        print(json.dumps({"mode": "catalog", "seconds": elapsed,
                          "models": [{"id": item["id"], "owned_by": item.get("owned_by")}
                                     for item in result.get("data", [])]}))
        return
    if not args.model:
        parser.error("--model is required for a real inference probe")
    if args.mode == "text":
        result, elapsed = call(args, "/v1/messages", {
            "model": args.model, "max_tokens": 128,
            "messages": [{"role": "user", "content": "Reply with exactly TOKYO_OK and nothing else."}]})
        text = "".join(item.get("text", "") for item in result.get("content", []) if item.get("type") == "text")
        assert "TOKYO_OK" in text, "Expected test marker missing"
        print(json.dumps({"mode": "text", "status": "passed", "seconds": elapsed,
                          "model": result.get("model"), "stop_reason": result.get("stop_reason"),
                          "usage": result.get("usage"), "marker_received": True}))
        return
    if args.mode == "stream":
        connection = connect(args)
        started = time.monotonic()
        first_delta = None
        completed = False
        text = ""
        event_types = set()
        usage = None
        try:
            body = {"model": args.model, "stream": True, "max_output_tokens": 128,
                    "input": "Reply with exactly TOKYO_STREAM_OK and nothing else."}
            connection.request("POST", "/v1/responses", body=json.dumps(body).encode(),
                               headers={"Authorization": "Bearer " + args.key, "Content-Type": "application/json"})
            response = connection.getresponse()
            assert response.status == 200, "Stream HTTP %d" % response.status
            for line in response:
                if not line.startswith(b"data:"):
                    continue
                data = line[5:].strip()
                if not data or data == b"[DONE]":
                    continue
                event = json.loads(data)
                kind = event.get("type")
                event_types.add(kind)
                if kind == "response.output_text.delta":
                    if first_delta is None:
                        first_delta = round(time.monotonic() - started, 3)
                    text += event.get("delta", "")
                if kind == "response.completed":
                    completed = True
                    usage = event.get("response", {}).get("usage")
                assert kind not in ("error", "response.failed"), "Failure event in stream"
            assert completed and "TOKYO_STREAM_OK" in text, "Incomplete stream or test marker missing"
            print(json.dumps({"mode": "stream", "status": "passed", "first_delta_seconds": first_delta,
                              "seconds": round(time.monotonic() - started, 3),
                              "completed": completed, "event_types": sorted(event_types), "usage": usage}))
        finally:
            connection.close()
        return
    original = {"role": "user", "content": "Call bridge_echo with text TOKYO_TOOL_OK, then repeat its result exactly."}
    body = {"model": args.model, "max_output_tokens": 256, "input": [original],
            "tools": [{"type": "function", "name": "bridge_echo", "description": "Return the supplied text unchanged.",
                       "parameters": {"type": "object", "properties": {"text": {"type": "string"}},
                                      "required": ["text"], "additionalProperties": False}}],
            "tool_choice": "auto"}
    first, first_elapsed = call(args, "/v1/responses", body)
    tool_call = next(item for item in first.get("output", []) if item.get("type") == "function_call")
    assert tool_call["name"] == "bridge_echo", "Unexpected tool name"
    arguments = json.loads(tool_call["arguments"])
    assert arguments["text"] == "TOKYO_TOOL_OK", "Unexpected tool argument"
    body["input"] = [original] + first["output"] + [
        {"type": "function_call_output", "call_id": tool_call["call_id"], "output": arguments["text"]}]
    body["tool_choice"] = "none"
    second, second_elapsed = call(args, "/v1/responses", body)
    text = "".join(part.get("text", "") for item in second.get("output", [])
                   for part in item.get("content", []) if part.get("type") == "output_text")
    assert "TOKYO_TOOL_OK" in text, "Tool result was not returned"
    print(json.dumps({"mode": "tools", "status": "passed", "tool": "bridge_echo",
                      "first_seconds": first_elapsed, "result_seconds": second_elapsed,
                      "completed": second.get("status") == "completed",
                      "usage_first": first.get("usage"), "usage_second": second.get("usage")}))


if __name__ == "__main__":
    main()
