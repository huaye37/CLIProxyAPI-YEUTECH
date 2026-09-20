#!/usr/bin/env python3
"""Probe and hot-enable Codex delegation compatibility without restarting inference."""
import argparse
import json
from pathlib import Path
import re
import shutil
import time
import urllib.error
import urllib.request

ROOT = Path('/volume1/docker/novel-ai-proxy')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true')
    parser.add_argument('--probe', action='store_true')
    parser.add_argument('--reload', action='store_true')
    parser.add_argument('--url', default='http://127.0.0.1:18310')
    args = parser.parse_args()
    state = json.loads((ROOT / 'gateway/active-v2.json').read_text())
    assert state['slot'] in ('amber', 'blue', 'green')
    config = ROOT / ('config-' + state['slot']) / 'config.yaml'
    source = config.read_text()
    # Fail closed instead of guessing how to modify an existing Codex block.
    enabled = bool(re.search(r'(?m)^  orphan-delegation-compatibility: true\s*$', source))
    print(json.dumps({'slot': state['slot'], 'port': state['port'], 'enabled': enabled}), flush=True)
    if args.apply and not enabled:
        assert not re.search(r'(?m)^codex:', source), 'Existing Codex block requires review'
        assert 'orphan-delegation-compatibility' not in source
        backup = config.with_name('config.before-delegation-' + str(time.time_ns()) + '.yaml')
        shutil.copy2(config, backup)
        updated = source.rstrip() + '\n\ncodex:\n  orphan-delegation-compatibility: true\n'
        temporary = config.with_suffix('.delegation-next')
        temporary.write_text(updated)
        shutil.copymode(config, temporary)
        assert config.read_text() == source, 'Concurrent config update'
        temporary.replace(config)
        print(json.dumps({'applied': True, 'backup': str(backup), 'restart': False}), flush=True)
    if args.reload or args.apply:
        key = Path('/volume1/docker/yeutech-api-manager/secrets/management.key').read_text().strip()
        base = 'http://127.0.0.1:%s/v0/management' % state['port']
        def management(path, data=None, content_type='application/json'):
            request = urllib.request.Request(base + path, data=data,
                method='PUT' if data is not None else 'GET', headers={
                    'Authorization': 'Bearer ' + key, 'Content-Type': content_type})
            with urllib.request.urlopen(request, timeout=30) as response:
                return json.load(response)
        current = management('/config')
        print(json.dumps({'runtime_compat_before': current.get('codex', {}).get('orphan-delegation-compatibility')}), flush=True)
        updated = config.read_bytes()
        assert b'orphan-delegation-compatibility: true' in updated
        management('/config.yaml', updated, 'application/yaml')
        # This idempotent management save invokes the explicit runtime reload hook.
        debug = management('/debug')['debug']
        management('/debug', json.dumps({'value': debug}).encode())
        print(json.dumps({'runtime_reload_requested': True, 'debug_unchanged': debug}), flush=True)
    if not args.probe:
        return
    match = re.search(r'(?m)^api-keys:\s*\n\s*-\s*["\']?([^"\'\s]+)', source)
    assert match, 'No configured probe key'
    payload = {'model': 'gemini-3.8-flash-high', 'stream': True, 'input': [
        {'type': 'message', 'role': 'user', 'content': 'This is an isolated routing diagnostic. No tools or external actions.'},
        {'type': 'message', 'role': 'assistant', 'content': [{'type': 'output_text', 'text': 'Ready.'}]},
        {'type': 'function_call_output', 'id': 'fco_delegation_diagnostic',
         'namespace': 'codex_app', 'name': 'send_message_to_thread',
         'output': '<codex_delegation><input>Reply with exactly DELEGATION_OK_729. Do not call tools.</input></codex_delegation>'}
    ]}
    request = urllib.request.Request(args.url.rstrip('/') + '/v1/responses',
        data=json.dumps(payload).encode(), headers={'Authorization': 'Bearer ' + match.group(1),
        'Content-Type': 'application/json', 'User-Agent': 'Codex Desktop'})
    started = time.monotonic()
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            text, completed, errors = '', False, []
            for line in response:
                if not line.startswith(b'data:'):
                    continue
                try:
                    event = json.loads(line[5:])
                except ValueError:
                    continue
                if event.get('type') == 'response.output_text.delta':
                    text += event.get('delta', '')
                if event.get('type') == 'response.completed':
                    completed = True
                if event.get('type') in ('error', 'response.failed'):
                    errors.append(event.get('type'))
            print(json.dumps({'status': response.status, 'completed': completed,
                'instruction_received': 'DELEGATION_OK_729' in text, 'errors': errors,
                'seconds': round(time.monotonic()-started, 2)}), flush=True)
            if not completed or 'DELEGATION_OK_729' not in text or errors:
                raise SystemExit(1)
    except urllib.error.HTTPError as error:
        body = error.read()
        print(json.dumps({'status': error.code, 'model_turn_error': b'Requests ending with a model turn' in body}), flush=True)
        raise SystemExit(1)


if __name__ == '__main__':
    main()
