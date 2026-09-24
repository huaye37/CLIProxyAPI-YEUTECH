#!/usr/bin/env python3
"""Three-way merge of model catalog data; reject overlapping scalar edits."""

from __future__ import annotations

import json
import sys
from pathlib import Path


MISSING = object()


class MergeConflict(Exception):
    pass


def model_index(value, path):
    if not isinstance(value, list) or any(not isinstance(item, dict) or not isinstance(item.get('id'), str) for item in value):
        raise MergeConflict(f'{path}: model list has no unique IDs')
    index = {item['id']: item for item in value}
    if len(index) != len(value):
        raise MergeConflict(f'{path}: duplicate model ID')
    return index


def merge(base, ours, theirs, path='$'):
    if ours == theirs:
        return ours
    if ours == base:
        return theirs
    if theirs == base:
        return ours
    if any(value is MISSING for value in (base, ours, theirs)):
        raise MergeConflict(f'{path}: deletion conflicts with an edit')
    if all(isinstance(value, dict) for value in (base, ours, theirs)):
        result = {}
        for key in set(base) | set(ours) | set(theirs):
            value = merge(base.get(key, MISSING), ours.get(key, MISSING),
                          theirs.get(key, MISSING), path + '.' + key)
            if value is not MISSING:
                result[key] = value
        return result
    if all(isinstance(value, list) for value in (base, ours, theirs)) and path.count('.') == 1:
        b, o, t = (model_index(value, path) for value in (base, ours, theirs))
        order = list(dict.fromkeys([*t, *o]))
        merged = {key: merge(b.get(key, MISSING), o.get(key, MISSING),
                             t.get(key, MISSING), f'{path}[{key}]') for key in order}
        return [merged[key] for key in order if merged[key] is not MISSING]
    raise MergeConflict(f'{path}: both branches changed the same value')


def main():
    base_path, ours_path, theirs_path = map(Path, sys.argv[1:4])
    base, ours, theirs = (json.loads(path.read_text()) for path in (base_path, ours_path, theirs_path))
    try:
        merged = merge(base, ours, theirs)
    except MergeConflict as error:
        print(error, file=sys.stderr)
        return 1
    ours_path.write_text(json.dumps(merged, ensure_ascii=False, indent=2) + '\n')
    return 0


if __name__ == '__main__':
    raise SystemExit(main())
