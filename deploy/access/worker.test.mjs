import test from 'node:test';
import assert from 'node:assert/strict';
import { createWorker, classify, submitIndexNow } from './worker.mjs';

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

// D1's prepare/bind/all/first/run over node:sqlite, with the real schema.
async function sqliteD1() {
  const { DatabaseSync } = await import('node:sqlite');
  const { readFileSync } = await import('node:fs');
  const db = new DatabaseSync(':memory:');
  db.exec(readFileSync(new URL('./schema.sql', import.meta.url), 'utf8'));
  return {db, prepare(sql) { return {bind(...a) { const st=db.prepare(sql); return {
    async all() { return {results: st.all(...a)} }, async first() { return st.get(...a) ?? null }, async run() { st.run(...a); return {} } } } } }};
}

test('"나도 안 돼요" reports: counted once per person per service, no address stored',async()=>{
  const h=harness(), d1=await sqliteD1();
  h.env.DB=d1; h.env.REPORT_SALT='salt';
  const post=(target,ip='203.0.113.9')=>h.get('/__report',{method:'POST',body:JSON.stringify({target}),headers:{'cf-connecting-ip':ip,'user-agent':'Mozilla/5.0'}});
  assert.equal((await post('../etc')).status,400);
  let r=await (await post('hometax')).json();
  assert.equal(r.counted,true); assert.equal(r.counts.hometax.lastHour,1);
  r=await (await post('hometax')).json();
  assert.equal(r.counted,false); assert.equal(r.counts.hometax.lastHour,1);
  r=await (await post('hometax','198.51.100.4')).json();
  assert.equal(r.counts.hometax.lastHour,2);
  const all=await (await h.get('/__report/summary')).json();
  assert.equal(all.counts.hometax.last24h,2);
  const rows=d1.db.prepare('SELECT * FROM reports').all();
  assert.ok(!JSON.stringify(rows).includes('203.0.113.9') && !JSON.stringify(rows).includes('Mozilla'));
  for (let i=0;i<20;i++) await post('svc-'+i,'192.0.2.1');
  assert.equal((await post('one-more','192.0.2.1')).status,429);
  assert.equal(h.calls.length,0);
  assert.equal(d1.db.prepare('SELECT COUNT(*) n FROM access_logs').get().n,0);
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

test('IndexNow: key file is served, and the daily run submits only this host\'s sitemap URLs',async()=>{
  const h=harness();
  h.env.INDEXNOW_KEY='abc123';
  const key=await h.get('/abc123.txt');
  assert.equal(await key.text(),'abc123');
  assert.equal(h.calls.length,0);
  const sent=[];
  const sitemap=async(url,opts)=>{sent.push(['origin',url,opts.headers.get('x-pamon-gateway')]);
    return new Response('<url><loc>https://pub.test/</loc></url><url><loc>https://pub.test/s/a.html</loc></url><url><loc>https://elsewhere.test/x</loc></url>')};
  const env={ORIGIN_URL:'https://host.test',GATEWAY_SECRET:'g',PUBLIC_URL:'https://pub.test',INDEXNOW_KEY:'abc123'};
  const result=await submitIndexNow(env,sitemap,async(url,opts)=>{sent.push([url,JSON.parse(opts.body)]);return new Response(null,{status:202})});
  assert.deepEqual(sent[0],['origin','https://host.test/sitemap.xml','g']);
  assert.equal(result.length,2);
  const body=sent[1][1];
  assert.equal(body.host,'pub.test');
  assert.equal(body.keyLocation,'https://pub.test/abc123.txt');
  assert.deepEqual(body.urlList,['https://pub.test/','https://pub.test/s/a.html']);
});

// --- MCP (/mcp): a client's whole conversation against a fake origin ---
const NOW = Date.now();
const iso = ms => new Date(NOW + ms).toISOString();
const target = (id, name, provider, category, host, observedStatus, reason, extra={}) => ({id, name, provider, category, host, observedStatus, reason,
  checkedAt: iso(-120000), freshUntil: iso(480000), locations: [{location:'kr-seoul', name:'한국 서울', status:observedStatus, checkedAt: iso(-120000), latencyMs: 80}],
  verdict: {tone: observedStatus==='ok'?'ok':'bad', headline: observedStatus==='ok'?'정상입니다':'장애가 있습니다', detail:'상세', yourSide:'안내'},
  recentIncidents: [], retryAdvice: {action: observedStatus==='ok'?'retry':'wait', reason:'r'}, incidentPattern: {summary:'30일 요약'}, scope: ['one function'], ...extra});
const CATALOG = {schemaVersion:'1.1', generatedAt: iso(-60000), targets: [
  target('kakao-login','카카오 로그인 (OpenID 설정)','카카오','로그인·인증','kauth.kakao.com','down','timeout: 서버 응답 대기 10s',
    {official:{provider:'카카오',indicator:'none',description:'All Systems Operational',page:'https://status.kakao.com',fetchedAt:iso(-60000),operational:true,comparison:'발표가 늦어지는 중'},
     recentIncidents:[{id:1,target:'kakao-login',started:iso(-600000),ended:'0001-01-01T00:00:00Z',severity:2,status:'down',reason:'timeout: 서버 응답 대기 10s'}]}),
  target('kakao-map','카카오맵 주소 검색','카카오','지도·위치','dapi.kakao.com','ok',''),
  target('github-api','GitHub 공개 저장소 API','GitHub','소스·배포','api.github.com','slow','slow: 2.1s (usual 300ms)'),
  target('toss-payments','토스페이먼츠 결제 API','토스페이먼츠','은행·결제','api.tosspayments.com','ok','',{checkedAt: iso(-3600000), freshUntil: iso(-3000000)}),
]};

function mcpHarness(origin) {
  const h = harness(origin || (async url => {
    const p = new URL(url).pathname;
    if (p.endsWith('/ai/diagnostics.json')) return Response.json(CATALOG);
    const m = /\/ai\/targets\/(.+)\.json$/.exec(p);
    if (m) { const t = CATALOG.targets.find(x => x.id === m[1]); return t ? Response.json({...CATALOG, targets:[t]}) : new Response('nf', {status:404}); }
    return new Response('nf', {status:404});
  }));
  let session = null, id = 0;
  h.rpc = async (method, params, opts={}) => {
    const body = opts.raw ?? JSON.stringify(opts.notify ? {jsonrpc:'2.0', method, params} : {jsonrpc:'2.0', id: ++id, method, params});
    const headers = {'content-type':'application/json', accept:'application/json, text/event-stream', 'user-agent': opts.ua || 'node', 'mcp-protocol-version':'2025-06-18'};
    if (session) headers['mcp-session-id'] = session;
    const r = await h.get('/mcp', {method:'POST', body, headers});
    if (r.headers.get('mcp-session-id')) session = r.headers.get('mcp-session-id');
    return {status: r.status, headers: r.headers, body: r.status === 202 ? null : await r.json()};
  };
  return h;
}

test('MCP handshake: initialize, initialized, tools/list, then a call logged under the client name', async () => {
  const h = mcpHarness();
  const init = await h.rpc('initialize', {protocolVersion:'2025-06-18', capabilities:{}, clientInfo:{name:'claude-code', version:'2.1.0'}}, {ua:'claude-code/2.1.0'});
  assert.equal(init.status, 200);
  assert.equal(init.body.result.protocolVersion, '2025-06-18');
  assert.equal(init.body.result.serverInfo.name, 'pamon');
  assert.ok(init.body.result.capabilities.tools);
  assert.match(init.body.result.instructions, /check_service/);
  assert.match(init.headers.get('mcp-session-id'), /^[0-9a-f]{16}\.[A-Za-z0-9_-]+$/);
  assert.equal(init.headers.get('access-control-allow-origin'), '*');
  assert.equal((await h.rpc('notifications/initialized', undefined, {notify:true})).status, 202);
  const list = await h.rpc('tools/list', {});
  assert.deepEqual(list.body.result.tools.map(t => t.name), ['check_service', 'list_problems', 'list_services']);
  for (const t of list.body.result.tools) { assert.equal(t.inputSchema.type, 'object'); assert.equal(t.annotations.readOnlyHint, true); }
  const call = await h.rpc('tools/call', {name:'check_service', arguments:{query:'kakao-login'}});
  assert.equal(call.body.result.isError, false);
  // Log rows: kind ai_claimed, agent from initialize (carried by the session id), path names the call.
  const rows = h.writes.map(w => ({path: w.args[2], kind: w.args[5], agent: w.args[6], resource: w.args[10]}));
  assert.equal(rows.length, 4);
  assert.ok(rows.every(r => r.resource === 'mcp' && r.kind === 'ai_claimed' && r.agent === 'mcp:claude-code'));
  assert.equal(rows[0].path, '/mcp initialize');
  assert.equal(rows[3].path, '/mcp tools/call check_service kakao-login');
  assert.equal(await h.rpc('ping', {}).then(r => JSON.stringify(r.body.result)), '{}');
});

test('MCP check_service: matches by id, host, URL, name and provider, with freshness at read time', async () => {
  const h = mcpHarness();
  await h.rpc('initialize', {protocolVersion:'2099-01-01', clientInfo:{name:'x'}}).then(r => assert.equal(r.body.result.protocolVersion, '2025-11-25'));
  const ask = async query => (await h.rpc('tools/call', {name:'check_service', arguments:{query}})).body.result;
  let r = await ask('https://kauth.kakao.com/oauth/token?client_id=abc');
  assert.equal(r.structuredContent.matches.length, 1);
  const d = r.structuredContent.matches[0];
  assert.equal(d.id, 'kakao-login');
  assert.equal(d.state, 'failing');
  assert.match(d.failingStep, /server wait/);
  assert.equal(d.official.operational, true);
  assert.equal(d.recentIncidents[0].ended, null);
  assert.equal(d.evidence.answerPage, 'https://gateway.test/s/kakao-login.html');
  assert.match(r.content[0].text, /FAILURE OBSERVED/);
  assert.match(r.content[0].text, /Official status page: operational/);
  assert.match(r.content[0].text, /ongoing/);
  assert.match(r.content[0].text, /not the provider's official statement/);
  r = await ask('카카오');                       // provider: every function of it
  assert.deepEqual(r.structuredContent.matches.map(m => m.id).sort(), ['kakao-login', 'kakao-map']);
  r = await ask('github');
  assert.equal(r.structuredContent.matches[0].state, 'degraded');
  assert.match(r.structuredContent.matches[0].failingStep, /latency/);
  r = await ask('토스페이먼츠 결제');           // last check expired: unknown, never "ok"
  assert.equal(r.structuredContent.matches[0].state, 'unknown');
  assert.match(r.content[0].text, /UNKNOWN \(observation expired/);
  r = await ask('dapi.kakao.com');
  assert.equal(r.structuredContent.matches[0].id, 'kakao-map');
  r = await ask('stripe');
  assert.equal(r.structuredContent.matches.length, 0);
  assert.match(r.content[0].text, /does not monitor anything matching/);
  const bad = await h.rpc('tools/call', {name:'check_service', arguments:{}});
  assert.equal(bad.body.error.code, -32602);
  // the catalog is read once and kept for a minute
  assert.equal(h.calls.filter(c => c[0].endsWith('/ai/diagnostics.json')).length, 1);
});

test('MCP list_problems and list_services', async () => {
  const h = mcpHarness();
  const call = async (name, args={}) => (await h.rpc('tools/call', {name, arguments:args})).body.result;
  let r = await call('list_problems');
  assert.deepEqual(r.structuredContent.problems.map(p => [p.id, p.state]), [['kakao-login', 'failing'], ['github-api', 'degraded']]);
  assert.equal(r.structuredContent.unknownCount, 1);
  assert.equal(r.structuredContent.monitorBehind, false);
  assert.match(r.content[0].text, /2 of 4 monitored services/);
  r = await call('list_services');
  assert.equal(r.structuredContent.services.length, 4);
  r = await call('list_services', {category:'payments'});
  assert.deepEqual(r.structuredContent.services.map(s => s.id), ['toss-payments']);
  r = await call('list_services', {category:'로그인'});
  assert.deepEqual(r.structuredContent.services.map(s => s.id), ['kakao-login']);
  r = await call('list_services', {category:'nothing'});
  assert.match(r.content[0].text, /Categories: /);
  const unknown = await h.rpc('tools/call', {name:'delete_everything', arguments:{}});
  assert.equal(unknown.body.error.code, -32602);
});

test('MCP protocol edges: bad JSON, unknown method, batches, GET, CORS preflight, origin down', async () => {
  const h = mcpHarness();
  assert.equal((await h.rpc(null, null, {raw:'{not json'})).body.error.code, -32700);
  assert.equal((await h.rpc('resources/read', {})).body.error.code, -32601);
  assert.deepEqual((await h.rpc('prompts/list', {})).body.result, {prompts: []});
  const batch = await h.rpc(null, null, {raw: JSON.stringify([{jsonrpc:'2.0', id:1, method:'ping'}, {jsonrpc:'2.0', method:'notifications/initialized'}, {jsonrpc:'2.0', id:2, method:'tools/list'}])});
  assert.deepEqual(batch.body.map(b => b.id), [1, 2]);
  assert.equal((await h.rpc(null, null, {raw: JSON.stringify({id: 3})})).body.error.code, -32600);
  assert.equal((await h.rpc(null, null, {raw: 'x'.repeat(70000)})).status, 413);
  const get = await h.get('/mcp', {headers:{accept:'text/event-stream'}});
  assert.equal(get.status, 405);
  assert.equal(get.headers.get('allow'), 'POST, OPTIONS');
  const pre = await h.get('/mcp', {method:'OPTIONS'});
  assert.equal(pre.status, 204);
  assert.match(pre.headers.get('access-control-allow-headers'), /mcp-session-id/);
  const down = mcpHarness(async () => { throw Error('boom'); });
  const r = (await down.rpc('tools/call', {name:'list_problems', arguments:{}})).body.result;
  assert.equal(r.isError, true);
  assert.match(r.content[0].text, /says nothing about the service/);
  // with ORIGIN_URL the tools read the GCP host with the gateway header, like the proxy
  const gcp = mcpHarness();
  gcp.env.ORIGIN_URL = 'https://1-2-3-4.sslip.io'; gcp.env.GATEWAY_SECRET = 'gw';
  await gcp.rpc('tools/call', {name:'list_services', arguments:{}});
  assert.equal(gcp.calls[0][0], 'https://1-2-3-4.sslip.io/ai/diagnostics.json');
  assert.equal(gcp.calls[0][1].headers.get('x-pamon-gateway'), 'gw');
});

test('robots.txt names the AI crawlers and keeps only /__access out; .md is served as Markdown', async () => {
  const h = harness(async () => new Response('# x', {headers:{'content-type':'application/octet-stream'}}));
  const text = await (await h.get('/robots.txt')).text();
  for (const a of ['GPTBot','OAI-SearchBot','ChatGPT-User','ClaudeBot','Claude-User','Claude-SearchBot','PerplexityBot','Perplexity-User','Google-Extended'])
    assert.match(text, new RegExp(`User-agent: ${a}\\nDisallow: /__access\\nAllow: /`));
  assert.ok(!/Disallow: \/(?!__access)/.test(text));
  assert.equal((await h.get('/s/kakao-login.md')).headers.get('content-type'), 'text/markdown; charset=utf-8');
});
