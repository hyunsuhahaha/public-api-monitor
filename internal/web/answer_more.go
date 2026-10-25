package web

import (
	"context"
	"html/template"
	"time"
)

// answerMore is what an answer page shows under its incidents: the comparison with the
// provider's status page, announced maintenance, and where to subscribe.
type answerMore struct {
	ID    string
	Lead  *answerLead
	Maint []maintRow
}

func (s *Server) answerMore(ctx context.Context, t TargetView, now time.Time) answerMore {
	return answerMore{ID: t.ID, Lead: s.answerLead(ctx, t), Maint: s.maintFor(t.ID, now)}
}

func init() {
	template.Must(answerTmpl.Parse(`{{define "more"}}{{template "maint" .Maint}}{{template "lead" .Lead}}
<h2 id="subscribe">이 API가 바뀌거나 이상해지면 알림 받기</h2>
<p>장애, 데이터 이상(응답은 오지만 데이터가 멈췄거나 틀림), 응답 형식 변경(필드 추가·사라짐), 공지된 점검을 Atom 피드로 받을 수 있습니다. 가입이나 키는 필요 없습니다.</p>
<ul>
<li>이 API만: <a href="../feed/{{.ID}}.xml">feed/{{.ID}}.xml</a></li>
<li>전체 API: <a href="../feed.xml">feed.xml</a></li>
</ul>
<p class="sub">Slack이라면 채널에서 <code>/feed subscribe</code> 뒤에 피드 주소를 붙이면 됩니다. Feedly 같은 RSS 리더, Discord·Teams의 RSS 연동도 같은 주소를 씁니다. 피드는 5분마다 갱신됩니다.</p>{{end}}`))
}
