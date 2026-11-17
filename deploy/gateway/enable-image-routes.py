#!/usr/bin/env python3
"""Enable only the two Images endpoints on the existing NAS nginx entry.

Run on NAS with root privileges and an independently observed config SHA256.
Does not install a proxy image, change credentials, or restart containers.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

OLD = "^/v1/(models|model-capabilities|responses(/compact)?|alpha/search|chat/completions|messages(/count_tokens)?)$"
NEW = "^/v1/(models|model-capabilities|responses(/compact)?|alpha/search|chat/completions|messages(/count_tokens)?|images/(generations|edits))$"
TARGET = Path('/etc/nginx/sites-enabled/yeutech-llm-api.conf')
BACKUPS = Path('/volume1/docker/yeutech-api-manager/data/node-channel-backups')


def patch(data):
    old, new = OLD.encode(), NEW.encode()
    if data.count(new) == 1 and old not in data:
        return data
    if data.count(old) != 1 or new in data:
        raise ValueError('unexpected inference route; inspect before changing')
    return data.replace(old, new, 1)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def atomic_write(path, data, metadata):
    fd, name = tempfile.mkstemp(prefix='.image-routes-', dir=str(path.parent))
    try:
        with os.fdopen(fd, 'wb') as out:
            os.fchmod(out.fileno(), metadata.st_mode & 0o7777)
            os.fchown(out.fileno(), metadata.st_uid, metadata.st_gid)
            out.write(data)
            out.flush()
            os.fsync(out.fileno())
        os.replace(name, str(path))
    finally:
        if os.path.exists(name):
            os.unlink(name)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--expected-sha256', required=True)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    target = TARGET.resolve(strict=True)
    original = target.read_bytes()
    if digest(original) != args.expected_sha256:
        raise SystemExit('config changed since preflight')
    updated = patch(original)
    result = {'before': digest(original), 'after': digest(updated), 'applied': False}
    if not args.apply or updated == original:
        print(json.dumps(result))
        return
    subprocess.run(['/usr/bin/nginx', '-t'], check=True)
    BACKUPS.mkdir(parents=True, exist_ok=True)
    backup = Path(tempfile.mkdtemp(prefix='image-routes-', dir=str(BACKUPS)))
    shutil.copy2(str(target), str(backup / 'yeutech-llm-api.conf'))
    os.chmod(str(backup / 'yeutech-llm-api.conf'), 0o600)
    metadata = target.stat()
    if target.read_bytes() != original:
        raise SystemExit('config changed during preflight')
    atomic_write(target, updated, metadata)
    try:
        subprocess.run(['/usr/bin/nginx', '-t'], check=True)
        subprocess.run(['/usr/bin/nginx', '-s', 'reload'], check=True)
    except Exception:
        atomic_write(target, original, metadata)
        subprocess.run(['/usr/bin/nginx', '-t'], check=True)
        subprocess.run(['/usr/bin/nginx', '-s', 'reload'], check=True)
        raise
    result.update(applied=True, backup=str(backup), reload='graceful')
    print(json.dumps(result))


if __name__ == '__main__':
    main()
