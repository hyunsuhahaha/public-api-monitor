package web

import (
	"context"
	"html/template"
	"time"
)

// answerMore is what an answer page shows under its incidents: the comparison with the
// provider's status page, announced maintenance, and where to subscribe.
type answerMore struct {
	Lead *answerLead
}

func (s *Server) answerMore(ctx context.Context, t TargetView, now time.Time) answerMore {
	return answerMore{Lead: s.answerLead(ctx, t)}
}

func init() {
	template.Must(answerTmpl.Parse(`{{define "more"}}{{template "lead" .Lead}}{{end}}`))
}
