#!/usr/bin/env python3
"""Measure the private NAS-to-Tokyo transport without invoking a model."""

import argparse
import base64
import http.client
import json
import random
import time
import zlib
from pathlib import Path


def varied_fixture(size):
    rng = random.Random(20261015)
    chunks = []
    total = 0
    while total < size:
        unique = base64.b64encode(rng.getrandbits(8192).to_bytes(1024, "little"))
        chunk = (b'{"role":"tool","name":"read_file","content":"'
                 + b"function example() { return synthetic_transport_probe; }\n" * 80
                 + unique + b'"}\n')
        chunks.append(chunk)
        total += len(chunk)
    # Intentionally invalid JSON: the handler reads the body, returns 400,
    # and cannot dispatch an inference request.
    return (b"[" + b"".join(chunks))[:size - 1] + b"!"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--port", type=int, choices=(18340, 18341), default=18340)
    parser.add_argument("--size-mib", type=int, choices=(1, 8), default=8)
    args = parser.parse_args()
    key = Path("/volume1/docker/yeutech-claude-edge-tunnel/bridge.key").read_text().strip()
    payload = varied_fixture(args.size_mib * 1024 * 1024)
    connection = http.client.HTTPConnection("127.0.0.1", args.port, timeout=45)
    started = time.monotonic()
    try:
        connection.request("POST", "/v1/chat/completions", body=payload,
                           headers={"Authorization": "Bearer " + key,
                                    "Content-Type": "application/json"})
        sent = time.monotonic()
        response = connection.getresponse()
        response.read()
        elapsed = time.monotonic() - started
        print(json.dumps({"probe": "private-tokyo-transport",
                          "bytes": len(payload),
                          "zlibBytes": len(zlib.compress(payload)),
                          "httpStatus": response.status,
                          "expectedRejection": response.status == 400,
                          "sendSeconds": round(sent - started, 3),
                          "totalSeconds": round(elapsed, 3)}))
        if response.status != 400:
            raise SystemExit("Unexpected response; do not treat this as a throughput result")
    finally:
        connection.close()


if __name__ == "__main__":
    main()
