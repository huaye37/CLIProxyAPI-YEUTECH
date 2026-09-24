import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import agent
import bluegreen as bg


class ControllerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.inventory = []
        for slot in bg.SLOTS:
            directory = self.root / ('config-' + slot)
            directory.mkdir()
            (directory / 'config.yaml').write_text('port: %d\nroute: retained\n' % bg.SLOTS[slot])
        for name, slot in [('active.json', 'blue'), ('active-v2.json', 'amber')]:
            state = dict(slot=slot, port=bg.SLOTS[slot], image='old')
            self.inventory.append(dict(path=self.root / name, state=state,
                allowed=set(bg.SLOTS.values()), status=dict(activePort=bg.SLOTS[slot], activeRequests={})))
        self.source = self.inventory[1]['state']

    def plan(self):
        with patch.object(bg, 'gateway_inventory', return_value=self.inventory), \
             patch.object(bg, 'production_state', return_value=self.source), \
             patch.object(bg, 'ROOT', self.root):
            return bg.deployment_plan()

    def test_plan_uses_only_idle_slot_and_primary_config(self):
        source, slot, _ = self.plan()
        self.assertEqual(slot, 'green')
        self.assertEqual(source['slot'], 'amber')

    def test_any_gateway_inflight_blocks_reuse(self):
        self.inventory[1]['status']['activeRequests']['18318'] = 1
        with self.assertRaisesRegex(RuntimeError, '没有空闲'):
            self.plan()

    def test_config_divergence_blocks_overwrite(self):
        (self.root / 'config-blue/config.yaml').write_text('port: 18317\nroute: different\n')
        with self.assertRaisesRegex(RuntimeError, '配置尚未统一'):
            self.plan()

    def test_allowed_port_intersection(self):
        self.inventory[1]['allowed'].remove(18318)
        with self.assertRaisesRegex(RuntimeError, '没有空闲'):
            self.plan()

    def test_switch_preserves_old_backends(self):
        observed = [dict(status=dict(activePort=18318)) for _ in self.inventory]
        with patch.object(bg, 'deployment_plan', return_value=(self.source, 'green', self.inventory)), \
             patch.object(bg, 'prepare_config'), patch.object(bg, 'start_slot'), \
             patch.object(bg, 'probe'), patch.object(bg, 'stop') as stop, \
             patch.object(bg, 'gateway_inventory', return_value=observed):
            result = bg.deploy('new')
        self.assertEqual(result['image'], 'new')
        for gateway in self.inventory:
            self.assertEqual(json.loads(gateway['path'].read_text())['port'], 18318)
        stop.assert_not_called()

    def test_failed_switch_restores_all_pointers_without_killing_candidate(self):
        with patch.object(bg, 'deployment_plan', return_value=(self.source, 'green', self.inventory)), \
             patch.object(bg, 'prepare_config'), patch.object(bg, 'start_slot'), \
             patch.object(bg, 'probe'), patch.object(bg, 'stop') as stop, \
             patch.object(bg, 'gateway_inventory', side_effect=RuntimeError('unreachable')):
            with self.assertRaisesRegex(RuntimeError, 'unreachable'):
                bg.deploy('new')
        for gateway in self.inventory:
            self.assertEqual(json.loads(gateway['path'].read_text()), gateway['state'])
        stop.assert_not_called()

    def test_upstream_not_included_blocks_before_build(self):
        with patch.object(agent, 'run'), patch.object(agent.subprocess, 'run',
                return_value=subprocess.CompletedProcess([], 1)), patch.object(agent, 'write_state') as write:
            with self.assertRaisesRegex(RuntimeError, '尚未合并'):
                agent.verify_upstream_included(self.root, 'v7.3.17')
        write.assert_not_called()

    def test_included_upstream_is_recorded(self):
        with patch.object(agent, 'run'), patch.object(agent.subprocess, 'run',
                return_value=subprocess.CompletedProcess([], 0)), patch.object(agent, 'write_state') as write:
            agent.verify_upstream_included(self.root, 'v7.3.17')
        write.assert_called_once_with(upstreamIntegratedVersion='v7.3.17')

    def test_missing_integration_evidence_is_not_installable(self):
        with patch.object(agent, 'read_state', return_value={}), \
             patch.object(agent, 'current_image', return_value='cli-proxy-api:yeutech-1234567'), \
             patch.object(agent, 'container_health', return_value='healthy'):
            status = agent.public_status()
        self.assertFalse(status['updateReady'])
        self.assertIn('检查更新', status['updateBlockedReason'])


if __name__ == '__main__':
    unittest.main()
