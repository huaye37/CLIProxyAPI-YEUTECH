#!/usr/bin/env python3
"""Zero-interruption rolling-slot lifecycle for the NAS CLIProxyAPI data plane."""

from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import time
import urllib.request
from pathlib import Path

ROOT = Path('/volume1/docker/novel-ai-proxy')
AUTH = ROOT / 'auth'
GATEWAY_DIR = ROOT / 'gateway'
STATE_FILE = GATEWAY_DIR / 'active.json'
DOCKER = '/var/packages/ContainerManager/target/usr/bin/docker'
GATEWAY_CONTAINER = 'novel-ai-proxy-gateway'
GATEWAY_IMAGE = 'yeutech-proxy-gateway:stable'
SLOTS = {'blue': 18317, 'green': 18318, 'amber': 18315}
LEGACY_CONTAINER = 'novel-ai-proxy'


def run(*args: str, timeout: int = 1200) -> str:
    result = subprocess.run(args, text=True, capture_output=True, timeout=timeout)
    if result.returncode:
        detail = (result.stderr or result.stdout).strip().splitlines()
        raise RuntimeError(detail[-1][:500] if detail else '命令执行失败')
    return result.stdout.strip()


def container_name(slot: str) -> str:
    return f'novel-ai-proxy-{slot}'


def slot_config(slot: str) -> Path:
    return ROOT / f'config-{slot}'


def read_state() -> dict:
    return json.loads(STATE_FILE.read_text())


def write_state(slot: str, image: str) -> None:
    GATEWAY_DIR.mkdir(parents=True, exist_ok=True)
    temporary = STATE_FILE.with_suffix('.next')
    temporary.write_text(json.dumps({'slot': slot, 'port': SLOTS[slot], 'image': image, 'updatedAt': int(time.time())}, separators=(',', ':')))
    os.chmod(temporary, 0o644)
    temporary.replace(STATE_FILE)


