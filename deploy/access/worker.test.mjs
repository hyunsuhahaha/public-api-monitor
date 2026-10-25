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
  const response=await h.get('/ai/diagnostics.json?token=secret&m=123',{
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
  assert.equal((await failure.get('/ai/diagnostics.json')).status,502);
  assert.equal(failure.writes[0].args[3],502);
  const redirect=harness(async()=>new Response(null,{status:301,headers:{location:'https://hyunsuhahaha.github.io/public-api-monitor/ai/'}}));
  assert.equal((await redirect.get('/ai')).headers.get('location'),'https://gateway.test/ai/');
  const external=harness(async()=>new Response(null,{status:302,headers:{location:'https://evil.test/'}}));
  assert.equal((await external.get('/')).status,502);
  const h=harness();
  assert.equal((await h.get('/',{method:'POST',body:'private'})).status,405);
  assert.equal(h.calls.length,0);
  await h.get('//evil.test/x');
  assert.ok(h.calls[0][0].startsWith('https://hyunsuhahaha.github.io/public-api-monitor/'));
});

test('old /public-api-monitor/ links move to the root; robots.txt points at the sitemap',async()=>{
  const h=harness();
  const moved=await h.get('/public-api-monitor/ai/index.html?x=1');
  assert.equal(moved.status,301);
  assert.equal(moved.headers.get('location'),'/ai/index.html?x=1');
  assert.equal((await h.get('/public-api-monitor')).headers.get('location'),'/');
  assert.match(await (await h.get('/robots.txt')).text(),/Sitemap: https:\/\/gateway\.test\/sitemap\.xml/);
  assert.equal(h.calls.length,0);
});

test('signing in sets an HMAC session cookie that authorizes stats and keeps the owner out of the counts',async()=>{
  const h=harness();
  assert.equal((await h.get('/__access/session',{method:'POST',body:JSON.stringify({token:'wrong'})})).status,401);
  const ok=await h.get('/__access/session',{method:'POST',body:JSON.stringify({token:'private-admin'})});
  assert.equal(ok.status,200);
  const cookie=ok.headers.get('set-cookie');
  assert.match(cookie,/^pamon_admin=[0-9a-f]{64}; Path=\/; Max-Age=\d+; HttpOnly; Secure; SameSite=Strict$/);
  assert.ok(!cookie.includes('private-admin'));
  const session=cookie.split(';')[0];
  assert.equal((await h.get('/__access/stats?range=7d')).status,401);
  const stats=await (await h.get('/__access/stats?range=7d',{headers:{cookie:session}})).json();
  assert.equal(stats.range,'7d');
  assert.equal(stats.bucket,'day');
  assert.equal((await h.get('/__access/stats',{headers:{cookie:'pamon_admin='+'0'.repeat(64)}})).status,401);
  await h.get('/status.html',{headers:{cookie:session,'user-agent':'Mozilla/5.0'}});
  await h.get('/status.html',{headers:{'user-agent':'pamon-selftest'}});
  assert.equal((await h.get('/favicon.ico')).status,204);
  assert.equal(h.writes.length,0);
  await h.get('/status.html',{headers:{'user-agent':'Mozilla/5.0'}});
  assert.equal(h.writes.length,1);
  assert.match((await h.get('/__access/logout',{method:'POST'})).headers.get('set-cookie'),/Max-Age=0/);
});

test('with ORIGIN_URL the site comes from the host at its root, with the gateway header',async()=>{
  const h=harness();
  h.env.ORIGIN_URL='https://1-2-3-4.sslip.io';
  h.env.GATEWAY_SECRET='gw-secret';
  assert.equal((await h.get('/status.html')).status,200);
  assert.equal(h.calls[0][0],'https://1-2-3-4.sslip.io/status.html');
  assert.equal(h.calls[0][1].headers.get('x-pamon-gateway'),'gw-secret');
  const redirect=harness(async()=>new Response(null,{status:301,headers:{location:'https://1-2-3-4.sslip.io/ai/'}}));
  redirect.env.ORIGIN_URL='https://1-2-3-4.sslip.io';
  assert.equal((await redirect.get('/ai')).headers.get('location'),'https://gateway.test/ai/');
  const pages=harness();
  await pages.get('/status.html');
  assert.equal(pages.calls[0][1].headers.get('x-pamon-gateway'),null);
});

test('log storage failure does not break public reads; scheduled retention removes old records',async()=>{
  const h=harness();
  h.env.DB.prepare=()=>({bind(){return {run:async()=>{throw Error('db offline')}}}});
  const old=console.error;let diagnostic='';console.error=(s)=>diagnostic=s;
  try { assert.equal((await h.get('/')).status,200); } finally {console.error=old}
  assert.equal(diagnostic,'access_log_write_failed');
  const retention=harness();
  await retention.worker.scheduled({},retention.env,retention.ctx);
  assert.equal(retention.writes[0].sql,'DELETE FROM access_logs WHERE at < ?');
});
