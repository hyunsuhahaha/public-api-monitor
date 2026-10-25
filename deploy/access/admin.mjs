// The private traffic dashboard. Plain inline HTML/JS (the CSP allows nothing
// external); the inner script avoids backticks, ${ and backslashes so this
// template literal stays a verbatim copy of it.
export const adminHTML = `<!doctype html><html lang="ko"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="robots" content="noindex"><link rel="icon" href="data:,"><title>pamon 접속 통계</title>
<style>
:root{--bg:#0b0f17;--panel:#121826;--line:#1f2a3a;--text:#d8dee9;--muted:#8a96a8;--ai:#a78bfa;--pv:#38bdf8;--auto:#fbbf24;--crawl:#34d399;--bad:#f87171}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px/1.5 system-ui,-apple-system,"Segoe UI","Malgun Gothic",sans-serif}
header{display:flex;align-items:center;gap:12px;flex-wrap:wrap;padding:12px 20px;border-bottom:1px solid var(--line);background:#0e1420;position:sticky;top:0;z-index:2}
header b{font-size:16px}.sub{color:var(--muted)}.spacer{flex:1}
.chips{display:flex;gap:4px}.chip,.btn{background:var(--panel);color:var(--text);border:1px solid var(--line);border-radius:6px;padding:6px 10px;font:inherit;cursor:pointer}
.chip.on{border-color:var(--pv);color:var(--pv)}.btn:hover,.chip:hover{border-color:#33435a}
main{padding:16px 20px;display:grid;gap:16px;grid-template-columns:repeat(4,1fr);max-width:1400px;margin:0 auto}
.panel{background:var(--panel);border:1px solid var(--line);border-radius:8px;padding:14px 16px;min-width:0}
.wide{grid-column:1/-1}.half{grid-column:span 2}
h2{margin:0 0 10px;font-size:13px;font-weight:600;color:var(--muted)}
.card .v{font-size:28px;font-weight:600;font-variant-numeric:tabular-nums}.card .d{font-size:12px;color:var(--muted)}
.card .d.up{color:var(--crawl)}.card .d.down{color:var(--bad)}
.dot{display:inline-block;width:8px;height:8px;border-radius:2px;margin-right:6px;vertical-align:middle}
.legend{display:flex;gap:14px;flex-wrap:wrap;font-size:12px;color:var(--muted);margin-top:6px}
table{width:100%;border-collapse:collapse;font-size:13px}td,th{padding:6px 8px;border-bottom:1px solid var(--line);text-align:left;overflow-wrap:anywhere;vertical-align:top}
th{color:var(--muted);font-weight:500}td.n{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}
.bar{height:6px;border-radius:3px;background:var(--ai);margin-top:4px}
.empty{color:var(--muted);padding:12px 0}
.login{max-width:420px;margin:12vh auto;padding:24px}.login input{width:100%;margin:12px 0;padding:10px;background:var(--bg);color:var(--text);border:1px solid var(--line);border-radius:6px;font:inherit}
.err{color:var(--bad);min-height:20px}.note{color:var(--muted);font-size:12px}
.tools{display:flex;gap:8px;align-items:center;margin-bottom:8px}select{background:var(--bg);color:var(--text);border:1px solid var(--line);border-radius:6px;padding:6px}
svg text{fill:var(--muted);font-size:11px}
@media(max-width:900px){main{grid-template-columns:repeat(2,1fr)}.half{grid-column:1/-1}}
@media(max-width:520px){main{padding:12px;grid-template-columns:1fr}header{padding:10px 12px}.card .v{font-size:24px}}
</style></head><body>
<section id="login" class="panel login" hidden>
  <b>pamon 접속 통계</b>
  <p class="note">관리자 토큰을 한 번 넣으면 이 브라우저는 30일 동안 로그인된 채로 있고, 이 브라우저의 방문은 통계에서 빠집니다.</p>
  <form id="login-form"><input id="token" type="password" autocomplete="off" placeholder="관리자 토큰" aria-label="관리자 토큰" required><button class="btn">로그인</button></form>
  <p class="err" id="login-err" role="alert"></p>
</section>
<div id="app" hidden>
<header>
  <b>pamon 접속 통계</b><span class="sub" id="since"></span><span class="spacer"></span>
  <div class="chips" id="ranges"><button class="chip" data-r="24h">24시간</button><button class="chip" data-r="7d">7일</button><button class="chip" data-r="30d">30일</button></div>
  <button class="btn" id="refresh" title="새로고침">⟳</button><button class="btn" id="logout">로그아웃</button>
</header>
<main>
  <section class="panel card"><h2><span class="dot" style="background:var(--ai)"></span>AI 요청</h2><div class="v" id="c-ai">–</div><div class="d" id="d-ai"></div></section>
  <section class="panel card"><h2><span class="dot" style="background:var(--pv)"></span>사람 페이지뷰</h2><div class="v" id="c-pageviews">–</div><div class="d" id="d-pageviews"></div></section>
  <section class="panel card"><h2><span class="dot" style="background:var(--auto)"></span>스크립트·도구 요청</h2><div class="v" id="c-automation">–</div><div class="d" id="d-automation"></div></section>
  <section class="panel card"><h2><span class="dot" style="background:var(--crawl)"></span>검색엔진 크롤러</h2><div class="v" id="c-crawlers">–</div><div class="d" id="d-crawlers"></div></section>
  <section class="panel wide"><h2 id="chart-title">시간대별 요청</h2><div id="chart"></div>
    <div class="legend"><span><span class="dot" style="background:var(--ai)"></span>AI</span><span><span class="dot" style="background:var(--pv)"></span>사람 페이지뷰</span><span><span class="dot" style="background:var(--auto)"></span>스크립트·도구</span><span><span class="dot" style="background:var(--crawl)"></span>크롤러</span></div></section>
  <section class="panel half"><h2>AI가 가장 많이 읽은 API</h2><div id="targets"></div></section>
  <section class="panel half"><h2>찾아온 AI</h2><div id="agents"></div></section>
  <section class="panel half"><h2>많이 열린 페이지</h2><div id="pages"></div></section>
  <section class="panel half"><h2>국가 · 유입 출처</h2><div id="countries"></div><div id="referrers"></div></section>
  <section class="panel wide"><h2>최근 접속</h2>
    <div class="tools"><select id="kind" aria-label="종류"><option value="">전체</option><option value="ai_claimed">AI</option><option value="browser_claimed">브라우저</option><option value="automation_claimed">스크립트·도구</option><option value="bot_claimed">크롤러</option><option value="unknown">알 수 없음</option></select></div>
    <div style="overflow-x:auto"><table><thead><tr><th>시각 (KST)</th><th>경로</th><th>응답</th><th>클라이언트 (자기 신고)</th><th>국가</th></tr></thead><tbody id="rows"></tbody></table></div>
    <button class="btn" id="more" hidden style="margin-top:10px">이전 기록</button></section>
  <p class="note wide">AI·브라우저·크롤러 구분은 User-Agent의 자기 신고라 신원 확인이 아니고, 요청 수는 사람 수가 아닙니다. 이 주소(Worker)를 거친 요청만 셉니다. 기록은 30일 보관합니다.</p>
</main></div>
<script>
var KINDS={ai_claimed:'AI',browser_claimed:'브라우저',automation_claimed:'스크립트·도구',bot_claimed:'크롤러',unknown:'알 수 없음'};
var GROUPS=[['ai','#a78bfa'],['pageviews','#38bdf8'],['automation','#fbbf24'],['crawlers','#34d399']];
var range='24h', cursor=null, names={}, last=null;
function $(id){return document.getElementById(id)}
function el(tag,text,cls){var e=document.createElement(tag);if(text!=null)e.textContent=text;if(cls)e.className=cls;return e}
function num(n){return Number(n||0).toLocaleString('ko-KR')}
function api(path,opts){return fetch(path,Object.assign({credentials:'same-origin'},opts||{})).then(function(r){if(r.status===401){showLogin();throw Error('auth')}if(!r.ok)throw Error('http '+r.status);return r.json()})}
function showLogin(){$('app').hidden=true;$('login').hidden=false;$('token').focus()}
function showApp(){$('login').hidden=true;$('app').hidden=false}
function kst(iso){var d=new Date(Date.parse(iso)+9*3600000);return d.toISOString().slice(0,19).replace('T',' ')}
function buckets(){var out=[],now=Date.now()+9*3600000;
  if(range==='24h'){for(var i=23;i>=0;i--)out.push(new Date(now-i*3600000).toISOString().slice(0,13))}
  else{var n=range==='7d'?7:30;for(var j=n-1;j>=0;j--)out.push(new Date(now-j*86400000).toISOString().slice(0,10))}
  return out}
function delta(cur,prev){if(!prev&&!cur)return '이전 기간과 같음';if(!prev)return '이전 기간 0';var p=Math.round((cur-prev)/prev*100);return (p>=0?'▲ ':'▼ ')+Math.abs(p)+'% (이전 '+num(prev)+')'}
function cards(s){GROUPS.forEach(function(g){var k=g[0],c=Number(s.current[k]||0),p=Number(s.previous[k]||0);$('c-'+k).textContent=num(c);var d=$('d-'+k);d.textContent=delta(c,p);d.className='d '+(c>p?'up':c<p?'down':'')})}
function chart(s){var keys=buckets(),by={};(s.series||[]).forEach(function(r){by[r.bucket]=r});
  var W=Math.max(320,$('chart').clientWidth||800),H=200,L=36,B=22,T=8,bw=(W-L)/keys.length,max=1;
  keys.forEach(function(k){var r=by[k]||{},t=0;GROUPS.forEach(function(g){t+=Number(r[g[0]]||0)});if(t>max)max=t});
  var NS='http://www.w3.org/2000/svg',svg=document.createElementNS(NS,'svg');svg.setAttribute('viewBox','0 0 '+W+' '+H);svg.setAttribute('width','100%');svg.setAttribute('height',H);
  for(var i=0;i<=3;i++){var y=T+(H-T-B)*i/3,ln=document.createElementNS(NS,'line');ln.setAttribute('x1',L);ln.setAttribute('x2',W);ln.setAttribute('y1',y);ln.setAttribute('y2',y);ln.setAttribute('stroke','#1f2a3a');svg.appendChild(ln);
    var tx=document.createElementNS(NS,'text');tx.setAttribute('x',L-6);tx.setAttribute('y',y+4);tx.setAttribute('text-anchor','end');tx.textContent=Math.round(max*(3-i)/3);svg.appendChild(tx)}
  keys.forEach(function(k,i){var r=by[k]||{},y=H-B,x=L+i*bw+1,g=document.createElementNS(NS,'g'),tip=document.createElementNS(NS,'title'),parts=[];
    GROUPS.forEach(function(gr){var v=Number(r[gr[0]]||0);if(!v)return;var h=(H-T-B)*v/max,rc=document.createElementNS(NS,'rect');y-=h;rc.setAttribute('x',x);rc.setAttribute('y',y);rc.setAttribute('width',Math.max(1,bw-2));rc.setAttribute('height',h);rc.setAttribute('fill',gr[1]);rc.setAttribute('rx',1);g.appendChild(rc);parts.push(gr[0]+' '+v)});
    var hit=document.createElementNS(NS,'rect');hit.setAttribute('x',x);hit.setAttribute('y',T);hit.setAttribute('width',Math.max(1,bw-2));hit.setAttribute('height',H-T-B);hit.setAttribute('fill','transparent');g.appendChild(hit);
    tip.textContent=(range==='24h'?k.slice(5).replace('T',' ')+'시':k.slice(5))+'  '+(parts.join(' · ')||'요청 없음');g.appendChild(tip);svg.appendChild(g);
    var every=range==='30d'?5:range==='7d'?1:3;if(i%every===0){var lb=document.createElementNS(NS,'text');lb.setAttribute('x',x+bw/2);lb.setAttribute('y',H-6);lb.setAttribute('text-anchor','middle');lb.textContent=range==='24h'?k.slice(11)+'시':k.slice(5);svg.appendChild(lb)}});
  $('chart').replaceChildren(svg);$('chart-title').textContent=range==='24h'?'시간대별 요청 (KST)':'일별 요청 (KST)'}
function list(id,rows,label,value,empty,extra){var box=$(id);box.replaceChildren();if(!rows.length){box.append(el('div',empty,'empty'));return}
  var max=Math.max.apply(null,rows.map(value)),t=el('table');rows.forEach(function(r){var tr=el('tr'),a=el('td'),b=el('td',num(value(r)),'n');a.append(el('div',label(r)));var x=extra&&extra(r);if(x)a.append(el('div',x,'note'));var bar=el('div',null,'bar');bar.style.width=Math.max(2,value(r)/max*100)+'%';a.append(bar);tr.append(a,b);t.append(tr)});box.append(t)}
function targetName(path){var id=path.replace('/ai/targets/','').replace('.json','');return names[id]||id}
function render(s){last=s;$('since').textContent='· '+kst(s.since).slice(0,16)+' 이후';cards(s);chart(s);
  list('targets',s.targets,function(r){return targetName(r.path)},function(r){return Number(r.requests)},'아직 AI가 읽어 간 API 진단 파일이 없습니다.',function(r){return 'AI '+num(r.ai)+'회'});
  list('agents',s.agents,function(r){return r.agent},function(r){return Number(r.requests)},'아직 AI 방문이 없습니다.');
  list('pages',s.pages,function(r){return r.path},function(r){return Number(r.requests)},'기록 없음',function(r){return Number(r.ai)?'그중 AI '+num(r.ai)+'회':''});
  list('countries',s.countries,function(r){return r.country},function(r){return Number(r.requests)},'기록 없음');
  var rf=$('referrers');rf.replaceChildren();if(s.referrers.length){rf.append(el('h2','유입 출처'));var t=el('table');s.referrers.forEach(function(r){var tr=el('tr');tr.append(el('td',r.referrer),el('td',num(r.requests),'n'));t.append(tr)});rf.append(t)}}
function loadStats(){return api('/__access/stats?range='+range).then(function(s){showApp();render(s)})}
function loadLogs(more){var q='limit=50&kind='+encodeURIComponent($('kind').value)+(more&&cursor?'&before='+cursor:'');
  return api('/__access/logs?'+q).then(function(d){if(!more)$('rows').replaceChildren();
    if(!d.logs.length&&!more){var tr=el('tr'),td=el('td','기록 없음','empty');td.colSpan=5;tr.append(td);$('rows').append(tr)}
    d.logs.forEach(function(r){var tr=el('tr');tr.append(el('td',kst(r.at)),el('td',r.method+' '+r.path),el('td',r.status+' · '+r.duration_ms+'ms','n'));
      var who=el('td');who.append(el('div',(KINDS[r.kind]||r.kind)+' · '+r.agent),el('div',r.user_agent,'note'));tr.append(who,el('td',r.country+(r.referrer?' · '+r.referrer:'')));$('rows').append(tr)});
    cursor=d.nextBefore;$('more').hidden=!cursor})}
function loadAll(){document.querySelectorAll('#ranges .chip').forEach(function(c){c.classList.toggle('on',c.dataset.r===range)});
  return Promise.all([loadStats(),loadLogs(false)]).catch(function(e){if(e.message!=='auth')$('since').textContent='· 불러오기 실패 ('+e.message+')'})}
fetch('/ai/diagnostics.json').then(function(r){return r.json()}).then(function(d){(d.targets||[]).forEach(function(t){names[t.id]=t.name+' · '+t.provider})}).catch(function(){}).then(loadAll);
$('login-form').onsubmit=function(e){e.preventDefault();$('login-err').textContent='';
  api('/__access/session',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({token:$('token').value})}).then(function(){$('token').value='';return loadAll()}).catch(function(err){$('login-err').textContent=err.message==='auth'?'토큰이 맞지 않습니다':'로그인할 수 없습니다'})};
$('logout').onclick=function(){api('/__access/logout',{method:'POST'}).then(showLogin)};
$('refresh').onclick=loadAll;$('kind').onchange=function(){loadLogs(false)};$('more').onclick=function(){loadLogs(true)};
document.querySelectorAll('#ranges .chip').forEach(function(c){c.onclick=function(){range=c.dataset.r;loadAll()}});
new ResizeObserver(function(){if(last&&!$('app').hidden)chart(last)}).observe($('chart'));
setInterval(function(){if(!$('app').hidden)loadAll()},60000);
</script></body></html>`;
