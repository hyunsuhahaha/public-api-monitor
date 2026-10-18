package web

import (
	"net/http"
	"strconv"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
)

func (s *Server) target(id string) *config.Target {
	for i := range s.Cfg.Targets {
		if s.Cfg.Targets[i].ID == id {
			return &s.Cfg.Targets[i]
		}
	}
	return nil
}

func (s *Server) slo(id string) float64 {
	for _, t := range s.Cfg.Targets {
		if t.ID == id {
			return t.SLO
		}
	}
	return s.Cfg.SLO
}

// incidentDetail: one incident with every raw check (all locations) from 15 minutes before
// it began to 15 minutes after it ended (or now), and the target's verdicts in that window:
// what an on-call engineer reads first, and what the postmortem draft is built from.
func (s *Server) incidentDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	in, err := s.Store.Incident(ctx, id)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	if in == nil {
		http.NotFound(w, r)
		return
	}
	from := in.Started.Add(-15 * time.Minute)
	to := time.Now()
	if !in.Open() {
		to = in.Ended.Add(15 * time.Minute)
	}
	checks, err := s.Store.ChecksBetween(ctx, in.TargetID, from, to, 2000)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	verdicts, err := s.Store.VerdictsBetween(ctx, in.TargetID, from, to, 1000)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	var t any
	for _, c := range s.Cfg.Targets {
		if c.ID == in.TargetID {
			t = map[string]any{"id": c.ID, "name": c.Name, "provider": c.Provider, "category": c.Category, "docs": c.Docs,
				"slo": c.SLO, "interval": c.Interval.String()}
		}
	}
	writeJSON(w, map[string]any{"incident": in, "target": t, "from": from, "to": to,
		"checks": recentChecks(checks), "verdicts": verdicts})
}
