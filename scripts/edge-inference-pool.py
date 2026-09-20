#!/usr/bin/env python3
"""Add isolated inference transports on hk-edge; never restart existing services."""
import argparse
import concurrent.futures
import fcntl
import json
from pathlib import Path
import subprocess
import shutil
import time
import urllib.error
import urllib.request

ROOT = Path('/opt/yeutech-api-manager/inference-pool')
CADDY = Path('/opt/yeutech-portal/Caddyfile')
PORTS = range(18331, 18335)
BACKEND_PORT = 18319
NAME_PREFIX = 'yeutech-inference-tunnel'
PROJECT = 'yeutech-inference-pool'
ROUTES_OLD = '@inference path /v1/models /v1/model-capabilities /v1/responses /v1/responses/compact /v1/alpha/search'
ROUTES_NEW = ROUTES_OLD + ' /v1/chat/completions /v1/messages /v1/messages/count_tokens'
OLD = '\t\treverse_proxy host.docker.internal:18319 {\n\t\t\tflush_interval -1\n\t\t}'
NEW = '''\t\treverse_proxy host.docker.internal:18331 host.docker.internal:18332 host.docker.internal:18333 host.docker.internal:18334 {
\t\t\t# Each backend owns a separate SSH TCP connection to the same NAS gateway.
\t\t\tlb_policy least_conn
\t\t\t# Never replay a model POST after a transport failure.
\t\t\tlb_retries 0
\t\t\thealth_uri /v1/models
\t\t\thealth_status 401
\t\t\thealth_interval 10s
\t\t\thealth_timeout 3s
\t\t\thealth_fails 2
\t\t\thealth_passes 2
\t\t\tflush_interval -1
\t\t}'''

def run(*args):
    return subprocess.check_output(args, text=True)

def probe(port):
    start = time.monotonic()
    try:
        with urllib.request.urlopen(f'http://172.18.0.1:{port}/v1/models', timeout=5) as response:
            status = response.status
    except urllib.error.HTTPError as error:
        status = error.code
    return {'port': port, 'status': status, 'seconds': round(time.monotonic()-start, 3)}

def patch_config(source, old, new):
    marker = 'llm-api.yeutech.cn {'
    assert source.count(marker) == 1, 'Expected one inference site'
    before, site = source.split(marker, 1)
    assert site.count(old) == 1, 'Inference block has drifted; inspect before deployment'
    return before + marker + site.replace(old, new, 1)

