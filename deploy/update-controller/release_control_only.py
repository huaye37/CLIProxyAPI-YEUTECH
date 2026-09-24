"""Install the control plane only. Never invoke a proxy update or rollback."""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import time
import urllib.request

D = '/var/packages/ContainerManager/target/usr/bin/docker'
ROOT = Path('/volume1/docker/yeutech-api-manager')
SOURCE = Path(sys.argv[1]).resolve()
assert str(SOURCE).startswith('/tmp/yeutech-update-control-')
RELEASE = '20260925-update-control-v1'
BACKUP = ROOT / 'backups' / RELEASE
resume = len(sys.argv) > 2 and sys.argv[2] == '--resume-ui'
assert not BACKUP.exists() or resume, 'Release already exists; inspect before retry'


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def inference_snapshot():
    names = run(D, 'ps', '-a', '--format', '{{.Names}}').splitlines()
    names = [n for n in names if n.startswith(('novel-ai-proxy', 'chatgpt-web-driver', 'gemini-web-driver', 'yeutech-web-responses'))]
    return {n: run(D, 'inspect', n, '--format', '{{.Id}} {{.State.StartedAt}} {{.State.Running}}') for n in names}


before = inference_snapshot()
BACKUP.mkdir(parents=True, exist_ok=resume)
if not resume:
    (BACKUP / 'inference-before.json').write_text(json.dumps(before))
else:
    assert before == json.loads((BACKUP / 'inference-before.json').read_text()), 'Inference changed since initial release'
updater = ROOT / 'updater'
service = Path('/usr/local/etc/rc.d/S99yeutech-proxy-updater.sh')
compose = ROOT / 'compose.yaml'
for path in [updater / 'agent.py', updater / 'bluegreen.py', service, compose]:
    if not resume:
        shutil.copy2(path, BACKUP / path.name)
rows = run('ps', '-eo', 'args').splitlines()
assert resume or '/usr/bin/python3 /volume1/docker/yeutech-api-manager/updater/agent.py' not in rows, 'Stop the old controller before installation'
for name in ['agent.py', 'bluegreen.py']:
    compile((SOURCE / name).read_text(), name, 'exec')
    if resume:
        assert (SOURCE / name).read_bytes() == (updater / name).read_bytes()
    else:
        shutil.copy2(SOURCE / name, updater / name)
if not resume:
    shutil.copy2(SOURCE / 'service.sh', service)
service.chmod(0o755)
subprocess.run([str(service), 'start'], check=True)

# Patch only the four update-related lines in the actual running image, not a
# stale local application snapshot. Build a durable manager-only overlay.
work = SOURCE / 'manager'
work.mkdir(exist_ok=resume)
subprocess.run([D, 'cp', 'yeutech-api-manager:/app/public/app.js', str(work / 'app.js')], check=True)
if not (BACKUP / 'app.js').exists():
    shutil.copy2(work / 'app.js', BACKUP / 'app.js')
lines = (work / 'app.js').read_text().splitlines()
for change in json.loads((SOURCE / 'ui-lines.json').read_text()):
    indexes = [i for i, line in enumerate(lines) if line.startswith(change['prefix'])]
    assert len(indexes) == 1, 'Production frontend drift: ' + change['prefix']
    lines[indexes[0]] = change['line']
(work / 'app.js').write_text('\n'.join(lines) + '\n')
base = run(D, 'inspect', 'yeutech-api-manager', '--format', '{{.Config.Image}}')
image = 'yeutech-api-manager:' + RELEASE
(work / 'Dockerfile').write_text('FROM ' + base + '\nCOPY app.js /app/public/app.js\nRUN node --check /app/public/app.js\n')
subprocess.run([D, 'build', '-t', image, str(work)], check=True)
old_compose = compose.read_text()
replacement, count = re.subn(r'(?m)^(\s*image:\s*)' + re.escape(base) + r'\s*$', lambda m: m[1] + image, old_compose)
assert count == 1, 'Compose drift; no manager switched'
compose.write_text(replacement)
try:
    subprocess.run([D, 'compose', '-f', str(compose), 'up', '-d', '--no-deps', '--no-build', 'manager'], check=True)
    for attempt in range(30):
        try:
            request = urllib.request.Request('http://127.0.0.1:18320/app.js', headers={'Host': 'api.yeutech.cn'})
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
    subprocess.run([D, 'compose', '-f', str(compose), 'up', '-d', '--no-deps', '--no-build', 'manager'], check=True)
    raise
shutil.copy2(work / 'app.js', ROOT / 'app/public/app.js')
after = inference_snapshot()
assert before == after, 'Inference containers changed concurrently; inspect immediately'
print(json.dumps({'release': RELEASE, 'inferenceContainersUnchanged': True,
                  'proxyUpdateInvoked': False, 'backup': str(BACKUP)}))