def exists(name: str) -> bool:
    return subprocess.run([DOCKER, 'inspect', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0


def image_of(name: str) -> str:
    return run(DOCKER, 'inspect', name, '--format', '{{.Config.Image}}', timeout=30)


def runtime_environment(name: str) -> list[str]:
    raw = run(DOCKER, 'inspect', name, '--format', '{{json .Config.Env}}', timeout=30)
    allowed = ('TZ=', 'HOME=', 'WEB_SUBSCRIPTION_')
    return [value for value in json.loads(raw) if value.startswith(allowed)]


def prepare_config(source: Path, slot: str) -> Path:
    target = slot_config(slot)
    if target.exists():
        shutil.rmtree(target)
    shutil.copytree(source, target, copy_function=shutil.copy2)
    config = target / 'config.yaml'
    value = config.read_text()
    updated, count = re.subn(r'(?m)^port:\s*\d+\s*$', f'port: {SLOTS[slot]}', value, count=1)
    if count != 1:
        raise RuntimeError('代理配置缺少唯一 port 字段')
    temporary = config.with_suffix('.next')
    temporary.write_text(updated)
    shutil.copymode(config, temporary)
    temporary.replace(config)
    owner = source.stat()
    for entry in [target, *target.rglob('*')]:
        os.chown(entry, owner.st_uid, owner.st_gid)
    return target


def stop(name: str) -> None:
    if exists(name):
        subprocess.run([DOCKER, 'stop', '-t', '35', name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=45)


def remove(name: str) -> None:
    if exists(name):
        stop(name)
        run(DOCKER, 'rm', name, timeout=30)


def start_slot(slot: str, image: str, env_source: str) -> None:
    name = container_name(slot)
    remove(name)
    args = [DOCKER, 'run', '-d', '--name', name, '--restart', 'unless-stopped', '--network', 'host', '--user', '1026:100', '--workdir', '/CLIProxyAPI', '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', '--memory', '512m']
    environment = runtime_environment(env_source)
    if not any(value.startswith('WEB_SUBSCRIPTION_GEMINI_URL=') for value in environment):
        environment.append('WEB_SUBSCRIPTION_GEMINI_URL=http://127.0.0.1:18792')
    for value in environment:
        args.extend(['-e', value])
    args.extend(['--tmpfs', '/tmp:rw,noexec,nosuid,size=64m', '-v', f'{slot_config(slot)}:/run/novel', '-v', f'{AUTH}:/auth', image, './CLIProxyAPI', '-config', '/run/novel/config.yaml'])
    run(*args, timeout=60)


def api_key(config_dir: Path) -> str:
    value = (config_dir / 'config.yaml').read_text()
    match = re.search(r'(?ms)^api-keys:\s*\n\s*-\s*["\']?([^"\'\s]+)', value)
    if not match:
        raise RuntimeError('代理配置未提供健康检查 API key')
    return match.group(1)


def probe(port: int, config_dir: Path, timeout: int = 90) -> None:
    deadline = time.time() + timeout
    key = api_key(config_dir)
    last = ''
    while time.time() < deadline:
        try:
            request = urllib.request.Request(f'http://127.0.0.1:{port}/v1/model-capabilities', headers={'Authorization': f'Bearer {key}'})
            with urllib.request.urlopen(request, timeout=5) as response:
                payload = json.load(response)
            if response.status == 200 and isinstance(payload.get('data'), list) and payload['data']:
                return
        except Exception as error:
            last = str(error)
        time.sleep(2)
    raise RuntimeError(f'代理实例未通过能力目录检查：{last[:200]}')


def legacy_connections() -> int:
    output = subprocess.run(['/usr/bin/netstat', '-tn'], text=True, capture_output=True).stdout
    return sum('ESTABLISHED' in line and ('127.0.0.1:18319' in line or '::1:18319' in line) for line in output.splitlines())


def wait_legacy_idle(timeout: int = 900) -> None:
    deadline, quiet_since = time.time() + timeout, None
    while time.time() < deadline:
        if legacy_connections() == 0:
            quiet_since = quiet_since or time.time()
            if time.time() - quiet_since >= 5:
                return
        else:
            quiet_since = None
        time.sleep(1)
    raise RuntimeError('等待旧代理请求结束超时，未执行入口迁移')


def gateway_status() -> dict:
    with urllib.request.urlopen('http://127.0.0.1:18322/status', timeout=3) as response:
        status = json.load(response)
    status['activePorts'] = [status['activePort']]
    for name, port in [('novel-ai-proxy-gateway-v2', 18313), ('novel-ai-proxy-gateway-v3', 18311), ('novel-ai-proxy-gateway-v4', 18309)]:
        if not exists(name):
            continue
        # Fail closed if a registered gateway cannot report its in-flight work.
        with urllib.request.urlopen(f'http://127.0.0.1:{port}/status', timeout=3) as response:
            extra = json.load(response)
        if not extra.get('ok'):
            raise RuntimeError('Additional inference gateway is unavailable; refusing slot replacement')
        status['activePorts'].append(extra['activePort'])
        for backend, count in extra.get('activeRequests', {}).items():
            status['activeRequests'][backend] = status['activeRequests'].get(backend, 0) + int(count)
    return status


def wait_slot_idle(slot: str, timeout: int = 900) -> None:
    deadline = time.time() + timeout
    while time.time() < deadline:
        status = gateway_status()
        if int(status.get('activeRequests', {}).get(str(SLOTS[slot]), 0)) == 0:
            return
        time.sleep(1)
    raise RuntimeError('旧代理仍有在途请求，已保留实例继续运行')


def slot_active_requests(slot: str) -> int:
    status = gateway_status()
    return int(status.get('activeRequests', {}).get(str(SLOTS[slot]), 0))


def gateway_allowed_ports() -> set[int]:
    raw = run(DOCKER, 'inspect', GATEWAY_CONTAINER, '--format', '{{json .Config.Env}}', timeout=30)
    environment = json.loads(raw)
    configured = next((value.split('=', 1)[1] for value in environment if value.startswith('GATEWAY_BACKEND_PORTS=')), '18317,18318')
    return {int(value) for value in configured.split(',') if value.strip()}


def select_deploy_slot(active_slot: str) -> str:
    allowed_ports = gateway_allowed_ports()
    status = gateway_status()
    for slot in SLOTS:
        if (slot != active_slot and SLOTS[slot] in allowed_ports
                and SLOTS[slot] not in status['activePorts']
                and int(status['activeRequests'].get(str(SLOTS[slot]), 0)) == 0):
            return slot
    raise RuntimeError('当前网关允许的所有非活动代理槽仍有在途请求，未执行可能中断会话的发布')


def start_gateway() -> None:
    remove(GATEWAY_CONTAINER)
    backend_ports = ','.join(str(port) for port in SLOTS.values())
    run(DOCKER, 'run', '-d', '--name', GATEWAY_CONTAINER, '--restart', 'unless-stopped', '--network', 'host', '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', '--memory', '96m', '-e', 'GATEWAY_STATE_FILE=/state/active.json', '-e', f'GATEWAY_BACKEND_PORTS={backend_ports}', '-v', f'{GATEWAY_DIR}:/state:ro', GATEWAY_IMAGE, timeout=60)
    deadline = time.time() + 30
    while time.time() < deadline:
        try:
            if gateway_status().get('ok'):
                return
        except Exception:
            pass
        time.sleep(1)
    raise RuntimeError('稳定代理入口未启动')


def migrate() -> dict:
    if STATE_FILE.exists() and exists(GATEWAY_CONTAINER):
        return read_state()
    if not exists(LEGACY_CONTAINER):
        raise RuntimeError('未找到待迁移的旧代理容器')
    old_image = image_of(LEGACY_CONTAINER)
    prepare_config(ROOT / 'config', 'blue')
    start_slot('blue', old_image, LEGACY_CONTAINER)
    probe(SLOTS['blue'], slot_config('blue'))
    legacy_backup = f'{LEGACY_CONTAINER}-legacy-{time.strftime("%Y%m%d-%H%M%S")}'
    # The one-time migration cannot distinguish a day-long idle SSH keep-alive
    # from a live request by socket state alone. CLIProxyAPI handles SIGTERM with
    # a bounded graceful HTTP shutdown, so active responses get their drain window.
    stop(LEGACY_CONTAINER)
    run(DOCKER, 'rename', LEGACY_CONTAINER, legacy_backup, timeout=30)
    try:
        write_state('blue', old_image)
        start_gateway()
        probe(18319, slot_config('blue'), timeout=30)
    except Exception:
        remove(GATEWAY_CONTAINER)
        stop(container_name('blue'))
        run(DOCKER, 'rename', legacy_backup, LEGACY_CONTAINER, timeout=30)
        run(DOCKER, 'start', LEGACY_CONTAINER, timeout=30)
        raise
    return read_state()


def deploy(image: str) -> dict:
    state = migrate()
    old_slot = state['slot']
    new_slot = select_deploy_slot(old_slot)
    old_container = container_name(old_slot)
    prepare_config(slot_config(old_slot), new_slot)
    start_slot(new_slot, image, old_container)
    probe(SLOTS[new_slot], slot_config(new_slot))
    try:
        write_state(new_slot, image)
        probe(18319, slot_config(new_slot), timeout=30)
    except Exception:
        write_state(old_slot, state['image'])
        stop(container_name(new_slot))
        raise
    if SLOTS[old_slot] not in gateway_status()['activePorts'] and slot_active_requests(old_slot) == 0:
        stop(old_container)
    return read_state()


if __name__ == '__main__':
    import sys
    action = sys.argv[1] if len(sys.argv) > 1 else 'status'
    result = migrate() if action == 'migrate' else deploy(sys.argv[2]) if action == 'deploy' and len(sys.argv) == 3 else read_state()
    print(json.dumps({'slot': result['slot'], 'port': result['port'], 'image': result['image']}))
