"""Verify an isolated Web protocol adapter before hot-adding its live route."""
import json
import os
import re
import secrets
import time
import sys
import urllib.request
from pathlib import Path

sys.path.insert(0, '/volume1/docker/yeutech-api-manager/updater')
import bluegreen as bg

IMAGE = 'cli-proxy-api:yeutech-web-wire-v2'
state = bg.read_state()
directory = Path('/volume1/docker/web-subscriptions/responses-adapter')
directory.mkdir(mode=0o700, exist_ok=True)
config_file = directory / 'config.yaml'
env = dict(line.split('=', 1) for line in Path('/volume1/docker/web-subscriptions/chatgpt/.env').read_text().splitlines() if '=' in line)
provider = {
    'name': 'chatgpt-web', 'wire-api': 'responses', 'web-driver': 'chatgpt-web',
    'base-url': 'http://127.0.0.1:18791/v1', 'request-retry': 0,
    'api-key-entries': [{'api-key': env['CHATGPT_WEB_DRIVER_TOKEN'], 'proxy-url': 'direct'}],
    'models': [{'name': 'chatgpt-web', 'alias': 'chatgpt-web-instant',
                'display-name': 'ChatGPT Instant · WEB', 'force-mapping': True,
                'max-context-length': 16000, 'capability-max-output-tokens': 4096,
                'input-modalities': ['text'], 'output-modalities': ['text']}],
}
if config_file.exists():
    key = json.loads(config_file.read_text())['api-keys'][0]
else:
    key = secrets.token_hex(32)
    config_file.write_text(json.dumps({'host': '127.0.0.1', 'port': 18316, 'auth-dir': '/tmp/web-auth', 'api-keys': [key], 'remote-management': {'disable-control-panel': True}, 'openai-compatibility': [provider]}))
    os.chmod(config_file, 0o600)
if not bg.exists('yeutech-web-responses-adapter'):
    bg.run(bg.DOCKER, 'run', '-d', '--name', 'yeutech-web-responses-adapter', '--restart', 'unless-stopped', '--network', 'host', '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', '--memory', '256m', '--tmpfs', '/tmp:rw,nosuid,size=64m', '-v', str(directory) + ':/config:ro', IMAGE, './CLIProxyAPI', '-config', '/config/config.yaml')

def request(path, payload=None, port=None):
    req = urllib.request.Request(
        f'http://127.0.0.1:{port or 18316}/v1/{path}',
        data=json.dumps(payload).encode() if payload else None,
        headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'},
    )
    with urllib.request.urlopen(req, timeout=180) as response:
        return response.read().decode()

for attempt in range(30):
    try:
        models = json.loads(request('model-capabilities'))['data']
        break
    except Exception:
        time.sleep(1)
else:
    raise RuntimeError('Adapter failed to start')
model = next(m for m in models if m['id'] == 'chatgpt-web-instant')
assert model['selectable'] and model['delivery'] == 'web', model
print('CANDIDATE_MODEL', json.dumps(model, ensure_ascii=False), flush=True)
result = json.loads(request('responses', {'model': model['id'], 'input': 'Reply exactly WEB_PROXY_OK', 'stream': False}))
assert any(c.get('text') == 'WEB_PROXY_OK' for item in result.get('output', []) for c in item.get('content', [])), result
print('CANDIDATE_RESPONSES_OK', flush=True)
tool = {'type': 'function', 'name': 'echo', 'description': 'Return the supplied text', 'parameters': {'type': 'object', 'properties': {'text': {'type': 'string'}}, 'required': ['text']}}
prompt = {'type': 'message', 'role': 'user', 'content': 'Call echo once with WEB_TOOL_OK, then return its result exactly.'}
first = json.loads(request('responses', {'model': model['id'], 'input': [prompt], 'tools': [tool], 'stream': False}))
call = next(i for i in first['output'] if i['type'] == 'function_call')
assert call['name'] == 'echo' and json.loads(call['arguments'])['text'] == 'WEB_TOOL_OK', first
output = {'type': 'function_call_output', 'call_id': call['call_id'], 'output': json.loads(call['arguments'])['text']}
stream = request('responses', {'model': model['id'], 'input': [prompt, call, output], 'tools': [tool], 'stream': True})
assert 'response.completed' in stream and 'WEB_TOOL_OK' in stream and 'response.failed' not in stream, stream
print('CANDIDATE_TOOL_ROUNDTRIP_STREAM_OK', flush=True)
chat = json.loads(request('chat/completions', {'model': model['id'], 'messages': [{'role': 'user', 'content': 'Reply exactly WEB_CHAT_OK'}], 'stream': False}))
assert chat['choices'][0]['message']['content'] == 'WEB_CHAT_OK', chat
print('CANDIDATE_CHAT_OK', flush=True)
live_file = bg.slot_config(state['slot']) / 'config.yaml'
live_text = live_file.read_text()
if re.search(r'name:\s*[\"\x27]?chatgpt-web', live_text):
    raise RuntimeError('Live Web provider already present; inspect before replacing')
live_provider = dict(provider)
live_provider.pop('wire-api')
live_provider.pop('web-driver')
live_provider['base-url'] = 'http://127.0.0.1:18316/v1'
live_provider['api-key-entries'] = [{'api-key': key, 'proxy-url': 'direct'}]
live_provider['models'] = [dict(provider['models'][0], name=model['id'])]
entry = '  - ' + json.dumps(live_provider, ensure_ascii=False) + '\n'
updated, count = re.subn(r'(?m)^openai-compatibility:\s*\n', lambda m: m.group(0) + entry, live_text, count=1)
if count != 1:
    raise RuntimeError('Expected existing live compatibility section')
backup = directory / 'proxy-config-before-web.yaml'
backup.write_text(live_text)
os.chmod(backup, 0o600)
live_file.write_text(updated)
key = bg.api_key(bg.slot_config(state['slot']))
for attempt in range(30):
    if any(m['id'] == model['id'] for m in json.loads(request('model-capabilities', port=18319))['data']):
        break
    time.sleep(1)
else:
    raise RuntimeError('Live model did not register')
public = json.loads(request('responses', {'model': model['id'], 'input': 'Reply exactly WEB_LIVE_OK', 'stream': False}, port=18319))
assert any(c.get('text') == 'WEB_LIVE_OK' for item in public.get('output', []) for c in item.get('content', [])), public
print('LIVE_GATEWAY_OK model=' + model['id'], flush=True)
print('Existing proxy slots and gateway left running', flush=True)
