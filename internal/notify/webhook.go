// Package notify posts incident changes to a Discord or Slack webhook.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

type Webhook struct {
	URL    string
	Client *http.Client
	Log    *slog.Logger
	// PublicURL, if set, is linked from the message.
	PublicURL string
}

func NewWebhook(url, publicURL string, log *slog.Logger) *Webhook {
	return &Webhook{URL: url, PublicURL: publicURL, Log: log, Client: &http.Client{Timeout: 10 * time.Second}}
}

// Message is the text for one event (also used by tests).
func Message(ev incident.Event, in *incident.Incident, t *config.Target, publicURL string) string {
	var b strings.Builder
	switch ev {
	case incident.Opened:
		label := "장애"
		if in.Severity < 2 {
			label = "성능 저하"
		}
		fmt.Fprintf(&b, "🔴 [%s] %s (%s) — %s", label, t.Name, t.Provider, in.Reason)
	case incident.Updated:
		fmt.Fprintf(&b, "🟠 [진행 중] %s — %s", t.Name, in.LastReason)
	case incident.Reopened:
		fmt.Fprintf(&b, "🔴 [다시 발생 · 불안정 %d회] %s — %s", in.Flaps, t.Name, in.LastReason)
	case incident.Closed:
		fmt.Fprintf(&b, "🟢 [복구] %s — %s 동안", t.Name, humanDuration(in.Ended.Sub(in.Started)))
	default:
		return ""
	}
	if publicURL != "" {
		fmt.Fprintf(&b, "\n%s/#%s", strings.TrimRight(publicURL, "/"), t.ID)
	}
	return b.String()
}

func humanDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%d시간 %d분", h, m)
	case h > 0:
		return fmt.Sprintf("%d시간", h)
	}
	return fmt.Sprintf("%d분", m)
}

// SchemaMessage is the text for a response-field change.
func SchemaMessage(e store.SchemaEvent, t *config.Target, publicURL string) string {
	var b strings.Builder
	label := "필드 추가"
	if e.Kind == "removed" {
		label = "필드 사라짐"
	}
	fmt.Fprintf(&b, "🧩 [응답 형식 변경: %s] %s — %s", label, t.Name, strings.Join(e.Paths, ", "))
	if publicURL != "" {
		fmt.Fprintf(&b, "\n%s/#t=%s", strings.TrimRight(publicURL, "/"), t.ID)
	}
	return b.String()
}

func (w *Webhook) NotifySchema(ctx context.Context, e store.SchemaEvent, t *config.Target) {
	w.post(ctx, SchemaMessage(e, t, w.PublicURL))
}

func (w *Webhook) Notify(ctx context.Context, ev incident.Event, in *incident.Incident, t *config.Target) {
	w.post(ctx, Message(ev, in, t, w.PublicURL))
}

func (w *Webhook) post(ctx context.Context, text string) {
	if text == "" || w.URL == "" {
		return
	}
	// Discord takes {"content"}, Slack (and most others) {"text"}.
	payload := map[string]string{"text": text}
	if strings.Contains(w.URL, "discord.com") || strings.Contains(w.URL, "discordapp.com") {
		payload = map[string]string{"content": text}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.Client.Do(req)
	if err != nil {
		w.Log.Error("webhook", "err", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		w.Log.Error("webhook", "status", resp.StatusCode)
	}
}

// NoteMessage is the text for an operator's note or ack on an incident: the channel
// sees who has it, so two people don't chase the same outage.
func NoteMessage(in *incident.Incident, t *config.Target, n store.Note, publicURL string) string {
	var b strings.Builder
	if n.Kind == "ack" {
		fmt.Fprintf(&b, "👤 [담당] %s — #%d %s 장애를 맡음", n.Author, in.ID, t.Name)
	} else {
		fmt.Fprintf(&b, "📝 [메모] #%d %s — %s: %s", in.ID, t.Name, n.Author, n.Text)
	}
	if publicURL != "" {
		fmt.Fprintf(&b, "\n%s/#i=%d", strings.TrimRight(publicURL, "/"), in.ID)
	}
	return b.String()
}

func (w *Webhook) NotifyNote(ctx context.Context, in *incident.Incident, t *config.Target, n store.Note) {
	w.post(ctx, NoteMessage(in, t, n, w.PublicURL))
}
