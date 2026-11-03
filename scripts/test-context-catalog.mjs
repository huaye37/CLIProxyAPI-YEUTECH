import test from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {spawnSync} from 'node:child_process';

const script=readFileSync(new URL('./refresh-yeutech-model-catalog',import.meta.url),'utf8');
const filter=script.split('--argjson policy "$policy_json" \'')[1]?.split('\n\' > "$temp_path"')[0];
assert.ok(filter,'catalog production filter must be located exactly');
function generate(caps,models,allowed=caps.map(m=>m.id)) {
  const result=spawnSync('/usr/bin/jq',['--argjson','bundled',JSON.stringify([{models}]),'--argjson','provider',JSON.stringify([{models}]),'--argjson','policy',JSON.stringify({consumers:{'codex-desktop':{enabled:true,allowedModelIds:allowed}}}),filter],{input:JSON.stringify({data:caps}),encoding:'utf8'});
  assert.equal(result.status,0,result.stderr);
  return JSON.parse(result.stdout).models;
}
const capability=(id,context,type='openai')=>({id,type,owned_by:type,available:true,selectable:true,capability_status:'ready',context_length:context,max_output_tokens:10000,supported_input_modalities:['text'],supported_output_modalities:['text'],supported_workloads:['agent']});
test('actual refresh filter selects route maximum and retains 80 percent compaction',()=>{
  const caps=[capability('gpt-6.1-sol',500000),capability('gpt-small',128000),capability('claude-test',1000000,'claude'),capability('grok-test',2000000,'xai')];
  const models=[{slug:'gpt-6.1-sol',context_window:500000,max_context_window:872000,service_tiers:[{id:'priority'}],supported_reasoning_levels:[{effort:'high'}]},{slug:'gpt-small',context_window:128000,max_context_window:128000}];
  const actual=new Map(generate(caps,models).map(m=>[m.slug,m]));
  for(const [id,context] of [['gpt-6.1-sol',872000],['gpt-small',128000],['claude-test',1000000],['grok-test',2000000]]) {
    assert.equal(actual.get(id).context_window,context);
    assert.equal(actual.get(id).max_context_window,context);
    assert.equal(actual.get(id).auto_compact_token_limit,Math.floor(context*.8));
  }
  assert.deepEqual(actual.get('gpt-6.1-sol').additional_speed_tiers,['fast']);
  assert.equal(actual.get('grok-test').tool_mode,'direct');
});
test('missing maximum falls back to declared capability and policy remains authoritative',()=>{
  const caps=[capability('gpt-future',700000),capability('blocked',1000000)];
  const [model]=generate(caps,[],['gpt-future']);
  assert.equal(model.slug,'gpt-future');assert.equal(model.context_window,700000);
});
