#!/usr/bin/env python3
"""Deploy a new gateway without stopping old streams; run as root on the NAS."""
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import time
import urllib.request

D='/var/packages/ContainerManager/target/usr/bin/docker'
source=Path(sys.argv[1]).resolve()
assert source.name == 'yeutech-gateway-v3-20260920'
release=Path('/volume1/docker/novel-ai-proxy/gateway-lifecycle-v3')
updater=Path('/volume1/docker/yeutech-api-manager/updater/bluegreen.py')
nginx=Path('/usr/local/etc/nginx/sites-enabled/yeutech-llm-api.conf')
state=Path('/volume1/docker/novel-ai-proxy/update-state.json')
phase=json.loads(state.read_text()).get('phase','idle') if state.exists() else 'idle'
assert phase in ('idle','complete','failed'), 'Updater is busy; do not interrupt it'
assert hashlib.sha256(updater.read_bytes()).hexdigest() == '033cff5eaca627f3d0bf568953568534ec1cf53c17bb21e6cb0c99c71b5d2ea1', 'Updater drift; inspect before release'
assert not release.exists() and not nginx.exists(), 'Release already exists; inspect before retry'
release.mkdir()
for name in ('proxy-gateway.mjs','bluegreen.py','llm-api.nginx.conf'):
    shutil.copy2(source/name, release/name)
    (release/name).chmod(0o644)
image=subprocess.check_output([D,'inspect','novel-ai-proxy-gateway','--format','{{.Image}}'],text=True).strip()
subprocess.run([D,'run','-d','--name','novel-ai-proxy-gateway-v3','--restart','unless-stopped',
 '--network','host','--user','1026:100','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges:true',
 '--memory','96m','-e','GATEWAY_STATE_FILE=/state/active.json','-e','GATEWAY_PORT=18312','-e','GATEWAY_ADMIN_PORT=18311',
 '-e','GATEWAY_BACKEND_PORTS=18317,18318,18315','-v','/volume1/docker/novel-ai-proxy/gateway:/state:ro',
 '-v',f'{release}/proxy-gateway.mjs:/app/proxy-gateway.mjs:ro',image],check=True)
for attempt in range(30):
    try:
        status=json.load(urllib.request.urlopen('http://127.0.0.1:18311/status',timeout=2))
        assert status.get('version')=='lifecycle-v3'
        print(json.dumps({'gateway':status}),flush=True)
        break
    except Exception:
        if attempt==29:raise
        time.sleep(0.2)
# Restart only the idle deployment controller to load multi-gateway accounting.
shutil.copy2(updater,release/'bluegreen.before.py')
shutil.copyfile(release/'bluegreen.py',updater)
subprocess.run(['/usr/local/etc/rc.d/S99yeutech-proxy-updater.sh','restart'],check=True)
shutil.copyfile(release/'llm-api.nginx.conf',nginx)
validation=subprocess.run(['/usr/bin/nginx','-t'])
if validation.returncode:
    nginx.unlink()
    raise RuntimeError('Nginx validation failed; live configuration unchanged')
subprocess.run(['/usr/bin/nginx','-s','reload'],check=True)
print(json.dumps({'lan_vhost':'installed','old_gateways':'retained','updater_phase_before':phase}))
