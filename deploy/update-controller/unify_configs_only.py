"""Unify the two verified additive proxy configurations on the current image."""
import json
from pathlib import Path
import shutil
import subprocess
import sys

sys.path.insert(0, '/volume1/docker/yeutech-api-manager/updater')
import bluegreen


ROOT = Path('/volume1/docker/novel-ai-proxy')
BACKUP = ROOT / 'update-backups/20260925-config-unification-v9'
assert not BACKUP.exists(), 'Config-unification backup already exists; inspect before retry'


def container_identity(slot):
    return subprocess.check_output([
        bluegreen.DOCKER, 'inspect', bluegreen.container_name(slot),
        '--format', '{{.Id}} {{.State.StartedAt}} {{.State.Running}}',
    ], text=True).strip()


inventory = bluegreen.gateway_inventory()
assert {row['state']['slot'] for row in inventory} == {'blue', 'amber'}, 'Unexpected active slots'
assert bluegreen.additive_web_config_source(inventory) == 'amber', 'Config difference is not the verified additive Web configuration'
assert bluegreen.deployment_plan()[1] == 'green', 'No safe green staging slot'
source_image = bluegreen.production_state()['image']
assert next(row['state']['image'] for row in inventory if row['state']['slot'] == 'amber') == source_image
prior_images = {row['state']['slot']: row['state']['image'] for row in inventory}
old_identity = {slot: container_identity(slot) for slot in ('blue', 'amber')}
BACKUP.mkdir(parents=True, mode=0o700)
for filename in ('active.json', 'active-v2.json'):
    shutil.copy2(ROOT / 'gateway' / filename, BACKUP / filename)
result = bluegreen.deploy(source_image, unify_only=True)
current = bluegreen.gateway_inventory()
assert {row['state']['slot'] for row in current} == {'green'}, 'Gateways did not converge on one slot'
assert {row['state']['image'] for row in current} == {source_image}, 'Gateways did not converge on the active image'
assert bluegreen.normalized_config('green') == bluegreen.normalized_config('amber'), 'Unified configuration differs from source'
assert old_identity == {slot: container_identity(slot) for slot in ('blue', 'amber')}, 'Existing inference container changed'
print(json.dumps({'unified': True, 'activeSlot': result['slot'],
                  'oldContainersUnchanged': True, 'sourceImageReused': source_image,
                  'priorImages': prior_images,
                  'backup': str(BACKUP)}))
