"""Remove unsupported thinking levels from the old live proxy's Web route."""
import json
from pathlib import Path
import shutil
import sys

sys.path.insert(0, '/volume1/docker/yeutech-api-manager/updater')
import bluegreen as bg

file = bg.slot_config(bg.read_state()['slot']) / 'config.yaml'
lines = file.read_text().splitlines(keepends=True)
count = 0
for index, line in enumerate(lines):
    if not line.startswith('  - {'):
        continue
    provider = json.loads(line[4:])
    if provider.get('name') != 'chatgpt-web':
        continue
    for model in provider['models']:
        model['thinking'] = {'levels': []}
    lines[index] = '  - ' + json.dumps(provider, ensure_ascii=False) + '\n'
    count += 1
assert count == 1
backup = Path('/volume1/docker/web-subscriptions/responses-adapter/proxy-config-before-thinking-fix.yaml')
if not backup.exists():
    shutil.copy2(file, backup)
file.write_text(''.join(lines))
print('WEB_THINKING_METADATA_FIXED_WITH_HOT_RELOAD')
