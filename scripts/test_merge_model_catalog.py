import unittest

from merge_model_catalog import MergeConflict, merge


class CatalogMergeTests(unittest.TestCase):
    def test_preserves_local_field_and_upstream_field(self):
        base = {'codex-plus': [{'id': 'review', 'type': 'openai'}]}
        ours = {'codex-plus': [{'id': 'review', 'type': 'openai', 'supported_workloads': ['review']},
                               {'id': 'spark', 'type': 'openai'}]}
        theirs = {'codex-plus': [{'id': 'review', 'type': 'openai', 'native_capabilities': {'web_search': True}}]}
        actual = merge(base, ours, theirs)['codex-plus']
        self.assertEqual([model['id'] for model in actual], ['review', 'spark'])
        self.assertEqual(actual[0]['supported_workloads'], ['review'])
        self.assertTrue(actual[0]['native_capabilities']['web_search'])

    def test_rejects_conflicting_model_limit(self):
        base = {'codex-plus': [{'id': 'gpt', 'context_length': 100}]}
        ours = {'codex-plus': [{'id': 'gpt', 'context_length': 200}]}
        theirs = {'codex-plus': [{'id': 'gpt', 'context_length': 300}]}
        with self.assertRaises(MergeConflict):
            merge(base, ours, theirs)

    def test_preserves_upstream_removal_when_local_unchanged(self):
        base = {'codex-plus': [{'id': 'old', 'type': 'openai'}]}
        self.assertEqual(merge(base, base, {'codex-plus': []})['codex-plus'], [])


if __name__ == '__main__':
    unittest.main()
