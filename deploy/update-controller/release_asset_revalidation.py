"""Release the API manager cache fix without restarting inference or controller."""
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
SOURCE = Path(sys.argv[1]).resolve()
assert str(SOURCE).startswith('/tmp/yeutech-asset-revalidation-')
RELEASE = '20260925-merge-status-cache-v5'
BACKUP = ROOT / 'backups' / RELEASE
assert not BACKUP.exists(), 'Release already exists; inspect before retry'
ASSETS = ('app.js', 'index.html', 'style.css')


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def inference_snapshot():
    names = run(DOCKER, 'ps', '-a', '--format', '{{.Names}}').splitlines()
    names = [name for name in names if name.startswith(
        ('novel-ai-proxy', 'chatgpt-web-driver', 'gemini-web-driver', 'yeutech-web-responses'))]
    return {name: run(DOCKER, 'inspect', name, '--format',
                      '{{.Id}} {{.State.StartedAt}} {{.State.Running}}') for name in names}


before = inference_snapshot()
BACKUP.mkdir(parents=True)
(BACKUP / 'inference-before.json').write_text(json.dumps(before))
compose = ROOT / 'compose.yaml'
old_compose = compose.read_text()
(BACKUP / 'compose.yaml').write_text(old_compose)
for name in ASSETS:
    subprocess.run([DOCKER, 'cp', 'yeutech-api-manager:/app/public/' + name,
                    str(BACKUP / name)], check=True)
subprocess.run([DOCKER, 'cp', 'yeutech-api-manager:/app/server.mjs',
                str(BACKUP / 'server.mjs')], check=True)
work = SOURCE / 'manager'
work.mkdir()
for name in ASSETS + ('server.mjs',):
    shutil.copy2(SOURCE / name, work / name)
base = run(DOCKER, 'inspect', 'yeutech-api-manager', '--format', '{{.Config.Image}}')
image = 'yeutech-api-manager:' + RELEASE
(work / 'Dockerfile').write_text('FROM ' + base + '\n'
                                'COPY app.js index.html style.css /app/public/\n'
                                'COPY server.mjs /app/server.mjs\n'
                                'RUN node --check /app/public/app.js && node --check /app/server.mjs\n')
subprocess.run([DOCKER, 'build', '-t', image, str(work)], check=True)
replacement, count = re.subn(r'(?m)^(\s*image:\s*)' + re.escape(base) + r'\s*$',
                              lambda match: match[1] + image, old_compose)
assert count == 1, 'Compose drift; no manager switched'
compose.write_text(replacement)
try:
    subprocess.run([DOCKER, 'compose', '-f', str(compose), 'up', '-d', '--no-deps', '--no-build', 'manager'], check=True)
    for attempt in range(30):
        try:
            for name in ('app.js', 'style.css'):
                request = urllib.request.Request('http://127.0.0.1:18320/' + name + '?v=20260925-merge-status-v5',
                                                 headers={'Host': 'api.yeutech.cn'})
                with urllib.request.urlopen(request, timeout=3) as response:
                    served = response.read()
                    assert response.headers['Cache-Control'] == 'no-cache'
                assert hashlib.sha256(served).digest() == hashlib.sha256((work / name).read_bytes()).digest()
            break
        except Exception:
            if attempt == 29:
                raise
            time.sleep(1)
    for name in ASSETS + ('server.mjs',):
        path = '/app/public/' + name if name in ASSETS else '/app/server.mjs'
        subprocess.run([DOCKER, 'cp', 'yeutech-api-manager:' + path,
                        str(work / ('verified-' + name))], check=True)
        assert (work / name).read_bytes() == (work / ('verified-' + name)).read_bytes()
except Exception:
    compose.write_text(old_compose)
    subprocess.run([DOCKER, 'compose', '-f', str(compose), 'up', '-d', '--no-deps', '--no-build', 'manager'], check=True)
    raise
for name in ASSETS:
    shutil.copy2(work / name, ROOT / 'app/public' / name)
shutil.copy2(work / 'server.mjs', ROOT / 'app/server.mjs')
assert before == inference_snapshot(), 'Inference containers changed concurrently; inspect immediately'
print(json.dumps({'release': RELEASE, 'inferenceContainersUnchanged': True,
                  'proxyUpdateInvoked': False, 'backup': str(BACKUP)}))
