"""Exercise the existing workbench BFF using its server-side identity contract."""
import base64
import hashlib
import hmac
import json
from pathlib import Path
import time
import urllib.request
import urllib.error

root = Path('/volume1/docker/yeutech-agent/runtime')
registry = json.loads((root / 'workers/registry.json').read_text())
owner = next(worker for worker in registry['workers'] if worker['portalUserId'] == 1)
secret = (root / 'secrets/portal.identity.secret').read_text().strip().encode()
encode = lambda value: base64.urlsafe_b64encode(value).decode().rstrip('=')
payload = encode(json.dumps({'sub':owner['portalUserId'],'username':owner['username'],'exp':int(time.time())+600}).encode())
identity = payload + '.' + encode(hmac.new(secret, payload.encode(), hashlib.sha256).digest())

def request(path, data=None):
    req = urllib.request.Request('http://127.0.0.1:18140'+path,
        data=json.dumps(data).encode() if data is not None else None,
        headers={'x-yeutech-agent-identity':identity,'Content-Type':'application/json'})
    try:
        with urllib.request.urlopen(req, timeout=60) as response:
            body = response.read()
            return json.loads(body) if body else None
    except urllib.error.HTTPError as error:
        raise RuntimeError(str(error.code)+' '+error.read().decode()[:1000]) from None

models = request('/api/models')
model = next((model for model in models['data'] if model['id']=='chatgpt-web-instant'), None)
print('WORKBENCH_MODEL', json.dumps(model, ensure_ascii=False), flush=True)
print('WORKBENCH_RELOAD', json.dumps(models.get('reload')), flush=True)
assert model and model['selectable'], 'WEB model not executable in workbench'
session = request('/api/agent/session', {'title':'GPT WEB 验收 · 工具闭环'})
session_id = session['id']
print('TEST_SESSION', session_id, flush=True)
request('/api/agent/session/'+session_id+'/prompt_async', {
    'model':{'providerID':'yeutech','modelID':model['id']},
    'parts':[{'type':'text','text':'请只做一件事：调用 bash 工具执行 printf GPT_WORKBENCH_TOOL_OK，不读写任何文件；拿到工具结果后，最终只回复 GPT_WORKBENCH_TOOL_OK。'}],
    'yeutech':{'permissionMode':'full','workload':'general-agent'},
})
for attempt in range(60):
    snapshot = request('/api/workbench/sessions/'+session_id+'/snapshot')
    messages = snapshot.get('messages', [])
    assistants = [message for message in messages if message.get('role')=='assistant']
    completed = [item for item in snapshot.get('trajectory', []) if item.get('type')=='tool' and item.get('status')=='completed']
    if assistants and assistants[-1].get('completedAt'):
        latest = assistants[-1]
        print('WORKBENCH_RESULT', json.dumps({'text':latest.get('text'),'error':latest.get('error'),'completedTools':len(completed)},ensure_ascii=False),flush=True)
        if latest.get('error'):
            raise RuntimeError('Workbench run failed')
        if 'GPT_WORKBENCH_TOOL_OK' in str(latest.get('text','')) and completed:
            print('WORKBENCH_REAL_TOOL_ROUNDTRIP_OK',flush=True)
            break
    if attempt % 6 == 0:
        print('WORKBENCH_PENDING', attempt, 'messages',len(messages),'completedTools',len(completed), flush=True)
    time.sleep(3)
else:
    raise RuntimeError('Workbench run did not finish with a real tool result')
