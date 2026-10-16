#!/usr/bin/env node
// Read-only catalogs plus explicitly requested, bounded inference probes.
// Credentials and raw provider errors must never enter the report.
import {execFileSync} from 'node:child_process';
import {mkdirSync,writeFileSync,renameSync,readFileSync} from 'node:fs';
import {dirname,resolve} from 'node:path';
import {pathToFileURL} from 'node:url';

export function classifyError(status, value) {
  const text=JSON.stringify(value).toLowerCase();
  if(/credits.{0,40}required|insufficient.{0,20}(credit|balance)/.test(text))return 'credits_required';
  if(/quota.{0,20}exhaust|usage_limit_reached|insufficient_quota|usage limit/.test(text))return 'quota_exhausted';
  if(/model.{0,30}(not found|not_found|does not exist)|unknown model/.test(text))return 'model_not_found';
  if(/model_cooldown/.test(text))return 'model_cooldown';
  if(status===401)return 'authentication_failed';
  if(status===403)return 'permission_denied';
  if(status===429)return 'rate_limited';
  if(status===400)return 'request_rejected';
  return 'upstream_error';
}

export function inspectCatalog(capabilities, clientModels) {
  const client=new Map(clientModels.map(m=>[m.slug,m]));
  return capabilities.map(m=>{
    const c=client.get(m.id); const gaps=[];
    for(const k of ['supported_input_modalities','supported_output_modalities','supported_workloads'])if(!m[k]?.length)gaps.push(k);
    if(m.token_limits_applicable!==false&&!m.context_length)gaps.push('context_length');
    if(m.token_limits_applicable!==false&&!m.max_output_tokens)gaps.push('max_output_tokens');
    // GPT intentionally retains a 500k consumer policy below its provider maximum.
    if(c && !m.id.startsWith('gpt-') && m.supported_workloads?.includes('conversation') && c.max_context_window!==m.context_length)gaps.push('client_context_mismatch');
    return {id:m.id,provider:m.owned_by,type:m.type,context:m.context_length,inputLimit:m.max_input_tokens,outputLimit:m.max_output_tokens,
      input:m.supported_input_modalities,output:m.supported_output_modalities,workloads:m.supported_workloads,
      thinking:m.thinking??null,parameters:m.supported_parameters??[],serviceTiers:m.service_tiers??[],
      available:m.available,selectable:m.selectable,capabilityStatus:m.capability_status,unavailableReason:m.unavailable_reason??null,
      clientContext:c?.context_window??null,clientMaxContext:c?.max_context_window??null,gaps,
      verification:{status:'not_tested',scope:'none'}};
  });
}

function save(path,value){mkdirSync(dirname(path),{recursive:true});const next=path+'.next';writeFileSync(next,JSON.stringify(value,null,2)+'\n',{mode:0o600});renameSync(next,path);}
export async function main(args=process.argv.slice(2)){
  const option=name=>{const i=args.indexOf(name);return i<0?null:args[i+1]};
  const output=option('--output');if(!output)throw Error('--output required');
  const base=(option('--base-url')||'https://llm-api.yeutech.cn').replace(/\/$/,'');
  const key=process.env.YEUTECH_CLIENT_KEY||execFileSync('/usr/bin/security',['find-generic-password','-a','codex','-s','cn.yeutech.llm-api','-w'],{encoding:'utf8'}).trim();
  async function request(path,body){
    const started=Date.now();
    try {const r=await fetch(base+path,{method:body?'POST':'GET',headers:{Authorization:'Bearer '+key,'Content-Type':'application/json'},body:body?JSON.stringify(body):undefined,signal:AbortSignal.timeout(body?60000:15000)});
      const text=await r.text();let data;try{data=JSON.parse(text)}catch{data={}};
      return {status:r.status,data,seconds:Math.round((Date.now()-started)/10)/100};
    }catch(e){return {status:0,data:{},seconds:Math.round((Date.now()-started)/10)/100,transportError:e.name==='TimeoutError'?'probe_timeout':'transport_error'};}
  }
  const cap=await request('/v1/model-capabilities');const catalog=await request('/v1/models?client_version=cpa');
  if(cap.status!==200||catalog.status!==200)throw Error('catalog_read_failed');
  const report={schemaVersion:1,observedAt:new Date().toISOString(),baseUrl:base,generation:cap.data.generation,
    sourceCommit:execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim(),sourceDirty:!!execFileSync('git',['status','--porcelain'],{encoding:'utf8'}).trim(),
    boundaries:{productionChanged:false,consumerPoliciesChanged:false,maximumContextLoadTested:false,fastGranted:false},
    models:inspectCatalog(cap.data.data,catalog.data.models)};
  if(option('--policy')){const p=JSON.parse(readFileSync(option('--policy'),'utf8'));report.consumers=Object.fromEntries(Object.entries(p.consumers||{}).map(([id,v])=>[id,{enabled:v.enabled,allowedModelIds:v.allowedModelIds}]));}
  save(output,report);
  const selected=option('--models')?.split(',');
  const blockedProviders=new Set();
  for(const m of report.models){
    if(selected&&!selected.includes(m.id))continue;
    if(!args.includes('--execute-text'))continue;
    if(!m.workloads?.some(w=>['conversation','agent','review'].includes(w))&&!option('--force-text-models')?.split(',').includes(m.id)){m.verification={status:'not_tested',scope:'none',reason:'non_text_or_incomplete_metadata'};continue;}
    if(!m.available){m.verification={status:'not_tested',scope:'none',reason:m.unavailableReason||'route_unavailable'};continue;}
    if(blockedProviders.has(m.provider)){m.verification={status:'not_tested',scope:'none',reason:'provider_authorization_or_quota_failure'};continue;}
    const body={model:m.id,messages:[{role:'user',content:'Reply with exactly YEUTECH_OK and nothing else.'}],max_tokens:32,stream:false};
    if(m.thinking?.levels?.includes('low')){body.reasoning_effort='low';body.max_completion_tokens=256;delete body.max_tokens;}
    const result=await request('/v1/chat/completions',body);
    const choice=result.data.choices?.[0];const text=choice?.message?.content;
    const passed=result.status===200&&typeof text==='string'&&text.trim()==='YEUTECH_OK';
    m.verification={scope:'short_text_chat_completions',observedAt:new Date().toISOString(),httpStatus:result.status,
      status:passed?'passed':result.transportError||(result.status===200?'unexpected_or_incomplete_output':classifyError(result.status,result.data)),
      responseModel:result.data.model??null,seconds:result.seconds,finishReason:choice?.finish_reason??null,
      usage:result.data.usage??null,serviceTier:result.data.service_tier??null,outputMatched:passed,
      syntheticOutput:typeof text==='string'?text.slice(0,500):null};
    if(['authentication_failed','quota_exhausted'].includes(m.verification.status))blockedProviders.add(m.provider);
    save(output,report);console.log(JSON.stringify({id:m.id,...m.verification}));
  }
  report.finishedAt=new Date().toISOString();report.summary={models:report.models.length,metadataGaps:report.models.filter(m=>m.gaps.length).length,
    verification:report.models.reduce((a,m)=>(a[m.verification.status]=(a[m.verification.status]||0)+1,a),{})};
  save(output,report);console.log(JSON.stringify(report.summary));
}
if(process.argv[1]&&import.meta.url===pathToFileURL(resolve(process.argv[1])).href)main().catch(()=>{console.error('Audit failed; no raw credential or provider response was logged.');process.exitCode=1;});
