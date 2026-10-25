package web

import (
	"context"
	"html/template"
	"time"
)

// answerMore is what an answer page shows under its incidents: the comparison with the
// provider's status page, announced maintenance, and where to subscribe.
type answerMore struct {
	Lead  *answerLead
	Maint []maintRow
}

func (s *Server) answerMore(ctx context.Context, t TargetView, now time.Time) answerMore {
	return answerMore{Lead: s.answerLead(ctx, t), Maint: s.maintFor(t.ID, now)}
}

func init() {
	template.Must(answerTmpl.Parse(`{{define "more"}}{{template "maint" .Maint}}{{template "lead" .Lead}}{{end}}`))
}
