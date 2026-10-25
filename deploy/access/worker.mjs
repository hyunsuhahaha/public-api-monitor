import { adminHTML } from './admin.mjs';

const ORIGIN = 'https://hyunsuhahaha.github.io';
const PREFIX = '/public-api-monitor/';
const headers = { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store', 'x-content-type-options': 'nosniff' };
const json = (data, status=200) => new Response(JSON.stringify(data), {status, headers});

export function classify(ua) {
  const ai = /GPTBot|OAI-SearchBot|ChatGPT-User|ChatGPT Agent|ClaudeBot|Claude-User|Claude-SearchBot|Claude-Code|anthropic-ai|PerplexityBot|Perplexity-User|Google-CloudVertexBot|Gemini|MistralAI-User|Meta-ExternalAgent|Meta-ExternalFetcher|DuckAssistBot|cohere-ai|Cursor|YouBot|Bytespider|CCBot|Amazonbot/i.exec(ua);
  if (ai) return { kind:'ai_claimed', agent:ai[0].toLowerCase() };
  const bot = /Googlebot|bingbot|DuckDuckBot|YandexBot|Baiduspider|Applebot|crawler|spider|bot/i.exec(ua);
  if (bot) return { kind:'bot_claimed', agent:bot[0].toLowerCase() };
  if (/curl|wget|python-requests|httpx|Go-http-client|node|undici/i.test(ua)) return {kind:'automation_claimed', agent:'http-client'};
  if (/Mozilla\//i.test(ua)) return {kind:'browser_claimed', agent:'browser'};
  return {kind:'unknown', agent:'unknown'};
}

function resource(path) {
  if (/\/ai\/.*\.json$/.test(path)) return 'ai_json';
  if (path.endsWith('/llms.txt')) return 'discovery';
  if (/\/data\/.*\.json$/.test(path)) return 'data_json';
  if (path.endsWith('/') || path.endsWith('.html')) return 'page';
  return 'asset_or_other';
}

async function record(request, env, response, start) {
  const url = new URL(request.url);
  const ua = (request.headers.get('user-agent') || '').slice(0,512);
  const who = classify(ua);
  let referrer = '';
  try { referrer = new URL(request.headers.get('referer')).origin; } catch {}
  await env.DB.prepare(`INSERT INTO access_logs (at, method, path, status, duration_ms, kind, agent, user_agent, country, referrer, resource) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
    .bind(new Date().toISOString(), request.method, url.pathname.slice(0,512), response.status, Date.now()-start,
      who.kind, who.agent, ua, request.cf?.country || '', referrer, resource(url.pathname)).run();
}

async function authorized(request, token) {
  if (!token || !request.headers.get('authorization')?.startsWith('Bearer ')) return false;
  const digest = async s => new Uint8Array(await crypto.subtle.digest('SHA-256',new TextEncoder().encode(s)));
  const [a,b] = await Promise.all([digest(request.headers.get('authorization').slice(7)),digest(token)]);
  let diff=0; for(let i=0;i<a.length;i++) diff |= a[i]^b[i];
  return diff===0;
}

async function logs(request, env) {
  if (!await authorized(request,env.ACCESS_ADMIN_TOKEN)) return json({error:'unauthorized'},401);
  const u=new URL(request.url);
  const limit=Math.min(200,Math.max(1,Number(u.searchParams.get('limit'))||50));
  const before=Number(u.searchParams.get('before'))||Number.MAX_SAFE_INTEGER;
  const kind=u.searchParams.get('kind')||'';
  const result=await env.DB.prepare('SELECT * FROM access_logs WHERE id < ? AND (? = \'\' OR kind = ?) ORDER BY id DESC LIMIT ?')
    .bind(before,kind,kind,Math.floor(limit)).all();
  const summary=await env.DB.prepare('SELECT kind, resource, COUNT(*) AS requests FROM access_logs WHERE at >= ? GROUP BY kind, resource')
    .bind(new Date(Date.now()-86400000).toISOString()).all();
  return json({logs:result.results, nextBefore:result.results?.at(-1)?.id ?? null, last24Hours:summary.results,
    classification:'User-Agent claims only; neither verified identity nor unique people. Direct github.io requests are not included.'});
}

export function createWorker(originFetch=fetch) {
  return {
    async fetch(request,env,ctx) {
      const start=Date.now(), u=new URL(request.url);
      if (u.pathname==='/__access' || u.pathname==='/__access/') {
        return new Response(adminHTML,{headers:{'content-type':'text/html; charset=utf-8','cache-control':'no-store',
          'content-security-policy':"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'",'referrer-policy':'no-referrer'}});
      }
      if (u.pathname.startsWith('/__access/')) {
        if (u.pathname!=='/__access/logs') return json({error:'not found'},404);
        if(request.method!=='GET') return json({error:'method not allowed'},405);
        try { return await logs(request,env); } catch { return json({error:'log storage unavailable'},503); }
      }
      let response;
      if (!['GET','HEAD'].includes(request.method)) response=json({error:'method not allowed'},405);
      else if (u.pathname==='/robots.txt') {
        // Crawlers and AI fetchers are welcome everywhere but the private log screen.
        response=new Response(`User-agent: *\nDisallow: /__access\nAllow: /\n\nSitemap: ${u.origin}/sitemap.xml\n`,
          {headers:{'content-type':'text/plain; charset=utf-8','cache-control':'no-store'}});
      }
      // The gateway serves the site at its root; old /public-api-monitor/ links move there.
      else if (u.pathname.startsWith(PREFIX) || u.pathname===PREFIX.slice(0,-1)) {
        response=new Response(null,{status:301,headers:{location:'/'+u.pathname.slice(PREFIX.length)+u.search}});
      }
      else {
        // Fixed origin and prefix: no user-controlled proxy destination, cookies or credentials.
        const upstream=new URL(ORIGIN+PREFIX+u.pathname.slice(1));        if (u.searchParams.has('m')) upstream.searchParams.set('m',u.searchParams.get('m').slice(0,32));
        const pass=new Headers();
        for (const key of ['accept','if-none-match','if-modified-since']) {
          if(request.headers.has(key)) pass.set(key,request.headers.get(key));
        }
        try {
          const result=await originFetch(upstream.toString(),{method:request.method,headers:pass,redirect:'manual'});
          response=new Response(result.body,result);
          if(result.status>=300 && result.status<400 && result.headers.has('location')) {
            const next=new URL(result.headers.get('location'),upstream);
            if(next.origin!==ORIGIN || !next.pathname.startsWith(PREFIX)) response=json({error:'unexpected origin redirect'},502);
            else { next.host=u.host; next.protocol=u.protocol; next.pathname='/'+next.pathname.slice(PREFIX.length); response.headers.set('location',next.toString()); }
          }
          // A gateway hit must reach the Worker to be counted, including conditional 304s.
          response.headers.set('cache-control','no-store');
          response.headers.delete('set-cookie');
        } catch { response=json({error:'origin unavailable'},502); }
      }
      ctx.waitUntil(record(request,env,response,start).catch(()=>console.error('access_log_write_failed')));
      return response;
    },
    async scheduled(_event,env,ctx) {
      ctx.waitUntil(env.DB.prepare('DELETE FROM access_logs WHERE at < ?').bind(new Date(Date.now()-30*86400000).toISOString()).run());
    }
  };
}

export default createWorker();
