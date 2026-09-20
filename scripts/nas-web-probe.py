import json
import sys
import urllib.request
import urllib.error
sys.path.insert(0, '/volume1/docker/yeutech-api-manager/updater')
import bluegreen as bg
key = bg.api_key(bg.slot_config(bg.read_state()['slot']))
def request(path, payload=None):
    req=urllib.request.Request('http://127.0.0.1:18319/v1/'+path, data=json.dumps(payload).encode() if payload else None, headers={'Authorization':'Bearer '+key,'Content-Type':'application/json'})
    try:
        with urllib.request.urlopen(req,timeout=180) as response:
            return response.read().decode()
    except urllib.error.HTTPError as error:
        raise RuntimeError(str(error.code)+' '+error.read().decode()[:1800]) from None
models=json.loads(request('model-capabilities'))['data']
print(json.dumps([m for m in models if 'web' in m['id']],ensure_ascii=False),flush=True)
print(request('responses',{'model':'chatgpt-web-instant','input':'Reply exactly WEB_LIVE_OK','stream':False}),flush=True)
tool={'type':'function','name':'echo','description':'Echo supplied text','parameters':{'type':'object','properties':{'text':{'type':'string'}},'required':['text']}}
prompt={'type':'message','role':'user','content':'Call echo once with WEB_LIVE_TOOL_OK, then reply with the returned value exactly.'}
first=json.loads(request('responses',{'model':'chatgpt-web-instant','input':[prompt],'tools':[tool],'stream':False}))
call=next(item for item in first['output'] if item['type']=='function_call')
assert call['name']=='echo' and json.loads(call['arguments'])['text']=='WEB_LIVE_TOOL_OK',first
output={'type':'function_call_output','call_id':call['call_id'],'output':json.loads(call['arguments'])['text']}
result=request('responses',{'model':'chatgpt-web-instant','input':[prompt,call,output],'tools':[tool],'stream':True})
assert 'response.completed' in result and 'WEB_LIVE_TOOL_OK' in result and 'response.failed' not in result,result
print('LIVE_RESPONSES_TOOL_ROUNDTRIP_STREAM_OK',flush=True)
result=request('chat/completions',{'model':'chatgpt-web-instant','messages':[{'role':'user','content':'Reply exactly WEB_CHAT_STREAM_OK'}],'stream':True})
assert 'WEB_CHAT_STREAM_OK' in result and '[DONE]' in result,result
print('LIVE_CHAT_STREAM_OK',flush=True)
