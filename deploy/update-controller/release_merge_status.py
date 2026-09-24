"""Release source-merge evidence and the API manager UI without updating inference."""
import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
import time
import urllib.request


DOCKER = '/var/packages/ContainerManager/target/usr/bin/docker'
ROOT = Path('/volume1/docker/yeutech-api-manager')
PROXY_ROOT = Path('/volume1/docker/novel-ai-proxy')
SOURCE = Path(sys.argv[1]).resolve()
assert str(SOURCE).startswith('/tmp/yeutech-merge-status-')
RELEASE = '20260925-merge-status-v4'
BACKUP = ROOT / 'backups' / RELEASE
assert not BACKUP.exists(), 'Release already exists; inspect before retry'


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def inference_snapshot():
    names = run(DOCKER, 'ps', '-a', '--format', '{{.Names}}').splitlines()
    names = [name for name in names if name.startswith(
        ('novel-ai-proxy', 'chatgpt-web-driver', 'gemini-web-driver', 'yeutech-web-responses'))]
    return {name: run(DOCKER, 'inspect', name, '--format',
                      '{{.Id}} {{.State.StartedAt}} {{.State.Running}}') for name in names}


def verify_controller():
    token = (PROXY_ROOT / 'update-agent.key').read_text().strip()
    request = urllib.request.Request('http://127.0.0.1:18321/status',
                                     headers={'Authorization': 'Bearer ' + token})
    for attempt in range(20):
        try:
            with urllib.request.urlopen(request, timeout=3) as response:
                status = json.load(response)
            assert 'branchCommit' in status, 'Controller response lacks branch commit'
            return status
        except Exception:
            if attempt == 19:
                raise
            time.sleep(1)


before = inference_snapshot()
BACKUP.mkdir(parents=True)
(BACKUP / 'inference-before.json').write_text(json.dumps(before))
agent = ROOT / 'updater/agent.py'
service = Path('/usr/local/etc/rc.d/S99yeutech-proxy-updater.sh')
compose = ROOT / 'compose.yaml'
old_compose = compose.read_text()
shutil.copy2(agent, BACKUP / 'agent.py')
(BACKUP / 'compose.yaml').write_text(old_compose)
for name in ('app.js', 'index.html', 'style.css'):
    subprocess.run([DOCKER, 'cp', 'yeutech-api-manager:/app/public/' + name,
                    str(BACKUP / name)], check=True)
compile((SOURCE / 'agent.py').read_text(), 'agent.py', 'exec')
work = SOURCE / 'manager'
work.mkdir()
for name in ('app.js', 'index.html', 'style.css'):
    shutil.copy2(SOURCE / name, work / name)
base = run(DOCKER, 'inspect', 'yeutech-api-manager', '--format', '{{.Config.Image}}')
image = 'yeutech-api-manager:' + RELEASE
(work / 'Dockerfile').write_text('FROM ' + base + '\n'
                                'COPY app.js index.html style.css /app/public/\n'
                                'RUN node --check /app/public/app.js\n')
subprocess.run([DOCKER, 'build', '-t', image, str(work)], check=True)
replacement, count = re.subn(r'(?m)^(\s*image:\s*)' + re.escape(base) + r'\s*$',
                              lambda match: match[1] + image, old_compose)
assert count == 1, 'Compose drift; no manager switched'

controller_changed = False
manager_changed = False
try:
    shutil.copy2(SOURCE / 'agent.py', agent)
    controller_changed = True
    subprocess.run([str(service), 'restart'], check=True)
    status = verify_controller()
    compose.write_text(replacement)
    manager_changed = True
    subprocess.run([DOCKER, 'compose', '-f', str(compose), 'up', '-d', '--no-deps', '--no-build', 'manager'], check=True)
    for attempt in range(30):
        try:
            for name in ('app.js', 'style.css'):
                request = urllib.request.Request('http://127.0.0.1:18320/' + name,
                                                 headers={'Host': 'api.yeutech.cn'})
                with urllib.request.urlopen(request, timeout=3) as response:
                    served = response.read()
                assert hashlib.sha256(served).digest() == hashlib.sha256((work / name).read_bytes()).digest()
            break
        except Exception:
            if attempt == 29:
                raise
            time.sleep(1)
    for name in ('app.js', 'index.html', 'style.css'):
        subprocess.run([DOCKER, 'cp', 'yeutech-api-manager:/app/public/' + name,
                        str(work / ('verified-' + name))], check=True)
        assert (work / name).read_bytes() == (work / ('verified-' + name)).read_bytes()
except Exception:
    if manager_changed:
        compose.write_text(old_compose)
        subprocess.run([DOCKER, 'compose', '-f', str(compose), 'up', '-d', '--no-deps', '--no-build', 'manager'], check=True)
    if controller_changed:
        shutil.copy2(BACKUP / 'agent.py', agent)
        subprocess.run([str(service), 'restart'], check=True)
    raise

for name in ('app.js', 'index.html', 'style.css'):
    shutil.copy2(work / name, ROOT / 'app/public' / name)
assert before == inference_snapshot(), 'Inference containers changed concurrently; inspect immediately'
print(json.dumps({'release': RELEASE, 'sourceMerged': status.get('updateReady'),
                  'branchCommit': status.get('branchCommit'),
                  'inferenceContainersUnchanged': True, 'proxyUpdateInvoked': False,
                  'backup': str(BACKUP)}))
