import assert from 'node:assert/strict';
import fs from 'node:fs';
import http from 'node:http';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { spawn } from 'node:child_process';
import test from 'node:test';
import { once } from 'node:events';

async function port() { const server=net.createServer();await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));const value=server.address().port;await new Promise(resolve=>server.close(resolve));return value; }
async function listen(server,value){await new Promise(resolve=>server.listen(value,'127.0.0.1',resolve));}

async function fixture(t, handler) {
  const directory=fs.mkdtempSync(path.join(os.tmpdir(),'yeutech-gateway-lifecycle-'));
  const backend=http.createServer(handler);
  const backendPort=await port(),gatewayPort=await port(),adminPort=await port();
  await listen(backend,backendPort);
  const state=path.join(directory,'active.json');
  fs.writeFileSync(state,JSON.stringify({port:backendPort}));
  const child=spawn(process.execPath,[path.resolve('gateway/proxy-gateway.mjs')],{env:{...process.env,GATEWAY_STATE_FILE:state,GATEWAY_PORT:String(gatewayPort),GATEWAY_ADMIN_PORT:String(adminPort),GATEWAY_BACKEND_PORTS:String(backendPort)},stdio:'ignore'});
  t.after(()=>{child.kill('SIGTERM');backend.closeAllConnections();backend.close();fs.rmSync(directory,{recursive:true,force:true});});
  const status=async()=>await (await fetch(`http://127.0.0.1:${adminPort}/status`)).json();
  for(let i=0;i<100;i++){try{await status();return {backend,backendPort,gatewayPort,status};}catch{await new Promise(r=>setTimeout(r,10));}}
  throw new Error('gateway did not start');
}

test('client disconnect closes upstream and releases accounting before upstream finishes', {timeout:5000}, async(t)=>{
  let closed;
  const f=await fixture(t,(req,res)=>{
    closed=once(res,'close');
    res.writeHead(200,{'content-type':'text/event-stream'});res.write('data: hello\n\n');
  });
  await new Promise((resolve,reject)=>{
    http.get(`http://127.0.0.1:${f.gatewayPort}/stream`,res=>res.once('data',()=>{res.destroy();resolve();})).once('error',reject);
  });
  await closed;
  const state=await f.status();
  assert.equal(state.activeRequests[f.backendPort],0);
  assert.equal(state.totals.disconnected,1);
  assert.equal(state.oldestRequestAgeMs[f.backendPort],0);
});

test('disconnect before response headers cancels upstream without leaking a request', {timeout:5000}, async(t)=>{
  let accepted,closed;
  const arrived=new Promise(r=>accepted=r);
  const f=await fixture(t,(req,res)=>{closed=once(res,'close');accepted();});
  const request=http.get(`http://127.0.0.1:${f.gatewayPort}/waiting`);
  request.on('error',()=>{});
  await arrived;request.destroy();await closed;
  const state=await f.status();assert.equal(state.activeRequests[f.backendPort],0);assert.equal(state.totals.disconnected,1);
});

test('upstream truncated stream is closed instead of appending JSON to SSE', {timeout:5000}, async(t)=>{
  let upstreamResponse;
  const f=await fixture(t,(req,res)=>{upstreamResponse=res;res.writeHead(200,{'content-type':'text/event-stream'});res.write('data: partial\n\n');});
  const content=await new Promise((resolve,reject)=>{
    http.get(`http://127.0.0.1:${f.gatewayPort}/broken`,res=>{
      let text='';res.on('data',chunk=>{text+=chunk;upstreamResponse.destroy();});
      res.once('close',()=>resolve(text));res.on('error',()=>{});
    }).once('error',reject);
  });
  assert.equal(content,'data: partial\n\n');
  const state=await f.status();assert.equal(state.activeRequests[f.backendPort],0);assert.equal(state.totals.upstreamErrors,1);
});

test('websocket disconnect closes its peer and releases accounting', {timeout:5000}, async(t)=>{
  const f=await fixture(t,()=>{});let peer,closed;
  f.backend.on('upgrade',(req,socket)=>{
    peer=socket;closed=once(socket,'close');socket.on('error',()=>{});
    socket.on('end',()=>socket.end());socket.resume();
    socket.write('HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n');
  });
  const client=net.connect(f.gatewayPort,'127.0.0.1');client.on('error',()=>{});
  t.after(()=>{client.destroy();peer?.destroy();});
  await once(client,'connect');client.write('GET /ws HTTP/1.1\r\nHost: localhost\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n');
  await once(client,'data');client.destroy();await closed;
  const state=await f.status();assert.equal(state.activeRequests[f.backendPort],0);
});

test('gateway switches new requests while allowing an old stream to finish', async (t) => {
  const directory=fs.mkdtempSync(path.join(os.tmpdir(),'yeutech-proxy-gateway-'));
  t.after(()=>fs.rmSync(directory,{recursive:true,force:true}));
  const bluePort=await port(),greenPort=await port(),gatewayPort=await port(),adminPort=await port();
  let releaseBlue;
  const blue=http.createServer((req,res)=>{res.writeHead(200);res.write('blue-');releaseBlue=()=>res.end('done');});
  const green=http.createServer((req,res)=>res.end('green'));
  await listen(blue,bluePort);await listen(green,greenPort);
  t.after(()=>blue.close());t.after(()=>green.close());
  const state=path.join(directory,'active.json');fs.writeFileSync(state,JSON.stringify({port:bluePort}));
  const child=spawn(process.execPath,[path.resolve('gateway/proxy-gateway.mjs')],{env:{...process.env,GATEWAY_STATE_FILE:state,GATEWAY_PORT:String(gatewayPort),GATEWAY_ADMIN_PORT:String(adminPort),GATEWAY_BACKEND_PORTS:`${bluePort},${greenPort}`}});
  t.after(()=>child.kill('SIGTERM'));
  await new Promise((resolve,reject)=>{const started=Date.now();const probe=()=>fetch(`http://127.0.0.1:${adminPort}/status`).then(resolve).catch(()=>Date.now()-started>3000?reject(new Error('gateway did not start')):setTimeout(probe,20));probe();});
  const blueResponse=await fetch(`http://127.0.0.1:${gatewayPort}/stream`);
  const reader=blueResponse.body.getReader();const first=await reader.read();assert.equal(Buffer.from(first.value).toString(),'blue-');
  fs.writeFileSync(state+'.next',JSON.stringify({port:greenPort}));fs.renameSync(state+'.next',state);
  assert.equal(await (await fetch(`http://127.0.0.1:${gatewayPort}/new`)).text(),'green');
  const during=await (await fetch(`http://127.0.0.1:${adminPort}/status`)).json();assert.equal(during.activeRequests[String(bluePort)],1);
  releaseBlue();let tail='';for(;;){const part=await reader.read();if(part.done)break;tail+=Buffer.from(part.value).toString();}assert.equal(tail,'done');
  const after=await (await fetch(`http://127.0.0.1:${adminPort}/status`)).json();assert.equal(after.activeRequests[String(bluePort)],0);
});
