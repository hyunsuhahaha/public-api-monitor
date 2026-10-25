package web

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/report"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// Atom feeds of what a developer wants to hear about an API they depend on: incidents
// (outages and slowness), data problems (an answer that arrives but is stale or wrong:
// json_expect, freshness, expect), response format changes (schema_events) and announced
// maintenance. /feed.xml covers every API, /feed/{id}.xml one. Feed readers, Slack's
// /feed subscribe and most chat tools poll them; no signup or secret is needed.

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Link    []atomLink  `xml:"link"`
	Entries []atomEntry `xml:"entry"`
}
type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
}
type atomEntry struct {
	Title    string        `xml:"title"`
	ID       string        `xml:"id"`
	Updated  string        `xml:"updated"`
	Link     atomLink      `xml:"link"`
	Category *atomCategory `xml:"category,omitempty"`
	Summary  string        `xml:"summary"`
	at       time.Time
}
type atomCategory struct {
	Term string `xml:"term,attr"`
}

// FeedLimit is how many entries of each kind a feed carries (newest first).
const FeedLimit = 50

// buildFeed is the feed of one target, or of all when id is "".
func (s *Server) buildFeed(ctx context.Context, base, id string) (*atomFeed, error) {
	now := time.Now()
	since := now.AddDate(0, 0, -90)
	list, err := s.Store.Incidents(ctx, id, since, FeedLimit)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, t := range s.Cfg.Targets {
		names[t.ID] = t.Name
	}
	name := func(id string) string {
		if n := names[id]; n != "" {
			return n
		}
		return id
	}
	self, title := base+"/feed.xml", "pamon: 장애·데이터 이상·응답 형식 변경"
	page := base + "/s/index.html"
	if id != "" {
		self, title, page = base+"/feed/"+id+".xml", name(id)+": 장애·데이터 이상·응답 형식 변경 (pamon)", base+"/s/"+id+".html"
	}
	f := &atomFeed{Title: title, ID: self, Updated: now.UTC().Format(time.RFC3339), Link: []atomLink{{Href: page}, {Href: self, Rel: "self"}}}
	for _, in := range list {
		label, kind := "장애", "incident"
		switch {
		case in.Status == "stale":
			label, kind = "데이터 이상", "data"
		case in.Severity < 2:
			label = "성능 저하"
		}
		updated := in.Started
		summary := fmt.Sprintf("%s 시작. %s", in.Started.In(config.KST).Format("2006-01-02 15:04 KST"), plainOr(in.Reason))
		title := fmt.Sprintf("[%s] %s", label, name(in.TargetID))
		if in.Open() {
			title += " (진행 중)"
		} else {
			updated = in.Ended
			title += " (복구, " + report.Dur(int(in.Ended.Sub(in.Started).Round(time.Minute).Minutes())) + ")"
			summary += fmt.Sprintf(" %s 복구.", in.Ended.In(config.KST).Format("15:04"))
		}
		f.Entries = append(f.Entries, atomEntry{Title: title, ID: fmt.Sprintf("%s/incidents/%d", base, in.ID), at: updated,
			Link: atomLink{Href: base + "/s/" + in.TargetID + ".html"}, Category: &atomCategory{kind}, Summary: summary})
	}
	sch, err := s.Store.SchemaEvents(ctx, id, since, FeedLimit)
	if err != nil {
		return nil, err
	}
	for _, e := range sch {
		label, what := store.SchemaLabel(e.Kind), "새로 생긴 필드"
		switch e.Kind {
		case "removed":
			what = "더 이상 오지 않는 필드"
		case "type_changed":
			what = "값의 형식이 바뀐 필드"
		}
		f.Entries = append(f.Entries, atomEntry{
			Title: fmt.Sprintf("[응답 형식 변경: %s] %s", label, name(e.Target)), ID: fmt.Sprintf("%s/schema/%d", base, e.ID), at: e.At,
			Link: atomLink{Href: base + "/s/" + e.Target + ".html"}, Category: &atomCategory{"schema"},
			Summary: fmt.Sprintf("%s: %s. 이 필드를 읽는 코드가 있다면 확인하세요.", what, strings.Join(e.Paths, ", ")),
		})
	}
	for _, m := range s.maintWindows(now) {
		if m.State == "ended" {
			continue
		}
		var hit []string
		for _, t := range m.Targets {
			if id == "" || t.ID == id {
				hit = append(hit, t.Name)
			}
		}
		if len(hit) == 0 {
			continue
		}
		summary := fmt.Sprintf("%s ~ %s (KST). 대상: %s. %s", kst(m.Start), kst(m.End), strings.Join(hit, ", "), m.Note)
		if m.URL != "" {
			summary += " 공지: " + m.URL
		}
		f.Entries = append(f.Entries, atomEntry{
			Title: fmt.Sprintf("[점검 예정] %s: %s", strings.Join(m.Match, ", "), m.Note), ID: base + "/maintenance/" + m.ID,
			at: m.Start, Link: atomLink{Href: base + "/maintenance.html"}, Category: &atomCategory{"maintenance"}, Summary: summary,
		})
	}
	sort.SliceStable(f.Entries, func(i, j int) bool { return f.Entries[i].at.After(f.Entries[j].at) })
	for i := range f.Entries {
		f.Entries[i].Updated = f.Entries[i].at.UTC().Format(time.RFC3339)
	}
	return f, nil
}

func plainOr(reason string) string {
	if p := plainReason(reason); p != "" {
		return p
	}
	return reason
}

func (s *Server) feed(w http.ResponseWriter, r *http.Request) {
	id := ""
	if v := r.PathValue("id"); v != "" {
		id = strings.TrimSuffix(v, ".xml")
		if !strings.HasSuffix(v, ".xml") || s.target(id) == nil {
			http.NotFound(w, r)
			return
		}
	}
	f, err := s.buildFeed(r.Context(), s.baseURL(r), id)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/atom+xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(f)
}
