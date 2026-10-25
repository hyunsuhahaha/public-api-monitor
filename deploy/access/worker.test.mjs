import test from 'node:test';
import assert from 'node:assert/strict';
import { createWorker, classify } from './worker.mjs';

function harness(origin=async()=>new Response('hello',{headers:{'content-type':'text/plain'}})) {
  const writes=[], tasks=[], calls=[];
  const env={ACCESS_ADMIN_TOKEN:'private-admin',DB:{
    prepare(sql) {
      return {bind(...args) {
        return {
          async run() { writes.push({sql,args});return {} },
          async all() { return {results:sql.includes('COUNT')?[]:[{id:7,path:'/ai/diagnostics.json'}]} }
        };
      }};
    }
  }};
  const worker=createWorker(async(...args)=>{calls.push(args);return origin(...args)});
  const ctx={waitUntil(p){tasks.push(p)}};
  return {env,worker,ctx,writes,calls,async get(path,options={}){const response=await worker.fetch(new Request('https://gateway.test'+path,options),env,ctx);await Promise.all(tasks);return response}};
}

test('JSON requests without JS are proxied and stored without query or credential headers',async()=>{
  const h=harness();
  const response=await h.get('/public-api-monitor/ai/diagnostics.json?token=secret&m=123',{
    headers:{'user-agent':'ChatGPT-User/1.0',authorization:'Bearer secret',cookie:'session=secret',referer:'https://site.test/path?key=secret'}});
  assert.equal(response.status,200);
  assert.equal(response.headers.get('cache-control'),'no-store');
  assert.equal(h.calls[0][0],'https://hyunsuhahaha.github.io/public-api-monitor/ai/diagnostics.json?m=123');
  assert.equal(h.calls[0][1].headers.get('authorization'),null);
  assert.equal(h.calls[0][1].headers.get('cookie'),null);
  assert.equal(h.writes.length,1);
  assert.equal(h.writes[0].args[5],'ai_claimed');
  assert.equal(h.writes[0].args[9],'https://site.test');
  assert.equal(h.writes[0].args[10],'ai_json');
  assert.ok(!JSON.stringify(h.writes).includes('secret'));
});

test('AI vs browser labels are claims, not verified identities',()=>{
  assert.equal(classify('Mozilla/5.0').kind,'browser_claimed');
  assert.equal(classify('ClaudeBot/1.0').kind,'ai_claimed');
  assert.equal(classify('bingbot/2.0').kind,'bot_claimed');
  assert.equal(classify('curl/8').kind,'automation_claimed');
  assert.equal(classify('').kind,'unknown');
});

test('private logs fail closed, are not themselves recorded, and paginate',async()=>{
  const h=harness();
  assert.equal((await h.get('/__access/logs')).status,401);
  assert.equal((await h.get('/__access/logs',{headers:{authorization:'Bearer wrong'}})).status,401);
  const data=await (await h.get('/__access/logs?before=8&kind=ai_claimed',{headers:{authorization:'Bearer private-admin'}})).json();
  assert.equal(data.nextBefore,7);
  assert.equal(h.writes.length,0);
  h.env.ACCESS_ADMIN_TOKEN='';
  assert.equal((await h.get('/__access/logs',{headers:{authorization:'Bearer private-admin'}})).status,401);
});

test('origin errors, redirects and writes remain bounded to the Pages origin',async()=>{
  const failure=harness(async()=>{throw Error('secret exception')});
  assert.equal((await failure.get('/public-api-monitor/ai/diagnostics.json')).status,502);
  assert.equal(failure.writes[0].args[3],502);
  const redirect=harness(async()=>new Response(null,{status:301,headers:{location:'https://hyunsuhahaha.github.io/public-api-monitor/ai/'}}));
  assert.equal((await redirect.get('/public-api-monitor/ai')).headers.get('location'),'https://gateway.test/public-api-monitor/ai/');
  const external=harness(async()=>new Response(null,{status:302,headers:{location:'https://evil.test/'}}));
  assert.equal((await external.get('/public-api-monitor/')).status,502);
  const h=harness();
  assert.equal((await h.get('/public-api-monitor/',{method:'POST',body:'private'})).status,405);
  assert.equal((await h.get('/https://evil.test')).status,404);
  assert.equal(h.calls.length,0);
});

test('log storage failure does not break public reads; scheduled retention removes old records',async()=>{
  const h=harness();
  h.env.DB.prepare=()=>({bind(){return {run:async()=>{throw Error('db offline')}}}});
  const old=console.error;let diagnostic='';console.error=(s)=>diagnostic=s;
  try { assert.equal((await h.get('/public-api-monitor/')).status,200); } finally {console.error=old}
  assert.equal(diagnostic,'access_log_write_failed');
  const retention=harness();
  await retention.worker.scheduled({},retention.env,retention.ctx);
  assert.equal(retention.writes[0].sql,'DELETE FROM access_logs WHERE at < ?');
});
