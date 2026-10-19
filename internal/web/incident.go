package web

import (
	"crypto/subtle"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
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
	notes, err := s.Store.Notes(ctx, in.ID)
	if err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	writeJSON(w, map[string]any{"incident": in, "target": t, "from": from, "to": to,
		"checks": recentChecks(checks), "verdicts": verdicts, "notes": notes, "canNote": s.AdminToken != ""})
}

// addNote: POST /api/v1/incidents/{id}/notes {"author","text"}, with
// Authorization: Bearer $PAMON_ADMIN_TOKEN. Off (404) without a token configured.
func (s *Server) addNote(w http.ResponseWriter, r *http.Request) {
	if s.AdminToken == "" {
		http.NotFound(w, r)
		return
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.AdminToken)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	in, err := s.Store.Incident(r.Context(), id)
	if err != nil || in == nil {
		http.NotFound(w, r)
		return
	}
	var body struct{ Author, Text, Kind string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	// text that wasn't UTF-8 (a shell in another code page) is refused, not stored garbled:
	// encoding/json turns such bytes into U+FFFD
	if strings.ContainsRune(body.Text, utf8.RuneError) || strings.ContainsRune(body.Author, utf8.RuneError) {
		http.Error(w, "author and text must be UTF-8", http.StatusBadRequest)
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	if body.Kind != "ack" {
		body.Kind = "note"
	} else if body.Text == "" {
		body.Text = "확인 중 (담당)"
	}
	if body.Text == "" || utf8.RuneCountInString(body.Text) > 2000 {
		http.Error(w, "text: 1 to 2000 characters", http.StatusBadRequest)
		return
	}
	if body.Author = strings.TrimSpace(body.Author); body.Author == "" {
		body.Author = "운영자"
	}
	n := store.Note{Incident: id, At: time.Now(), Author: body.Author, Text: body.Text, Kind: body.Kind}
	if err := s.Store.AddNote(r.Context(), &n); err != nil {
		http.Error(w, "internal error", 500)
		return
	}
	writeJSON(w, n)
}
