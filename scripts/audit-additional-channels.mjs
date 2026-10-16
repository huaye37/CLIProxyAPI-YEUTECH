import {spawn,execFileSync} from 'node:child_process';
import {readFileSync,writeFileSync} from 'node:fs';
const key=execFileSync('security',['find-generic-password','-a','codex','-s','cn.yeutech.llm-api','-w'],{encoding:'utf8'}).trim();
const video=JSON.parse(readFileSync('docs/audits/2026-10-17-media-nas-execution.json')).results.filter(m=>m.requestId);
const code=`import json,sys,urllib.request,urllib.error,uuid
from pathlib import Path
key=sys.stdin.readline().strip()
jobs=json.loads(sys.stdin.readline())
for job in jobs:
 try:
  req=urllib.request.Request('http://127.0.0.1:18319/v1/videos/'+job['requestId'],headers={'Authorization':'Bearer '+key})
  with urllib.request.urlopen(req,timeout=30) as r: data=json.load(r)
  video=data.get('video') or {}
  print(json.dumps({'id':job['id'],'scope':'video_poll','httpStatus':200,'upstreamStatus':data.get('status'),'hasVideoUrl':bool(video.get('url') or data.get('url')),'requestId':job['requestId']}),flush=True)
 except urllib.error.HTTPError as e: print(json.dumps({'id':job['id'],'scope':'video_poll','httpStatus':e.code}),flush=True)
 except Exception as e: print(json.dumps({'id':job['id'],'scope':'video_poll','errorType':type(e).__name__}),flush=True)
try:
 token=Path('/volume1/docker/yeutech-api-manager/secrets/plugin-service.token').read_text().strip()
 body={'consumer':'api-platform-verification','idempotencyKey':'model-audit-'+str(uuid.uuid4()),'model':'gemini-3.1-flash-image','size':'1024x1024','quality':'auto','prompt':'A blue circle centered on a white background. No text.'}
 req=urllib.request.Request('http://127.0.0.1:18140/api/plugins/v1/image-generation/business-runs',data=json.dumps(body).encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
 with urllib.request.urlopen(req,timeout=120) as r: data=json.load(r)
 result=data.get('data',{})
 print(json.dumps({'id':'gemini-3.1-flash-image','scope':'business_image_plugin','httpStatus':200,'contractVersion':data.get('contractVersion'),'result':{k:v for k,v in result.items() if k in ['id','model','width','height','mimeType','bytes','sha256','status']}}),flush=True)
except urllib.error.HTTPError as e: print(json.dumps({'id':'gemini-3.1-flash-image','scope':'business_image_plugin','httpStatus':e.code}),flush=True)
except Exception as e: print(json.dumps({'id':'gemini-3.1-flash-image','scope':'business_image_plugin','errorType':type(e).__name__}),flush=True)
`;
const p=spawn('ssh',['-o','BatchMode=yes','yeutech-nas',"python3 -c '"+code.replaceAll("'","'\\''")+"'"],{stdio:['pipe','pipe','pipe']});
const report={observedAt:new Date().toISOString(),results:[]};let buffer='';
p.stdout.on('data',s=>{buffer+=s;let i;while((i=buffer.indexOf('\n'))>=0){const line=buffer.slice(0,i);buffer=buffer.slice(i+1);try{const row=JSON.parse(line);report.results.push(row);writeFileSync('docs/audits/2026-10-17-additional-channels.json',JSON.stringify(report,null,2)+'\n',{mode:0o600});console.log(JSON.stringify(row));}catch{}}});p.stderr.on('data',()=>{});p.stdin.end(key+'\n'+JSON.stringify(video)+'\n');p.on('exit',code=>process.exitCode=code||0);
