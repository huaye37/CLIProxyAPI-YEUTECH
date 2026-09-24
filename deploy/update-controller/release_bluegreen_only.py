"""Publish the controller's rolling-slot logic without touching inference."""
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import time
import urllib.request

DOCKER = '/var/packages/ContainerManager/target/usr/bin/docker'
ROOT = Path('/volume1/docker/yeutech-api-manager')
SOURCE = Path(sys.argv[1]).resolve()
assert str(SOURCE).startswith('/tmp/yeutech-bluegreen-release-')
RELEASE = '20260925-config-unification-v9'
BACKUP = ROOT / 'backups' / RELEASE
TARGET = ROOT / 'updater/bluegreen.py'
SERVICE = Path('/usr/local/etc/rc.d/S99yeutech-proxy-updater.sh')
assert not BACKUP.exists(), 'Release already exists; inspect before retry'


def run(*args):
    return subprocess.check_output(args, text=True).strip()


def inference_snapshot():
    names = run(DOCKER, 'ps', '-a', '--format', '{{.Names}}').splitlines()
    names = [name for name in names if name.startswith(
        ('novel-ai-proxy', 'chatgpt-web-driver', 'gemini-web-driver', 'yeutech-web-responses'))]
    return {name: run(DOCKER, 'inspect', name, '--format',
                      '{{.Id}} {{.State.StartedAt}} {{.State.Running}}') for name in names}


def status():
    token = Path('/volume1/docker/novel-ai-proxy/update-agent.key').read_text().strip()
    request = urllib.request.Request('http://127.0.0.1:18321/status',
                                     headers={'Authorization': 'Bearer ' + token})
    with urllib.request.urlopen(request, timeout=5) as response:
        return json.load(response)


before_status = status()
assert before_status.get('phase') not in ('starting', 'downloading', 'waiting', 'installing', 'rolling_back'), \
    'An update job is running; controller not replaced'
before = inference_snapshot()
compile((SOURCE / 'bluegreen.py').read_text(), 'bluegreen.py', 'exec')
BACKUP.mkdir(parents=True)
shutil.copy2(TARGET, BACKUP / 'bluegreen.py')
shutil.copy2(SOURCE / 'bluegreen.py', TARGET)
try:
    subprocess.run([str(SERVICE), 'restart'], check=True)
    for attempt in range(15):
        try:
            status()
            break
        except Exception:
            if attempt == 14:
                raise
            time.sleep(1)
except Exception:
    shutil.copy2(BACKUP / 'bluegreen.py', TARGET)
    subprocess.run([str(SERVICE), 'restart'], check=True)
    raise
assert hashlib.sha256(TARGET.read_bytes()).digest() == hashlib.sha256((SOURCE / 'bluegreen.py').read_bytes()).digest()
assert before == inference_snapshot(), 'Inference container identity or start time changed; inspect immediately'
print(json.dumps({'release': RELEASE, 'inferenceContainersUnchanged': True,
                  'proxyUpdateInvoked': False, 'backup': str(BACKUP)}))
