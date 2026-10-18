#!/usr/bin/env python3
"""Hot-reload only the approved edge request limit, with identity and idle gates."""
import argparse
import fcntl
import hashlib
import http.client
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import time

import yaml

ROOT = Path("/opt/yeutech-claude-edge")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def atomic(file, data, mode):
    fd, name = tempfile.mkstemp(prefix=".body-limit-", dir=file.parent)
    try:
        os.fchmod(fd, mode)
        with os.fdopen(fd, "wb") as output:
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
        os.replace(name, file)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def health():
    c = http.client.HTTPConnection("127.0.0.1", 18340, timeout=5)
    try:
        c.request("GET", "/healthz")
        return json.loads(c.getresponse().read())
    finally:
        c.close()


def rewrite_config(file, data):
    # The reviewed runtime watches this inode. Keep it intact while the gate is
    # closed, and retain the durable backup for crash/manual recovery.
    fd = os.open(file, os.O_RDWR | os.O_NOFOLLOW)
    try:
        if os.write(fd, data) != len(data):
            raise RuntimeError("Incomplete config write")
        os.ftruncate(fd, len(data))
        os.fsync(fd)
    finally:
        os.close(fd)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--expected-config-sha256", required=True)
    parser.add_argument("--expected-image", required=True)
    args = parser.parse_args()
    handles = [(ROOT / "control" / name).open("a") for name in ("operation.lock", "prepare.lock")]
    for handle in handles:
        fcntl.flock(handle, fcntl.LOCK_EX | fcntl.LOCK_NB)
    file, gate = ROOT / "config/config.yaml", ROOT / "control/gate.json"
    original, original_gate = file.read_bytes(), gate.read_bytes()
    if digest(original) != args.expected_config_sha256:
        raise SystemExit("Config identity changed; refusing overwrite")
    inspect = lambda: json.loads(subprocess.check_output(["docker", "inspect", "yeutech-claude-edge"]))[0]
    before = inspect()
    if before["Image"] != args.expected_image or before["State"]["OOMKilled"]:
        raise SystemExit("Runtime image/state changed")
    state = json.loads((ROOT / "control/state.json").read_bytes())
    if (state.get("job") or {}).get("phase") in {"queued", "draining", "installing", "verifying", "rolling_back"}:
        raise SystemExit("Node operation in progress")
    gate_value = json.loads(original_gate)
    if gate_value.get("enabled") is not True or gate_value.get("maintenance") is not False:
        raise SystemExit("Existing node gate is not accepting; preserving it")
    snapshot = health()
    if snapshot["edgeControl"]["activeRequests"] or snapshot["memoryGuard"]["activeRequests"]:
        raise SystemExit("Active requests; retry in an idle window")
    config = yaml.safe_load(original)
    if config["server"]["max-request-body-bytes"] != 67108864:
        raise SystemExit("Expected the reviewed 64 MiB limit")
    updated, count = re.subn(rb"(?m)^(\s+max-request-body-bytes:\s*)67108864([ \t]*\r?$)", rb"\g<1>134217728\2", original)
    if count != 1:
        raise SystemExit("Ambiguous body limit field")
    expected = yaml.safe_load(updated)
    config["server"]["max-request-body-bytes"] = 134217728
    if expected != config:
        raise SystemExit("Unexpected semantic config change")
    key = expected["access"]["api-keys"][0]
    if not isinstance(key, str) or not key:
        raise SystemExit("Unsupported health-key layout")
    backup = ROOT / "backups" / time.strftime("body-limit-128m-%Y%m%dT%H%M%SZ", time.gmtime())
    backup.mkdir(mode=0o700)
    atomic(backup / "config.yaml", original, 0o600)
    atomic(backup / "gate.json", original_gate, 0o600)
    changed = False
    closed_gate = json.dumps({**gate_value, "maintenance": True}).encode()
    atomic(gate, closed_gate, 0o600)
    try:
        for _ in range(30):
            if health()["edgeControl"]["activeRequests"] == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("Requests did not drain; no configuration changed")
        if file.read_bytes() != original:
            raise RuntimeError("Config changed while draining")
        changed = True
        rewrite_config(file, updated)
        time.sleep(1)
        atomic(gate, original_gate, 0o600)

        def truncated_probe():
            # A deliberately truncated invalid body cannot dispatch inference.
            # The old limit rejects its declared size with 413; the new limit
            # accepts admission and returns 400 for the invalid/truncated JSON.
            c = http.client.HTTPConnection("127.0.0.1", 18340, timeout=5)
            try:
                c.putrequest("POST", "/v1/chat/completions")
                c.putheader("Authorization", "Bearer " + key)
                c.putheader("Content-Type", "application/json")
                c.putheader("Content-Length", "97919776")
                c.endheaders()
                c.send(b"!")
                c.sock.shutdown(socket.SHUT_WR)
                response = c.getresponse()
                response.read()
                return response.status
            finally:
                c.close()

        for _ in range(10):
            status = truncated_probe()
            if status == 400:
                break
            if status != 413:
                raise RuntimeError("Unexpected probe status: " + str(status))
            time.sleep(0.5)
        else:
            raise RuntimeError("Body limit did not hot reload")
        after = inspect()
        if after["State"]["StartedAt"] != before["State"]["StartedAt"] or after["Image"] != before["Image"]:
            raise RuntimeError("Unexpected runtime identity change")
        receipt = {"limitBytes": 134217728, "previousLimitBytes": 67108864,
                   "configSha256": digest(updated), "previousConfigSha256": digest(original),
                   "imageId": after["Image"], "startedAt": after["State"]["StartedAt"],
                   "restartCount": after["RestartCount"], "probeStatus": status,
                   "paidCalls": 0, "backup": str(backup), "gateRestored": gate.read_bytes() == original_gate}
        atomic(backup / "receipt.json", json.dumps(receipt, indent=2).encode(), 0o600)
        print(json.dumps(receipt))
    except BaseException:
        if changed:
            rewrite_config(file, original)
        raise
    finally:
        if gate.read_bytes() == closed_gate:
            atomic(gate, original_gate, 0o600)


if __name__ == "__main__":
    main()
