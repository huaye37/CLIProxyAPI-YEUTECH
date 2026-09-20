import importlib.util
from pathlib import Path
import sys
import unittest
from unittest.mock import patch
import io
import json

sys.dont_write_bytecode = True
spec=importlib.util.spec_from_file_location('bluegreen',Path(__file__).parents[1]/'updater/bluegreen.py')
module=importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

class GatewayAccounting(unittest.TestCase):
    def test_counts_all_gateways(self):
        values=[{'ok':True,'activePort':18317,'activeRequests':{'18317':2}},
                {'ok':True,'activePort':18315,'activeRequests':{'18315':3}},
                {'ok':True,'activePort':18317,'activeRequests':{'18317':4}},
                {'ok':True,'activePort':18315,'activeRequests':{'18315':1}}]
        with patch.object(module,'exists',return_value=True), patch.object(module.urllib.request,'urlopen',side_effect=[io.BytesIO(json.dumps(v).encode()) for v in values]):
            status=module.gateway_status()
        self.assertEqual(status['activeRequests'],{'18317':6,'18315':4})
        self.assertEqual(set(status['activePorts']),{18317,18315})

    def test_active_empty_slot_not_reused(self):
        status={'activePorts':[18317,18315],'activeRequests':{'18317':0,'18315':0,'18318':0}}
        with patch.object(module,'gateway_status',return_value=status),patch.object(module,'gateway_allowed_ports',return_value={18317,18315}):
            with self.assertRaises(RuntimeError):module.select_deploy_slot('blue')

    def test_unknown_gateway_state_blocks_release(self):
        main={'ok':True,'activePort':18317,'activeRequests':{}}
        with patch.object(module,'exists',return_value=True),patch.object(module.urllib.request,'urlopen',side_effect=[io.BytesIO(json.dumps(main).encode()),OSError('unavailable')]):
            with self.assertRaises(OSError):module.gateway_status()

if __name__=='__main__':unittest.main()
