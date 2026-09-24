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
            (self.root / name).write_text(json.dumps(state))
        self.source = self.inventory[1]['state']

    def plan(self):
        with patch.object(bg, 'gateway_inventory', return_value=self.inventory), \
             patch.object(bg, 'production_state', return_value=self.source), \
             patch.object(bg, 'ROOT', self.root), \
             patch.object(bg, 'backend_connections', return_value=0):
            return bg.deployment_plan()

    def test_plan_uses_only_idle_slot_and_primary_config(self):
        source, slot, _ = self.plan()
        self.assertEqual(slot, 'green')
        self.assertEqual(source['slot'], 'amber')

    def test_any_gateway_inflight_blocks_reuse(self):
        self.inventory[1]['status']['activeRequests']['18318'] = 1
        self.inventory[1]['status']['version'] = 'lifecycle-v3'
        with self.assertRaisesRegex(RuntimeError, '没有空闲'):
            self.plan()

    def test_config_divergence_keeps_a_safe_staging_slot(self):
        (self.root / 'config-blue/config.yaml').write_text('port: 18317\nroute: different\n')
        _, slot, _ = self.plan()
        self.assertEqual(slot, 'green')

    def test_known_additive_web_config_can_be_unified(self):
        (self.root / 'config-blue/config.yaml').write_text(
            'port: 18317\nroute: retained\nopenai-compatibility:\n  - name: existing\n')
        (self.root / 'config-amber/config.yaml').write_text(
            'port: 18315\nroute: retained\nopenai-compatibility:\n  - name: existing\n'
            '  - name: chatgpt-web\n  - name: chatgpt-web-gpt-6-pro\n'
            '  - name: chatgpt-web-gpt-5-6-thinking\n  - name: chatgpt-web-gpt-5-6-pro\n'
            'codex:\n  orphan-delegation-compatibility: false\n')
        with patch.object(bg, 'ROOT', self.root), \
             patch.object(bg, 'production_state', return_value=self.source):
            self.assertEqual(bg.additive_web_config_source(self.inventory), 'amber')
            (self.root / 'config-blue/config.yaml').write_text(
                'port: 18317\nroute: changed\nopenai-compatibility:\n  - name: existing\n')
            self.assertIsNone(bg.additive_web_config_source(self.inventory))

    def test_live_codex_compatibility_setting_can_be_unified(self):
        (self.root / 'config-blue/config.yaml').write_text(
            'port: 18317\nroute: retained\nopenai-compatibility:\n  - name: existing\n')
        (self.root / 'config-amber/config.yaml').write_text(
            'port: 18315\nroute: retained\nopenai-compatibility:\n  - name: existing\n'
            '  - name: chatgpt-web\n  - name: chatgpt-web-gpt-6-pro\n'
            '  - name: chatgpt-web-gpt-5-6-thinking\n  - name: chatgpt-web-gpt-5-6-pro\n'
            'codex:\n  orphan-delegation-compatibility: true\n')
        with patch.object(bg, 'ROOT', self.root), \
             patch.object(bg, 'production_state', return_value=self.source):
            self.assertEqual(bg.additive_web_config_source(self.inventory), 'amber')

    def test_unify_only_rejects_unknown_difference(self):
        (self.root / 'config-blue/config.yaml').write_text('port: 18317\nroute: changed\n')
        with patch.object(bg, 'ROOT', self.root), \
             patch.object(bg, 'deployment_plan', return_value=(self.source, 'green', self.inventory)), \
             patch.object(bg, 'production_state', return_value=self.source), \
             patch.object(bg, 'start_slot') as start:
            with self.assertRaisesRegex(RuntimeError, '不符合已核对'):
                bg.deploy('old', unify_only=True)
        start.assert_not_called()

    def test_unify_only_aligns_older_lane_to_live_source_image(self):
        (self.root / 'config-blue/config.yaml').write_text(
            'port: 18317\nroute: retained\nopenai-compatibility:\n  - name: existing\n')
        (self.root / 'config-amber/config.yaml').write_text(
            'port: 18315\nroute: retained\nopenai-compatibility:\n  - name: existing\n'
            '  - name: chatgpt-web\n  - name: chatgpt-web-gpt-6-pro\n'
            '  - name: chatgpt-web-gpt-5-6-thinking\n  - name: chatgpt-web-gpt-5-6-pro\n'
            'codex:\n  orphan-delegation-compatibility: true\n')
        self.inventory[0]['state']['image'] = 'older'
        self.inventory[0]['path'].write_text(json.dumps(self.inventory[0]['state']))
        def observed(require_applied=True):
            return [dict(g, state=json.loads(g['path'].read_text()),
                         status=dict(activePort=json.loads(g['path'].read_text())['port'], activeRequests={}))
                    for g in self.inventory]
        with patch.object(bg, 'ROOT', self.root), \
             patch.object(bg, 'deployment_plan', return_value=(self.source, 'green', self.inventory)), \
             patch.object(bg, 'gateway_inventory', side_effect=observed), \
             patch.object(bg, 'slot_reusable', return_value=True), \
             patch.object(bg, 'wait_reusable_slot'), \
             patch.object(bg, 'prepare_config') as prepare, \
             patch.object(bg, 'start_slot') as start, \
             patch.object(bg, 'probe'), \
             patch.object(bg, 'production_state', side_effect=lambda: json.loads(self.inventory[1]['path'].read_text())):
            result = bg.deploy('old', unify_only=True)
        self.assertEqual(result['image'], 'old')
        self.assertEqual({json.loads(g['path'].read_text())['slot'] for g in self.inventory}, {'green'})
        prepare.assert_called_once_with(self.root / 'config-amber', 'green')
        self.assertEqual(start.call_args.args[1], 'old')

    def test_legacy_stale_count_requires_no_backend_socket(self):
        self.inventory[0]['status']['activeRequests']['18318'] = 16
        with patch.object(bg, 'backend_connections', return_value=0):
            self.assertTrue(bg.slot_reusable('green', self.inventory))
        with patch.object(bg, 'backend_connections', return_value=1):
            self.assertFalse(bg.slot_reusable('green', self.inventory))

    def test_allowed_port_intersection(self):
        self.inventory[1]['allowed'].remove(18318)
        with self.assertRaisesRegex(RuntimeError, '没有空闲'):
            self.plan()

    def test_switch_preserves_old_backends(self):
        def observed(require_applied=True):
            return [dict(g, state=json.loads(g['path'].read_text()),
                         status=dict(activePort=json.loads(g['path'].read_text())['port'], activeRequests={}))
                    for g in self.inventory]
        with patch.object(bg, 'ROOT', self.root), \
             patch.object(bg, 'deployment_plan', return_value=(self.source, 'green', self.inventory)), \
             patch.object(bg, 'prepare_config'), patch.object(bg, 'start_slot'), \
             patch.object(bg, 'probe'), patch.object(bg, 'stop') as stop, \
             patch.object(bg, 'gateway_inventory', side_effect=observed), \
             patch.object(bg, 'slot_reusable', return_value=True), \
             patch.object(bg, 'wait_reusable_slot'), \
             patch.object(bg, 'production_state', side_effect=lambda: json.loads(self.inventory[1]['path'].read_text())):
            result = bg.deploy('new')
        self.assertEqual(result['image'], 'new')
        for gateway in self.inventory:
            self.assertEqual(json.loads(gateway['path'].read_text())['port'], 18318)
        stop.assert_not_called()

    def test_distinct_gateway_configs_roll_separately(self):
        (self.root / 'config-blue/config.yaml').write_text('port: 18317\nroute: standard\n')
        (self.root / 'config-amber/config.yaml').write_text('port: 18315\nroute: web-models\n')
        def observed(require_applied=True):
            return [dict(g, state=json.loads(g['path'].read_text()),
                         status=dict(activePort=json.loads(g['path'].read_text())['port'], activeRequests={}))
                    for g in self.inventory]
        def reusable(slot, rows):
            return all(g['status']['activePort'] != bg.SLOTS[slot] for g in rows)
        with patch.object(bg, 'ROOT', self.root), \
             patch.object(bg, 'deployment_plan', return_value=(self.source, 'green', self.inventory)), \
             patch.object(bg, 'gateway_inventory', side_effect=observed), \
             patch.object(bg, 'slot_reusable', side_effect=reusable), \
             patch.object(bg, 'wait_reusable_slot'), \
             patch.object(bg, 'prepare_config') as prepare, \
             patch.object(bg, 'start_slot'), patch.object(bg, 'probe'), \
             patch.object(bg, 'production_state', side_effect=lambda: json.loads(self.inventory[1]['path'].read_text())):
            result = bg.deploy('new')
        self.assertEqual(result['slot'], 'blue')
        self.assertEqual(json.loads(self.inventory[0]['path'].read_text())['slot'], 'green')
        self.assertEqual(json.loads(self.inventory[1]['path'].read_text())['slot'], 'blue')
        self.assertEqual([call.args[1] for call in prepare.call_args_list], ['green', 'blue'])
        self.assertEqual([call.args[0].name for call in prepare.call_args_list], ['config-blue', 'config-amber'])

    def test_failed_switch_restores_all_pointers_without_killing_candidate(self):
        with patch.object(bg, 'ROOT', self.root), \
             patch.object(bg, 'deployment_plan', return_value=(self.source, 'green', self.inventory)), \
             patch.object(bg, 'prepare_config'), patch.object(bg, 'start_slot'), \
             patch.object(bg, 'probe'), patch.object(bg, 'stop') as stop, \
             patch.object(bg, 'slot_reusable', return_value=True), \
             patch.object(bg, 'wait_reusable_slot'), \
             patch.object(bg, 'production_state', return_value=self.source), \
             patch.object(bg, 'gateway_inventory', side_effect=[self.inventory, self.inventory, RuntimeError('unreachable')]):
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

    def test_integrated_source_remains_blocked_without_safe_slot(self):
        saved = {'upstreamIncluded': True, 'installReady': False,
                 'latestVersion': 'v7.3.17', 'checkedAt': 1727200000,
                 'branchCommit': 'a6ad8678e21568a9e83c2f5cd115eded28b82dba',
                 'installationMessage': '不同推理入口的配置尚未统一'}
        with patch.object(agent, 'read_state', return_value=saved), \
             patch.object(agent, 'current_image', return_value='cli-proxy-api:yeutech-1234567'), \
             patch.object(agent, 'container_health', return_value='healthy'):
            status = agent.public_status()
        self.assertTrue(status['updateReady'])
        self.assertFalse(status['installReady'])
        self.assertEqual(status['branchCommit'], saved['branchCommit'])
        self.assertIn('配置尚未统一', status['updateBlockedReason'])


if __name__ == '__main__':
    unittest.main()
