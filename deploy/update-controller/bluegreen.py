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
from typing import Optional

ROOT = Path('/volume1/docker/novel-ai-proxy')
AUTH = ROOT / 'auth'
GATEWAY_DIR = ROOT / 'gateway'
STATE_FILE = GATEWAY_DIR / 'active.json'
DOCKER = '/var/packages/ContainerManager/target/usr/bin/docker'
GATEWAY_CONTAINER = 'novel-ai-proxy-gateway'
GATEWAY_IMAGE = 'yeutech-proxy-gateway:stable'
SLOTS = {'blue': 18317, 'green': 18318, 'amber': 18315}
LEGACY_CONTAINER = 'novel-ai-proxy'
GATEWAYS = (
    ('novel-ai-proxy-gateway', 18322, 'active.json'),
    ('novel-ai-proxy-gateway-v2', 18313, 'active-v2.json'),
    ('novel-ai-proxy-gateway-v3', 18311, 'active.json'),
    ('novel-ai-proxy-gateway-v4', 18309, 'active-v2.json'),
)


def production_state() -> dict:
    primary = GATEWAY_DIR / 'active-v2.json'
    return json.loads(primary.read_text()) if primary.exists() else read_state()


def gateway_inventory(require_applied: bool = True) -> list[dict]:
    result = []
    for name, admin_port, filename in GATEWAYS:
        if not exists(name):
            continue
        raw = json.loads(run(DOCKER, 'inspect', name, '--format', '{{json .Config.Env}}', timeout=30))
        env = dict(item.split('=', 1) for item in raw if '=' in item)
        if env.get('GATEWAY_STATE_FILE', '/state/active.json') != '/state/' + filename:
            raise RuntimeError('网关指针配置发生变化，未切换任何模型请求')
        allowed = {int(p) for p in env.get('GATEWAY_BACKEND_PORTS', '18317,18318').split(',')}
        with urllib.request.urlopen(f'http://127.0.0.1:{admin_port}/status', timeout=3) as response:
            status = json.load(response)
        if not status.get('ok') or not isinstance(status.get('activeRequests'), dict):
            raise RuntimeError('网关无法确认在途请求，未切换任何模型请求')
        path = GATEWAY_DIR / filename
        state = json.loads(path.read_text())
        if (require_applied and state.get('port') != status.get('activePort')) or SLOTS.get(state.get('slot')) != state.get('port'):
            raise RuntimeError('网关尚未应用版本指针，未开始更新')
        result.append(dict(name=name, admin_port=admin_port, path=path,
                           allowed=allowed, status=status, state=state))
    if not result:
        raise RuntimeError('未发现受管理的推理网关，禁止重启式更新')
    return result


def deployment_plan() -> tuple:
    inventory = gateway_inventory()
    source_state = production_state()
    allowed = set.intersection(*(g['allowed'] for g in inventory))
    for slot, port in SLOTS.items():
        if port in allowed and slot_reusable(slot, inventory):
            return source_state, slot, inventory
    raise RuntimeError('没有空闲发布槽；现有请求和旧实例均保留，请稍后重试')


def normalized_config(slot: str) -> str:
    return re.sub(r'(?m)^port:\s*\d+\s*$', 'port: SLOT',
                  (slot_config(slot) / 'config.yaml').read_text())


def config_sections(slot: str) -> dict[str, str]:
    sections = {}
    current = '(header)'
    for line in normalized_config(slot).splitlines(keepends=True):
        match = re.match(r'^([A-Za-z][A-Za-z0-9-]*):', line)
        if match:
            current = match.group(1)
        sections[current] = sections.get(current, '') + line
    return sections


def additive_web_config_source(inventory: list[dict]) -> Optional[str]:
    """Use the Web-enabled config only for the known additive lane drift."""
    slots = {g['state']['slot'] for g in inventory}
    if len(slots) != 2:
        return None
    candidate = production_state()['slot']
    if candidate not in slots:
        return None
    other = next(iter(slots - {candidate}))
    target, base = config_sections(candidate), config_sections(other)
    if any(base.get(key) != target.get(key) for key in base.keys() | target.keys()
           if key not in {'codex', 'openai-compatibility'}):
        return None
    if base.get('codex') or not re.fullmatch(
            r'codex:\s*\n\s+orphan-delegation-compatibility:\s*(?:true|false)\s*\n?', target.get('codex', '')):
        return None
    before, after = base.get('openai-compatibility', ''), target.get('openai-compatibility', '')
    if not before or not after.startswith(before):
        return None
    extra = after[len(before):]
    names = re.findall(r'(?m)^\s*-\s*name:\s*([^\s#]+)', extra)
    if names != ['chatgpt-web', 'chatgpt-web-gpt-6-pro',
                 'chatgpt-web-gpt-5-6-thinking', 'chatgpt-web-gpt-5-6-pro']:
        return None
    return candidate


