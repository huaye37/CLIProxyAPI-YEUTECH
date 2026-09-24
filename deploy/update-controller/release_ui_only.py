"""Publish only the API manager's update-page copy; never touch inference."""
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
assert str(SOURCE).startswith('/tmp/yeutech-update-ui-')
RELEASE = '20260925-update-ui-v3'
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


before = inference_snapshot()
BACKUP.mkdir(parents=True)
(BACKUP / 'inference-before.json').write_text(json.dumps(before))
compose = ROOT / 'compose.yaml'
old_compose = compose.read_text()
(BACKUP / 'compose.yaml').write_text(old_compose)
work = SOURCE / 'manager'
work.mkdir()
subprocess.run([DOCKER, 'cp', 'yeutech-api-manager:/app/public/app.js', str(work / 'app.js')], check=True)
shutil.copy2(work / 'app.js', BACKUP / 'app.js')
lines = (work / 'app.js').read_text().splitlines()
for change in json.loads((SOURCE / 'ui-lines.json').read_text()):
    indexes = [i for i, line in enumerate(lines) if line.startswith(change['prefix'])]
    assert len(indexes) == 1, 'Production frontend drift: ' + change['prefix']
    lines[indexes[0]] = change['line']
(work / 'app.js').write_text('\n'.join(lines) + '\n')
base = run(DOCKER, 'inspect', 'yeutech-api-manager', '--format', '{{.Config.Image}}')
image = 'yeutech-api-manager:' + RELEASE
(work / 'Dockerfile').write_text('FROM ' + base + '\nCOPY app.js /app/public/app.js\nRUN node --check /app/public/app.js\n')
subprocess.run([DOCKER, 'build', '-t', image, str(work)], check=True)
replacement, count = re.subn(r'(?m)^(\s*image:\s*)' + re.escape(base) + r'\s*$',
                              lambda match: match[1] + image, old_compose)
assert count == 1, 'Compose drift; no manager switched'
compose.write_text(replacement)
try:
    subprocess.run([DOCKER, 'compose', '-f', str(compose), 'up', '-d', '--no-deps', '--no-build', 'manager'], check=True)
    for attempt in range(30):
        try:
            request = urllib.request.Request('http://127.0.0.1:18320/app.js',
                                             headers={'Host': 'api.yeutech.cn'})
            with urllib.request.urlopen(request, timeout=3) as response:
                served = response.read()
            assert hashlib.sha256(served).digest() == hashlib.sha256((work / 'app.js').read_bytes()).digest()
            break
        except Exception:
            if attempt == 29:
                raise
            time.sleep(1)
except Exception:
    compose.write_text(old_compose)
    subprocess.run([DOCKER, 'compose', '-f', str(compose), 'up', '-d', '--no-deps', '--no-build', 'manager'], check=True)
    raise
shutil.copy2(work / 'app.js', ROOT / 'app/public/app.js')
assert before == inference_snapshot(), 'Inference containers changed concurrently; inspect immediately'
print(json.dumps({'release': RELEASE, 'inferenceContainersUnchanged': True,
                  'proxyUpdateInvoked': False, 'backup': str(BACKUP)}))
