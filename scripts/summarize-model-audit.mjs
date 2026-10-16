import {readFileSync,writeFileSync} from 'node:fs';
const root='docs/audits/';
const read=name=>JSON.parse(readFileSync(root+name));
const baseline=read('2026-10-17-model-availability.json');
const follow=read('2026-10-17-followup-observations.json');
const media=read('2026-10-17-media-nas-execution.json');
const additional=read('2026-10-17-additional-channels.json');
const forced=read('2026-10-17-tool-roundtrips.json');
const auto=read('2026-10-17-tool-auto-roundtrips.json');
const recheck=read('2026-10-17-grok-recheck.json');
const result={observedDateShanghai:'2026-10-17',scope:'Every model registered by the live YEUTECH proxy, not just the local desktop',
 productionChanged:false,consumerPoliciesChanged:false,installableCandidatePrepared:false,
 sourceRepairs:['Provider-scoped metadata enrichment shared by standard and Codex model catalogs','No GPT template context substituted for unknown or media models','Opt-in schema=2 for image/video/speech without fictional token windows; strict capability-v1 remains compatible','Management consumer runtime carries applicability and evidence boundaries','BFF accepts v2 without placing media in text-agent selection','Public media path matcher prepared without changing management exposure, upstream selection, credentials or tunnels'],
 remaining:['Merge source deltas into the authoritative manager tree while preserving its existing unresolved UI merge','Build and check exact paired NAS/Tokyo/manager artifacts before user installation','Browser acceptance and save/read/send verification across actual consumer UIs','Independent image plugin still filters out xai; its workload-specific settings and business-route adapter need implementation before enabling Grok images there','Requested model identity remains unresolved for Grok 3 Mini variants and Composer','Grok Multi-Agent custom function-tool contract is not accepted','Maximum-window load, streaming recovery, media editing, video playback and quota attribution were not tested'],
 consumers:baseline.consumers,models:[]};
for(const m of baseline.models){
 const identity=follow.modelIdentityUnresolved.find(x=>x.requested===m.id);
 const ordinary=follow.naturalQuestionRecheck.find(x=>x.id===m.id);
 const mediaResult=media.results.find(x=>x.id===m.id);
 const additionalResult=additional.results.find(x=>x.id===m.id);
 const tool=auto.results.find(x=>x.id===m.id)||forced.results.find(x=>x.id===m.id);
 const composer=recheck.models.find(x=>x.id===m.id&&x.verification.scope!=='none');
 const implementation=m.type==='xai'?'internal/runtime/executor/xai_executor*.go':m.type==='antigravity'?'internal/runtime/executor/antigravity_executor*.go':m.type==='openai'?'internal/runtime/executor/codex_executor*.go':'internal/runtime/executor/openai_compat_executor*.go';
 const evidence={text:ordinary?{status:'passed_natural_question',...ordinary}:composer?.verification||m.verification,tools:tool||{status:'not_tested'},media:mediaResult||null,additional:additionalResult||null};
 const basicPassed=ordinary||m.verification.status==='passed'||mediaResult?.status.startsWith('passed_')||additionalResult?.httpStatus===200;
 result.models.push({id:m.id,provider:m.provider,callChain:m.provider==='tokyo-claude'?'consumer -> NAS proxy -> existing HK SSH relay -> Tokyo Claude':`consumer -> NAS proxy -> ${m.provider}`,
 implementation,metadataImplementation:'internal/registry/capability_metadata.go; internal/api/server_model_capabilities.go',frontendNeeded:!!mediaResult||!!identity,
 context:m.context,outputLimit:m.outputLimit,metadataGaps:m.gaps.filter(x=>!(x==='client_context_mismatch'&&m.id.startsWith('gpt-'))),
 identityMismatch:identity||null,verification:evidence,
 conclusion:m.id==='gpt-5.5'?'不适用：当前ChatGPT账户通道明确不支持；保留历史模型身份':identity?'待完成：回执型号与请求型号不一致':mediaResult?'待完成：内网能力已验证或已取得完成回执，公网和消费者发布未完成':tool&&tool.status!=='passed_tool_roundtrip'?'待完成：短文本成功，自定义工具未通过':basicPassed?'基础文本/工具已验证；不代表最大窗口、速度授权或全消费者验收':'待完成：缺少真实成功证据'});
}
result.summary={registered:result.models.length,identityMismatches:result.models.filter(m=>m.identityMismatch).length,toolRoundtripsPassed:result.models.filter(m=>m.verification.tools.status==='passed_tool_roundtrip').length,imageBytesValidated:media.results.filter(m=>m.status==='passed_image_bytes').length,businessImageReceipts:additional.results.filter(m=>m.scope==='business_image_plugin'&&m.httpStatus===200).length,audioResponsesValidated:media.results.filter(m=>m.status==='passed_audio_response').length,videoCompletedReceipts:additional.results.filter(m=>m.scope==='video_poll'&&m.upstreamStatus==='done'&&m.hasVideoUrl).length};
writeFileSync(root+'2026-10-17-audit-summary.json',JSON.stringify(result,null,2)+'\n',{mode:0o600});console.log(JSON.stringify(result.summary));