def backend_connections(port: int) -> int:
    output = subprocess.run(['/usr/bin/netstat', '-tn'], text=True, capture_output=True, check=True).stdout
    return sum('ESTABLISHED' in line and re.search(r'[:.]' + str(port) + r'\s', line) is not None
               for line in output.splitlines())


def slot_reusable(slot: str, inventory: list[dict]) -> bool:
    port = SLOTS[slot]
    if any(g['status']['activePort'] == port for g in inventory):
        return False
    if any(int(g['status']['activeRequests'].get(str(port), 0)) > 0 and
           g['status'].get('version') == 'lifecycle-v3' for g in inventory):
        return False
    # Pre-v3 gateways can retain completed request counts. Their stale counters
    # are ignored only when the backend has no established TCP connections.
    return backend_connections(port) == 0


def wait_reusable_slot(slot: str, timeout: int = 900) -> None:
    deadline, quiet_since = time.monotonic() + timeout, None
    while time.monotonic() < deadline:
        inventory = gateway_inventory()
        if slot_reusable(slot, inventory):
            quiet_since = quiet_since or time.monotonic()
            if time.monotonic() - quiet_since >= 5:
                return
        else:
            quiet_since = None
        time.sleep(1)
    raise RuntimeError(f'{slot} 发布槽仍有在途请求，已保留旧实例；稍后可重试')


def replace_pointer(path: Path, value: dict) -> None:
    temporary = path.with_suffix('.next')
    temporary.write_text(json.dumps(value, separators=(',', ':')))
    os.chmod(temporary, 0o644)
    temporary.replace(path)


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


def deploy(image: str, *, unify_only: bool = False) -> dict:
    _, first_slot, inventory = deployment_plan()
    unified_source = additive_web_config_source(inventory)
    if unify_only and not unified_source:
        raise RuntimeError('入口配置不符合已核对的增量差异，未执行统一')
    if unify_only and (image != production_state()['image'] or
                       image != next(g['state']['image'] for g in inventory
                                     if g['state']['slot'] == unified_source)):
        raise RuntimeError('统一配置只能复用已在线的完整配置镜像')
    groups = {}
    if unified_source:
        groups['additive-web-config'] = {g['path']: g for g in inventory}
    else:
        for gateway in inventory:
            groups.setdefault(normalized_config(gateway['state']['slot']), {})[gateway['path']] = gateway
    for index, group in enumerate(groups.values()):
        if not unify_only and all(g['state']['image'] == image for g in group.values()) and (
                not unified_source or len({g['state']['slot'] for g in group.values()}) == 1):
            continue
        current = gateway_inventory()
        members = [g for g in current if g['path'] in group]
        allowed = set.intersection(*(g['allowed'] for g in members))
        candidates = [first_slot] if index == 0 else list(SLOTS)
        if index == 0:
            candidates += [slot for slot in SLOTS if slot != first_slot]
        deadline = time.monotonic() + 900
        while True:
            current = gateway_inventory()
            next_slot = next((slot for slot in candidates if SLOTS[slot] in allowed and
                              slot_reusable(slot, current)), None)
            if next_slot:
                break
            if time.monotonic() >= deadline:
                raise RuntimeError('下一组入口没有空闲发布槽；已切换的入口继续运行，未切换的入口保持原样')
            time.sleep(1)
        wait_reusable_slot(next_slot)
        source = unified_source or members[0]['state']['slot']
        prepare_config(slot_config(source), next_slot)
        start_slot(next_slot, image, container_name(source))
        probe(SLOTS[next_slot], slot_config(next_slot))
        pointers = {g['path']: g['state'] for g in members}
        next_state = dict(slot=next_slot, port=SLOTS[next_slot], image=image, updatedAt=int(time.time()))
        try:
            # Switch only gateways sharing this configuration. Other gateway
            # groups and their active streams retain their own config and slot.
            for path in pointers:
                replace_pointer(path, next_state)
            deadline = time.monotonic() + 15
            while True:
                observed = gateway_inventory(require_applied=False)
                if all(g['status']['activePort'] == SLOTS[next_slot]
                       for g in observed if g['path'] in pointers):
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeError('部分网关未确认新版本，恢复本组入口；保留全部在途实例')
                time.sleep(0.2)
        except Exception:
            for path, previous in pointers.items():
                replace_pointer(path, previous)
            # The candidate may already own streams. Never stop it on rollback.
            raise
    return production_state()


if __name__ == '__main__':
    import sys
    action = sys.argv[1] if len(sys.argv) > 1 else 'status'
    result = migrate() if action == 'migrate' else deploy(sys.argv[2]) if action == 'deploy' and len(sys.argv) == 3 else deploy(production_state()['image'], unify_only=True) if action == 'unify' else read_state()
    print(json.dumps({'slot': result['slot'], 'port': result['port'], 'image': result['image']}))
