import test from 'node:test';
import assert from 'node:assert/strict';
import {classifyError,inspectCatalog} from './audit-model-capabilities.mjs';
test('failures are classified without echoing upstream secrets',()=>{
  assert.equal(classifyError(429,{message:'model_cooldown secret-token'}),'model_cooldown');
  assert.equal(classifyError(429,{message:'Usage credits are required'}),'credits_required');
  assert.equal(classifyError(429,{message:'frequency limit'}),'rate_limited');
  assert.equal(classifyError(401,{message:'private account path'}),'authentication_failed');
});
test('catalog declaration is not live acceptance and mismatched windows stay visible',()=>{
  const [m]=inspectCatalog([{id:'deepseek-flash',context_length:1048576,max_output_tokens:32768,supported_input_modalities:['text'],supported_output_modalities:['text'],supported_workloads:['conversation'],available:true}], [{slug:'deepseek-flash',max_context_window:272000}]);
  assert.deepEqual(m.gaps,['client_context_mismatch']);assert.equal(m.verification.status,'not_tested');
});
test('incomplete image metadata never gets fabricated text capabilities',()=>{
  const [m]=inspectCatalog([{id:'media',context_length:0}],[]);assert.ok(m.gaps.includes('supported_workloads'));assert.equal(m.context,0);
});
