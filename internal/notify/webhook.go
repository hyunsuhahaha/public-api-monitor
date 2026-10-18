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

func (w *Webhook) Notify(ctx context.Context, ev incident.Event, in *incident.Incident, t *config.Target) {
	text := Message(ev, in, t, w.PublicURL)
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
