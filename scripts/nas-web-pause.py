"""Pause only subscription-Web services, retaining credentials and backups."""
import datetime
import json
from pathlib import Path
import shutil
import subprocess
import sys

docker='/var/packages/ContainerManager/target/usr/bin/docker'
files=[Path('/volume1/docker/novel-ai-proxy/config-'+slot+'/config.yaml') for slot in ('blue','green','amber')]
planned=[]
for file in files:
    if not file.exists():
        continue
    lines=file.read_text().splitlines(keepends=True)
    matches=[]
    for index,line in enumerate(lines):
        if line.startswith('  - {'):
            entry=json.loads(line[4:])
            if entry.get('name') in ('chatgpt-web','gemini-web'):
                matches.append(index)
    if not matches and any('chatgpt-web' in line or 'gemini-web' in line for line in lines):
        # Fail closed if the provider format changed; do not guess YAML ranges.
        print('INSPECT_REQUIRED',str(file),flush=True)
        sys.exit(2)
    planned.append((file,lines,matches))
    print('WEB_ROUTE_PLAN',file.parent.name,'remove',len(matches),flush=True)
if '--apply' not in sys.argv:
    sys.exit(0)
stamp=datetime.datetime.now().strftime('%Y%m%d-%H%M%S')
backup=Path('/volume1/docker/web-subscriptions/paused-'+stamp)
backup.mkdir(mode=0o700)
for file,lines,matches in planned:
    if not matches:
        continue
    shutil.copy2(file,backup/(file.parent.name+'.yaml'))
    file.write_text(''.join(line for index,line in enumerate(lines) if index not in matches))
names=['chatgpt-web-driver','gemini-web-driver','yeutech-web-responses-adapter']
metadata=json.loads(subprocess.check_output([docker,'inspect',*names]))
(backup/'restart-policies.json').write_text(json.dumps({item['Name'].lstrip('/'):item['HostConfig']['RestartPolicy'] for item in metadata}))
compose=Path('/volume1/docker/web-subscriptions/chatgpt/compose.yaml')
shutil.copy2(compose,backup/'compose.yaml')
compose.write_text(compose.read_text().replace('    restart: unless-stopped','    restart: "no"'))
subprocess.run([docker,'update','--restart=no',*names],check=True)
subprocess.run([docker,'stop','--time','30',*names],check=True)
print('WEB_PAUSED_BACKUP',str(backup),flush=True)
