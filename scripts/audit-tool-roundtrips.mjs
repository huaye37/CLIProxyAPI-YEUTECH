import {execFileSync} from 'node:child_process';
import {readFileSync,writeFileSync} from 'node:fs';
import {classifyError} from './audit-model-capabilities.mjs';
const key=execFileSync('security',['find-generic-password','-a','codex','-s','cn.yeutech.llm-api','-w'],{encoding:'utf8'}).trim();
const catalog=JSON.parse(readFileSync('docs/audits/2026-10-17-model-availability.json'));
const selected=process.argv.find(x=>x.startsWith('--models='))?.slice(9).split(',');
const automatic=process.argv.includes('--auto');
const output=automatic?'docs/audits/2026-10-17-tool-auto-roundtrips.json':'docs/audits/2026-10-17-tool-roundtrips.json';
const models=catalog.models.filter(m=>m.workloads.includes('agent')&&!['gpt-5.5','grok-3-mini','grok-3-mini-fast'].includes(m.id)&&(!selected||selected.includes(m.id)));
const report={observedAt:new Date().toISOString(),scope:'chat_completions_function_call_and_continuation',results:[]};
async function call(body){try{const r=await fetch('https://llm-api.yeutech.cn/v1/chat/completions',{method:'POST',headers:{Authorization:'Bearer '+key,'Content-Type':'application/json'},body:JSON.stringify(body),signal:AbortSignal.timeout(45000)});return {status:r.status,data:await r.json()};}catch(e){return {status:0,data:{},error:e.name};}}
for(const m of models){
 const started=Date.now(), row={id:m.id,status:'not_tested'};
 const messages=[{role:'user',content:'Use the add_numbers tool to calculate 2 plus 3. Then tell me the result briefly.'}];
 const options={model:m.id,max_tokens:256};if(m.thinking?.levels?.includes('low'))options.reasoning_effort='low';
 const tool={type:'function',function:{name:'add_numbers',description:'Add two integers.',parameters:{type:'object',properties:{a:{type:'integer'},b:{type:'integer'}},required:['a','b'],additionalProperties:false}}};
 const first=await call({...options,messages,tools:[tool],tool_choice:automatic?'auto':{type:'function',function:{name:'add_numbers'}}});
 row.httpStatus=first.status;row.responseModel=first.data.model??null;
 if(first.status!==200){row.status=classifyError(first.status,first.data);}else{
  const assistant=first.data.choices?.[0]?.message, t=assistant?.tool_calls?.[0];let args;try{args=JSON.parse(t?.function?.arguments)}catch{}
  if(t?.function?.name!=='add_numbers'||args?.a!==2||args?.b!==3){row.status='tool_call_mismatch';}
  else{
   const second=await call({...options,messages:[...messages,assistant,{role:'tool',tool_call_id:t.id,content:'5'}],tools:[tool],tool_choice:automatic?'auto':'none'});
   const text=second.data.choices?.[0]?.message?.content||'';row.continuationStatus=second.status;row.serviceTier=second.data.service_tier??null;row.outputContainsResult=typeof text==='string'&&/\b5\b/.test(text);
   row.status=second.status===200&&row.outputContainsResult?'passed_tool_roundtrip':'continuation_failed';
  }
 }
 row.toolChoiceMode=automatic?'auto':'forced_then_none';row.seconds=Math.round((Date.now()-started)/10)/100;report.results.push(row);writeFileSync(output,JSON.stringify(report,null,2)+'\n',{mode:0o600});console.log(JSON.stringify(row));
 if(['authentication_failed','quota_exhausted'].includes(row.status))break;
}
