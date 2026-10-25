package web

import (
	"context"
	"html/template"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/insight"
)

// answerMore is what an answer page shows under its incidents: the comparison with the
// provider's status page, announced maintenance, and where to subscribe.
type answerMore struct {
	ID       string
	Name     string
	Base     string // absolute address for copy-and-paste snippets
	Lead     *answerLead
	Maint    []maintRow
	Pattern  insight.Pattern
	Peaks    *peakView
	Accuracy *insight.Accuracy
}

func (s *Server) answerMore(ctx context.Context, t TargetView, now time.Time) answerMore {
	return answerMore{ID: t.ID, Name: t.Name, Base: s.siteBase(), Pattern: s.pattern(ctx, t.ID, now), Peaks: s.peaks(ctx, t.ID, now), Accuracy: s.accuracyFor(ctx, t.ID), Lead: s.answerLead(ctx, t), Maint: s.maintFor(t.ID, now)}
}

func init() {
	template.Must(answerTmpl.Parse(`{{define "more"}}{{template "pattern" .Pattern}}{{template "accuracyLine" .Accuracy}}{{template "peaks" .Peaks}}{{template "maint" .Maint}}{{template "lead" .Lead}}
<h2 id="subscribe">이 API가 바뀌거나 이상해지면 알림 받기</h2>
<p>장애, 데이터 이상(응답은 오지만 데이터가 멈췄거나 틀림), 응답 형식 변경(필드 추가·사라짐), 공지된 점검을 Atom 피드로 받을 수 있습니다. 가입이나 키는 필요 없습니다.</p>
<ul>
<li>이 API만: <a href="../feed/{{.ID}}.xml">feed/{{.ID}}.xml</a></li>
<li>전체 API: <a href="../feed.xml">feed.xml</a></li>
</ul>
<p class="sub">Slack이라면 채널에서 <code>/feed subscribe</code> 뒤에 피드 주소를 붙이면 됩니다. Feedly 같은 RSS 리더, Discord·Teams의 RSS 연동도 같은 주소를 씁니다. 피드는 5분마다 갱신됩니다.</p>
<h2 id="embed">내 README·블로그에 붙이기</h2>
<p><img src="../badge/{{.ID}}-status.svg" alt="{{.Name}} 상태"> <img src="../badge/{{.ID}}.svg" alt="{{.Name}} 30일 가용률"></p>
<p class="sub">지금 상태 배지 (Markdown)</p>
<pre style="overflow-x:auto;background:#0e1420;border:1px solid var(--line);border-radius:8px;padding:10px;font-size:13px">[![{{.Name}} 상태]({{.Base}}/badge/{{.ID}}-status.svg)]({{.Base}}/s/{{.ID}}.html)</pre>
<p class="sub">30일 가용률 배지 (Markdown)</p>
<pre style="overflow-x:auto;background:#0e1420;border:1px solid var(--line);border-radius:8px;padding:10px;font-size:13px">[![{{.Name}} 30일 가용률]({{.Base}}/badge/{{.ID}}.svg)]({{.Base}}/s/{{.ID}}.html)</pre>
<p class="sub">상태 카드 (HTML, 블로그·사내 위키)</p>
<pre style="overflow-x:auto;background:#0e1420;border:1px solid var(--line);border-radius:8px;padding:10px;font-size:13px">&lt;iframe src="{{.Base}}/w/{{.ID}}.html" width="360" height="170" style="border:0" loading="lazy" title="{{.Name}} 상태"&gt;&lt;/iframe&gt;</pre>
<p class="sub">5분마다 갱신됩니다. 마지막 확인이 10분 넘게 지났으면 배지는 '확인 불가'로 바뀝니다. GitHub README의 이미지는 GitHub가 잠시 캐시해서 몇 분 늦게 바뀔 수 있습니다.</p>{{end}}`))
}
