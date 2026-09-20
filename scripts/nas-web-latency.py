"""Measure Web/control-plane latency without sending model prompts or secrets."""
import concurrent.futures
import json
from pathlib import Path
import subprocess
import time
import urllib.request

docker = '/var/packages/ContainerManager/target/usr/bin/docker'
def environment(name):
    info=json.loads(subprocess.check_output([docker,'inspect',name]))[0]
    return dict(item.split('=',1) for item in info['Config']['Env'] if '=' in item)
manager=environment('yeutech-api-manager')
proxy=manager.get('PROXY_URL','http://127.0.0.1:18319')
print('MANAGER_PROXY',proxy,flush=True)
for name in ('novel-ai-proxy-gateway','novel-ai-proxy-gateway-v2'):
    env=environment(name)
    print(name,{key:value for key,value in env.items() if key.startswith('GATEWAY_')},flush=True)
api=Path('/volume1/docker/yeutech-api-manager/secrets/api.key').read_text().strip()
management=Path('/volume1/docker/yeutech-api-manager/secrets/management.key').read_text().strip()
def probe(entry):
    url,key=entry
    start=time.monotonic()
    try:
        request=urllib.request.Request(url,headers={'Authorization':'Bearer '+key})
        with urllib.request.urlopen(request,timeout=12) as response:
            data=json.load(response)
            detail={field:data.get(field) for field in ('status','authenticated','activePort','activeRequests') if field in data}
            if url.endswith('model-capabilities'):
                detail['web_models']=[{'id':m['id'],'selectable':m.get('selectable')} for m in data.get('data',[]) if 'web' in m['id']]
        print('PROBE',url,round(time.monotonic()-start,3),detail,flush=True)
    except Exception as error:
        print('PROBE',url,round(time.monotonic()-start,3),type(error).__name__,flush=True)
entries=[(proxy+'/v1/model-capabilities',api),(proxy+'/v0/management/auth-files',management)]
entries.extend((proxy+'/v0/management/web-subscriptions/'+channel+'/status',management) for channel in ('chatgpt-web','gemini-web'))
with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
    list(pool.map(probe,entries))
