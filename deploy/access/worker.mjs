import { adminHTML } from './admin.mjs';

const ORIGIN = 'https://hyunsuhahaha.github.io';
const PREFIX = '/public-api-monitor/';
// Search-engine ownership files (public by design: the engine fetches them anonymously).
const VERIFY_FILES = {
  '/google63ebdd653ecf9466.html': 'google-site-verification: google63ebdd653ecf9466.html',
};
const SESSION = 'pamon_admin';
const SESSION_DAYS = 30;
const headers = { 'content-type': 'application/json; charset=utf-8', 'cache-control': 'no-store', 'x-content-type-options': 'nosniff' };
const json = (data, status=200, extra={}) => new Response(JSON.stringify(data), {status, headers:{...headers, ...extra}});

export function classify(ua) {
  const ai = /GPTBot|OAI-SearchBot|ChatGPT-User|ChatGPT Agent|ClaudeBot|Claude-User|Claude-SearchBot|Claude-Code|anthropic-ai|PerplexityBot|Perplexity-User|Google-CloudVertexBot|Gemini|MistralAI-User|Meta-ExternalAgent|Meta-ExternalFetcher|DuckAssistBot|cohere-ai|Cursor|YouBot|Bytespider|CCBot|Amazonbot/i.exec(ua);
  if (ai) return { kind:'ai_claimed', agent:ai[0].toLowerCase() };
  const bot = /Googlebot|Google-Site-Verification|Google-InspectionTool|bingbot|Yeti|Daumoa|DuckDuckBot|YandexBot|Baiduspider|Applebot|crawler|spider|bot/i.exec(ua);
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
  // The owner's own browsing (signed-in admin cookie), the deploy self-test and the
  // freshness watchdog are not traffic.
  if (ua === 'pamon-selftest' || ua === 'pamon-watchdog' || await hasSession(request, env.ACCESS_ADMIN_TOKEN)) return;
  const who = classify(ua);
  let referrer = '';
  try { referrer = new URL(request.headers.get('referer')).origin; } catch {}
  await env.DB.prepare(`INSERT INTO access_logs (at, method, path, status, duration_ms, kind, agent, user_agent, country, referrer, resource) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
    .bind(new Date().toISOString(), request.method, url.pathname.slice(0,512), response.status, Date.now()-start,
      who.kind, who.agent, ua, request.cf?.country || '', referrer, resource(url.pathname)).run();
}

const bytes = s => new TextEncoder().encode(s);
function same(a, b) {
  if (a.length !== b.length) return false;
  let diff=0; for(let i=0;i<a.length;i++) diff |= a[i]^b[i];
  return diff===0;
}
const sha = async s => new Uint8Array(await crypto.subtle.digest('SHA-256', bytes(s)));

// The session cookie holds an HMAC of the admin token, never the token itself.
async function sessionValue(token) {
  const key = await crypto.subtle.importKey('raw', bytes(token), {name:'HMAC', hash:'SHA-256'}, false, ['sign']);
  const mac = new Uint8Array(await crypto.subtle.sign('HMAC', key, bytes('pamon-admin-session-v1')));
  return [...mac].map(b => b.toString(16).padStart(2,'0')).join('');
}

async function hasSession(request, token) {
  if (!token) return false;
  const m = /(?:^|;\s*)pamon_admin=([0-9a-f]{64})/.exec(request.headers.get('cookie') || '');
  return !!m && same(bytes(m[1]), bytes(await sessionValue(token)));
}

async function tokenMatches(given, token) {
  if (!token || !given) return false;
  const [a,b] = await Promise.all([sha(given), sha(token)]);
  return same(a,b);
}

async function authorized(request, token) {
  const auth = request.headers.get('authorization') || '';
  if (auth.startsWith('Bearer ')) return tokenMatches(auth.slice(7), token);
  return hasSession(request, token);
}

async function login(request, env) {
  let given = '';
  try { given = String((await request.json()).token || ''); } catch {}
  if (!await tokenMatches(given, env.ACCESS_ADMIN_TOKEN)) return json({error:'unauthorized'},401);
  const cookie = `${SESSION}=${await sessionValue(env.ACCESS_ADMIN_TOKEN)}; Path=/; Max-Age=${SESSION_DAYS*86400}; HttpOnly; Secure; SameSite=Strict`;
  return json({ok:true}, 200, {'set-cookie':cookie});
}

const logout = () => json({ok:true}, 200, {'set-cookie':`${SESSION}=; Path=/; Max-Age=0; HttpOnly; Secure; SameSite=Strict`});

async function logs(request, env) {
  const u=new URL(request.url);
  const limit=Math.min(200,Math.max(1,Number(u.searchParams.get('limit'))||50));
  const before=Number(u.searchParams.get('before'))||Number.MAX_SAFE_INTEGER;
  const kind=u.searchParams.get('kind')||'';
  const result=await env.DB.prepare('SELECT * FROM access_logs WHERE id < ? AND (? = \'\' OR kind = ?) ORDER BY id DESC LIMIT ?')
    .bind(before,kind,kind,Math.floor(limit)).all();
  return json({logs:result.results, nextBefore:result.results?.at(-1)?.id ?? null,
    classification:'User-Agent claims only; neither verified identity nor unique people. Direct github.io requests are not included.'});
}

export const RANGES = {'24h':{ms:86400000, bucket:'%Y-%m-%dT%H'}, '7d':{ms:7*86400000, bucket:'%Y-%m-%d'}, '30d':{ms:30*86400000, bucket:'%Y-%m-%d'}};
// Visitor groups: AI fetches, human page views, scripts, search crawlers.
const GROUPS = `SUM(kind='ai_claimed') AS ai, SUM(kind='browser_claimed' AND resource='page') AS pageviews,
  SUM(kind='automation_claimed') AS automation, SUM(kind='bot_claimed') AS crawlers, COUNT(*) AS requests`;

async function stats(request, env) {
  const u=new URL(request.url);
  const name=Object.hasOwn(RANGES,u.searchParams.get('range')) ? u.searchParams.get('range') : '24h';
  const range=RANGES[name], now=Date.now();
  const since=new Date(now-range.ms).toISOString(), prev=new Date(now-2*range.ms).toISOString(), end=new Date(now).toISOString();
  const q=(sql,...args)=>env.DB.prepare(sql).bind(...args).all().then(r=>r.results||[]);
  const [current, previous, series, agents, targets, pages, countries, referrers] = await Promise.all([
    q(`SELECT ${GROUPS} FROM access_logs WHERE at >= ? AND at < ?`, since, end),
    q(`SELECT ${GROUPS} FROM access_logs WHERE at >= ? AND at < ?`, prev, since),
    q(`SELECT strftime(?, at, '+9 hours') AS bucket, ${GROUPS} FROM access_logs WHERE at >= ? GROUP BY bucket ORDER BY bucket`, range.bucket, since),
    q(`SELECT agent, COUNT(*) AS requests FROM access_logs WHERE at >= ? AND kind='ai_claimed' GROUP BY agent ORDER BY requests DESC LIMIT 10`, since),
    q(`SELECT path, COUNT(*) AS requests, SUM(kind='ai_claimed') AS ai FROM access_logs WHERE at >= ? AND path LIKE '/ai/targets/%' GROUP BY path ORDER BY requests DESC LIMIT 15`, since),
    q(`SELECT path, COUNT(*) AS requests, SUM(kind='ai_claimed') AS ai FROM access_logs WHERE at >= ? AND resource IN ('page','ai_json','discovery') GROUP BY path ORDER BY requests DESC LIMIT 15`, since),
    q(`SELECT country, COUNT(*) AS requests FROM access_logs WHERE at >= ? AND country <> '' GROUP BY country ORDER BY requests DESC LIMIT 10`, since),
    q(`SELECT referrer, COUNT(*) AS requests FROM access_logs WHERE at >= ? AND referrer <> '' AND referrer <> ? GROUP BY referrer ORDER BY requests DESC LIMIT 10`, since, u.origin),
  ]);
  return json({range:name, since, bucket:name==='24h'?'hour':'day', timezone:'Asia/Seoul',
    current:current[0]||{}, previous:previous[0]||{}, series, agents, targets, pages, countries, referrers});
}

const ADMIN_HEADERS = {'content-type':'text/html; charset=utf-8','cache-control':'no-store',
  'content-security-policy':"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
  'referrer-policy':'no-referrer'};

async function admin(request, env) {
  const u=new URL(request.url);
  if (u.pathname==='/__access' || u.pathname==='/__access/') return new Response(adminHTML,{headers:ADMIN_HEADERS});
  const route = request.method+' '+u.pathname;
  try {
    if (route==='POST /__access/session') return await login(request,env);
    if (route==='POST /__access/logout') return logout();
    if (route!=='GET /__access/logs' && route!=='GET /__access/stats') {
      const known = ['/__access/logs','/__access/stats','/__access/session','/__access/logout'].includes(u.pathname);
      return known ? json({error:'method not allowed'},405) : json({error:'not found'},404);
    }
    if (!await authorized(request,env.ACCESS_ADMIN_TOKEN)) return json({error:'unauthorized'},401);
    return u.pathname==='/__access/stats' ? await stats(request,env) : await logs(request,env);
  } catch { return json({error:'log storage unavailable'},503); }
}

export function createWorker(originFetch=fetch) {
  return {
    async fetch(request,env,ctx) {
      const start=Date.now(), u=new URL(request.url);
      if (u.pathname==='/__access' || u.pathname.startsWith('/__access/')) return admin(request,env);
      // Browsers ask for this on every site; answering here keeps it out of the counts.
      if (u.pathname==='/favicon.ico') return new Response(null,{status:204,headers:{'cache-control':'public, max-age=86400'}});
      let response;
      if (!['GET','HEAD'].includes(request.method)) response=json({error:'method not allowed'},405);
      else if (Object.hasOwn(VERIFY_FILES,u.pathname)) {
        response=new Response(VERIFY_FILES[u.pathname],{headers:{'content-type':'text/html; charset=utf-8','cache-control':'no-store'}});
      }
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
        // ORIGIN_URL (the GCP host) replaces GitHub Pages when set; the host only answers
        // requests carrying GATEWAY_SECRET, so it cannot be visited around the counter.
        const origin=env.ORIGIN_URL||ORIGIN, prefix=env.ORIGIN_URL?'/':PREFIX;
        const upstream=new URL(origin+prefix+u.pathname.slice(1));
        if (u.searchParams.has('m')) upstream.searchParams.set('m',u.searchParams.get('m').slice(0,32));
        const pass=new Headers();
        for (const key of ['accept','accept-encoding','if-none-match','if-modified-since']) {
          if(request.headers.has(key)) pass.set(key,request.headers.get(key));
        }
        if (env.ORIGIN_URL && env.GATEWAY_SECRET) pass.set('x-pamon-gateway',env.GATEWAY_SECRET);
        try {
          const result=await originFetch(upstream.toString(),{method:request.method,headers:pass,redirect:'manual'});
          response=new Response(result.body,result);
          if(result.status>=300 && result.status<400 && result.headers.has('location')) {
            const next=new URL(result.headers.get('location'),upstream);
            if(next.origin!==new URL(origin).origin || !next.pathname.startsWith(prefix)) response=json({error:'unexpected origin redirect'},502);
            else { next.host=u.host; next.protocol=u.protocol; next.pathname='/'+next.pathname.slice(prefix.length); response.headers.set('location',next.toString()); }
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
