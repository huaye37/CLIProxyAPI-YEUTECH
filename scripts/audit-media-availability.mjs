#!/usr/bin/env node
import {execFileSync} from 'node:child_process';
import {writeFileSync,mkdirSync} from 'node:fs';
import {dirname} from 'node:path';
import {classifyError} from './audit-model-capabilities.mjs';

const output=process.argv[2];if(!output)throw Error('report path required');
const key=execFileSync('security',['find-generic-password','-a','codex','-s','cn.yeutech.llm-api','-w'],{encoding:'utf8'}).trim();
const base=process.env.YEUTECH_AUDIT_BASE_URL||'https://llm-api.yeutech.cn';
const report={observedAt:new Date().toISOString(),baseUrl:base,scope:'minimum_media_generation',productionChanged:false,results:[]};
const save=()=>{mkdirSync(dirname(output),{recursive:true});writeFileSync(output,JSON.stringify(report,null,2)+'\n',{mode:0o600});};
const entries=[
 ...['gpt-image-1.5','gpt-image-2','gpt-image-2.5','gpt-image-2.5-flare','gpt-image-2.5-sunburst','gemini-3.1-flash-image','grok-imagine-image','grok-imagine-image-2.0','grok-imagine-image-quality'].map(id=>({id,path:'/v1/images/generations',body:{model:id,prompt:'A plain blue circle on a white background.',n:1,response_format:'b64_json'}})),
 ...['grok-tts','grok-voice-tts-1.0'].map(id=>({id,path:'/v1/audio/speech',body:{model:id,input:'Hello.',voice:'ara',response_format:'mp3'}})),
 ...['grok-imagine-video','grok-imagine-video-1.5','grok-imagine-video-1.5-preview'].map(id=>({id,path:'/v1/videos/generations',body:{model:id,prompt:'A blue circle slowly moving on a white background.',duration:1}})),
];
for(const e of entries){
 const started=Date.now(); const result={id:e.id,path:e.path,status:'not_tested'};
 try{
  const r=await fetch(base+e.path,{method:'POST',headers:{Authorization:'Bearer '+key,'Content-Type':'application/json'},body:JSON.stringify(e.body),signal:AbortSignal.timeout(90000)});
  const bytes=Buffer.from(await r.arrayBuffer());result.httpStatus=r.status;result.contentType=r.headers.get('content-type');
  let j={};try{j=JSON.parse(bytes.toString())}catch{}
  if(!r.ok){result.status=classifyError(r.status,j);}
  else if(e.path.endsWith('/speech')){result.bytes=bytes.length;result.status=result.contentType?.startsWith('audio/')&&bytes.length>64?'passed_audio_response':'unexpected_response';}
  else if(e.path.includes('/images/')){const images=(j.data||[]).filter(x=>typeof x.b64_json==='string');const valid=images.map(x=>Buffer.from(x.b64_json,'base64')).filter(b=>b.length>64&&(b.subarray(0,8).equals(Buffer.from([137,80,78,71,13,10,26,10]))||b[0]===255&&b[1]===216||b.subarray(0,4).toString()==='RIFF'));
   result.imageCount=valid.length;result.imageBytes=valid.map(b=>b.length);result.status=valid.length?'passed_image_bytes':j.data?.some(x=>x.url)?'image_url_returned_unverified':'unexpected_response';
  }else{result.requestId=j.request_id||j.id||null;result.upstreamStatus=j.status||null;result.status=result.requestId?'video_job_accepted_not_completed':'unexpected_response';}
 }catch(e){result.status=e.name==='TimeoutError'?'probe_timeout':'transport_error';}
 result.seconds=Math.round((Date.now()-started)/10)/100;result.observedAt=new Date().toISOString();report.results.push(result);save();console.log(JSON.stringify(result));
}
report.finishedAt=new Date().toISOString();save();
