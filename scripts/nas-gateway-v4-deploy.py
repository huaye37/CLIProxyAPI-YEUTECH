#!/usr/bin/env python3
"""Start a lifecycle-aware gateway on the already running repaired proxy lane."""
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import time
import urllib.request

D='/var/packages/ContainerManager/target/usr/bin/docker'
root=Path('/volume1/docker/novel-ai-proxy')
release=root/'gateway-lifecycle-v4'
state=json.loads((root/'gateway/active-v2.json').read_text())
assert state['port']==18315 and state['image']=='cli-proxy-api:yeutech-d9ca2f5d6b63', 'Repaired lane changed; inspect before release'
updater=Path('/volume1/docker/yeutech-api-manager/updater/bluegreen.py')
update_state=json.loads((root/'update-state.json').read_text())
assert update_state.get('phase') in ('idle','complete','failed'), 'Update in progress'
assert not release.exists(), 'Release already exists; inspect before retry'
release.mkdir()
old_gateway=root/'gateway-lifecycle-v3/proxy-gateway.mjs'
shutil.copy2(old_gateway,release/'proxy-gateway.mjs')
image=subprocess.check_output([D,'inspect','novel-ai-proxy-gateway-v3','--format','{{.Image}}'],text=True).strip()
subprocess.run([D,'run','-d','--name','novel-ai-proxy-gateway-v4','--restart','unless-stopped',
 '--network','host','--user','1026:100','--read-only','--cap-drop','ALL','--security-opt','no-new-privileges:true',
 '--memory','96m','-e','GATEWAY_STATE_FILE=/state/active-v2.json','-e','GATEWAY_PORT=18310','-e','GATEWAY_ADMIN_PORT=18309',
 '-e','GATEWAY_BACKEND_PORTS=18317,18318,18315','-v',f'{root}/gateway:/state:ro',
 '-v',f'{release}/proxy-gateway.mjs:/app/proxy-gateway.mjs:ro',image],check=True)
for attempt in range(30):
 try:
  status=json.load(urllib.request.urlopen('http://127.0.0.1:18309/status',timeout=2))
  assert status['activePort']==18315 and status['version']=='lifecycle-v3'
  print(json.dumps({'gateway':status}),flush=True)
  break
 except Exception:
  if attempt==29:raise
  time.sleep(0.2)
# Retain every old gateway in deployment-drain accounting.
old="('novel-ai-proxy-gateway-v3', 18311)"
source=updater.read_text()
assert source.count(old)==1 and 'gateway-v4' not in source
shutil.copy2(updater,release/'bluegreen.before.py')
updater.write_text(source.replace(old,old+", ('novel-ai-proxy-gateway-v4', 18309)",1))
subprocess.run(['/usr/local/etc/rc.d/S99yeutech-proxy-updater.sh','restart'],check=True)
# Extend only the existing Hong Kong host/user allowlist before opening new tunnels.
sshd=Path('/etc/ssh/sshd_config')
source=sshd.read_text()
old='    PermitOpen 127.0.0.1:14318 127.0.0.1:18320 127.0.0.1:18319 127.0.0.1:18312'
assert source.count(old)==1
shutil.copy2(sshd,release/'sshd_config.before')
sshd.write_text(source.replace(old,old+' 127.0.0.1:18310',1))
try:subprocess.run(['/usr/bin/sshd','-t'],check=True)
except Exception:
 shutil.copyfile(release/'sshd_config.before',sshd)
 raise
os.kill(int(Path('/var/run/sshd.pid').read_text()),signal.SIGHUP)
print('New gateway ready; neither inference entry nor old requests changed yet')