def main():
    global ROOT, PORTS, BACKEND_PORT, NAME_PREFIX, PROJECT, OLD, NEW
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--rollback', action='store_true')
    parser.add_argument('--gateway-v3', action='store_true', help='Migrate to lifecycle-aware gateway with fresh tunnels')
    args = parser.parse_args()
    if args.gateway_v3:
        ROOT = Path('/opt/yeutech-api-manager/inference-pool-v3')
        PORTS = range(18341, 18345)
        BACKEND_PORT = 18312
        NAME_PREFIX = 'yeutech-inference-v3-tunnel'
        PROJECT = 'yeutech-inference-pool-v3'
        OLD = NEW
        for previous, current in zip(range(18331, 18335), PORTS):
            NEW = NEW.replace(str(previous), str(current))
        NEW = NEW.replace('\t\t\tflush_interval -1', '\t\t\t# SSE flushes immediately by default; preserve client cancellation.')
    current = CADDY.read_text()
    desired = patch_config(current, NEW, OLD) if args.rollback else (
        current if NEW in current else patch_config(current, OLD, NEW))
    if args.gateway_v3:
        previous, target = (ROUTES_NEW, ROUTES_OLD) if args.rollback else (ROUTES_OLD, ROUTES_NEW)
        if target not in [line.strip() for line in desired.splitlines()]:
            assert desired.count(previous) == 1, 'Inference routes drift'
            desired = desired.replace(previous, target, 1)
    if not args.apply:
        print(json.dumps({'action': 'rollback' if args.rollback else 'deploy',
                          'ports': list(PORTS), 'changed': desired != current,
                          'existing_services_restarted': False}))
        return
    ROOT.mkdir(exist_ok=True)
    with (ROOT / 'deploy.lock').open('w') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        assert CADDY.read_text() == current, 'Configuration changed during preflight'
        if not args.rollback:
            image = run('docker', 'inspect', 'yeutech-api-edge-tunnel', '--format', '{{.Image}}').strip()
            services = {}
            for number, port in enumerate(PORTS, 1):
                command = ['-N', '-T', '-i', '/keys/id_ed25519']
                for option in ['IdentitiesOnly=yes', 'BatchMode=yes', 'StrictHostKeyChecking=yes',
                               'UserKnownHostsFile=/keys/known_hosts', 'ExitOnForwardFailure=yes',
                               'ServerAliveInterval=30', 'ServerAliveCountMax=3', 'ConnectTimeout=10',
                               'ControlMaster=no', 'ControlPath=none']:
                    command += ['-o', option]
                command += ['-L', f'172.18.0.1:{port}:127.0.0.1:{BACKEND_PORT}', '-p', '5522',
                            'fangjialiang@home.yeutech.cn']
                services[f'inference-{number}'] = {
                    'image': image, 'container_name': f'{NAME_PREFIX}-{number}',
                    'network_mode': 'host', 'restart': 'unless-stopped', 'read_only': True,
                    'dns': ['1.1.1.1', '8.8.8.8'], 'cap_drop': ['ALL'],
                    'security_opt': ['no-new-privileges:true'],
                    'volumes': ['/opt/yeutech-api-manager/ssh:/keys:ro'], 'command': command}
            compose = ROOT / 'compose.json'
            serialized = json.dumps({'services': services}, indent=2) + '\n'
            if compose.exists():
                assert compose.read_text() == serialized, 'Pool config drift; do not recreate active tunnels'
            else:
                compose.write_text(serialized)
            compose_cli = ['docker-compose'] if shutil.which('docker-compose') else ['docker', 'compose']
            subprocess.run(compose_cli + ['-p', PROJECT, '-f', str(compose),
                            'up', '-d', '--no-recreate'], check=True)
            for attempt in range(6):
                try:
                    with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
                        results = list(pool.map(probe, PORTS))
                    assert all(r['status'] == 401 for r in results), results
                    print(json.dumps({'transport_preflight': results}), flush=True)
                    break
                except (OSError, AssertionError):
                    if attempt == 5:
                        raise
                    time.sleep(1)
        if current == desired:
            print('Pool already installed; no reload needed')
            return
        candidate = ROOT / 'Caddyfile.candidate'
        candidate.write_text(desired)
        run('docker', 'cp', str(candidate), 'yeutech-edge:/tmp/inference-pool.Caddyfile')
        run('docker', 'exec', 'yeutech-edge', 'caddy', 'validate', '--config',
            '/tmp/inference-pool.Caddyfile', '--adapter', 'caddyfile')
        assert CADDY.read_text() == current, 'Configuration changed during preflight'
        backup = ROOT / f'Caddyfile.before-{time.time_ns()}'
        backup.write_text(current)
        # Preserve the bind-mounted inode; atomically reloading Caddy keeps HTTP streams alive.
        CADDY.write_text(desired)
        try:
            run('docker', 'exec', 'yeutech-edge', 'caddy', 'reload', '--config',
                '/etc/caddy/Caddyfile', '--adapter', 'caddyfile')
        except subprocess.CalledProcessError:
            CADDY.write_text(current)
            run('docker', 'exec', 'yeutech-edge', 'caddy', 'reload', '--config',
                '/etc/caddy/Caddyfile', '--adapter', 'caddyfile')
            raise
        print(json.dumps({'reloaded': True, 'backup': str(backup),
                          'old_transports_retained': True}), flush=True)

if __name__ == '__main__':
    main()
