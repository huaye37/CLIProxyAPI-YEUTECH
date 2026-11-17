import importlib.util
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('image_routes', ROOT / 'enable-image-routes.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class ImageRoutesTest(unittest.TestCase):
    def test_exact_extension_and_idempotence(self):
        for name in ('llm-api.nginx.conf', 'llm-api-v4.nginx.conf'):
            new = (ROOT / name).read_bytes()
            old = new.replace(module.NEW.encode(), module.OLD.encode())
            self.assertEqual(module.patch(old), new)
            self.assertEqual(module.patch(new), new)

    def test_unknown_or_duplicate_layout_rejected(self):
        for value in (b'', module.OLD.encode() * 2, (module.OLD + module.NEW).encode()):
            with self.assertRaises(ValueError):
                module.patch(value)

    def test_old_routes_and_images_allowed(self):
        for suffix in ('models', 'model-capabilities', 'responses', 'responses/compact',
                       'alpha/search', 'chat/completions', 'messages', 'messages/count_tokens',
                       'images/generations', 'images/edits'):
            self.assertIsNotNone(re.fullmatch(module.NEW, '/v1/' + suffix), suffix)

    def test_management_and_other_paths_stay_blocked(self):
        for path in ('/v8/management/config', '/v0/management/config', '/v1/images',
                     '/v1/images/edits/extra', '/v1/images/delete', '/v1/files', '/healthz'):
            self.assertIsNone(re.fullmatch(module.NEW, path), path)


if __name__ == '__main__':
    unittest.main()
