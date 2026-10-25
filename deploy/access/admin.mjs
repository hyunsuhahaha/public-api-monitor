export const adminHTML = `<!doctype html><html lang="ko"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>접속 기록</title>
<style>body{font:16px system-ui;margin:32px;color:#182330}input,select,button{font:inherit;padding:8px}table{border-collapse:collapse;width:100%;font-size:13px}td,th{padding:8px;border-bottom:1px solid #ddd;text-align:left;overflow-wrap:anywhere}#message{white-space:pre-wrap}form{display:flex;gap:8px;flex-wrap:wrap}</style>
<h1>접속 기록</h1><p>이 입구를 거친 요청만 표시합니다. AI·봇·브라우저 표시는 User-Agent의 자기 신고이며 신원 확인 결과가 아닙니다. 요청 수는 사람 수와 다릅니다.</p>
<form id="form"><input id="token" type="password" autocomplete="off" placeholder="관리자 토큰" required aria-label="관리자 토큰"><select id="kind" aria-label="종류"><option value="">전체</option><option>ai_claimed</option><option>bot_claimed</option><option>browser_claimed</option><option>automation_claimed</option><option>unknown</option></select><button>조회</button></form>
<p id="message" role="status"></p><pre id="summary"></pre><table><thead><tr><th>시각</th><th>경로·종류</th><th>응답</th><th>클라이언트 신고</th><th>국가·유입 출처</th></tr></thead><tbody id="rows"></tbody></table><button id="more" hidden>이전 기록</button>
<script>
let cursor=null;
async function load(more){
  document.querySelector('#message').textContent='조회 중';
  const q=new URLSearchParams({kind:document.querySelector('#kind').value});
  if(more&&cursor) q.set('before',cursor);
  try {
    const response=await fetch('/__access/logs?'+q,{headers:{Authorization:'Bearer '+document.querySelector('#token').value}});
    if(!response.ok) throw Error(response.status===401?'관리자 토큰을 확인하세요':'기록을 가져올 수 없습니다');
    const data=await response.json();
    if(!more)document.querySelector('#rows').replaceChildren();
    for(const row of data.logs){
      const tr=document.createElement('tr');
      for(const text of [row.at,row.method+' '+row.path+' ['+row.resource+']',row.status+' / '+row.duration_ms+'ms',row.kind+' / '+row.agent+' / '+row.user_agent,row.country+' / '+row.referrer]){
        const td=document.createElement('td');td.textContent=text;tr.append(td);
      }
      document.querySelector('#rows').append(tr);
    }
    document.querySelector('#summary').textContent='최근 24시간 요청 수\\n'+data.last24Hours.map(x=>x.kind+' / '+x.resource+': '+x.requests).join('\\n');
    cursor=data.nextBefore;document.querySelector('#more').hidden=!cursor;
    document.querySelector('#message').textContent=data.logs.length?'조회 완료':'기록 없음';
  }catch(error){document.querySelector('#message').textContent=error.message;}
}
document.querySelector('#form').onsubmit=e=>{e.preventDefault();load(false)};
document.querySelector('#more').onclick=()=>load(true);
</script></html>`;
