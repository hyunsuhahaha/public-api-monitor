package notify

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/hyunsuhahaha/public-api-monitor/internal/config"
	"github.com/hyunsuhahaha/public-api-monitor/internal/incident"
	"github.com/hyunsuhahaha/public-api-monitor/internal/store"
)

// Grouper holds incidents that open (or close) at the same agency for Wait and sends
// them as one message, like Alertmanager's group_by/group_wait: when an agency's servers
// go down, all its APIs fail within a check or two of each other, and that is one page,
// not five. Updates and reopens go out at once.
type Grouper struct {
	Wait time.Duration
	Send func(ctx context.Context, text string)
	// PublicURL, if set, is linked from grouped messages.
	PublicURL string

	mu      sync.Mutex
	pending map[groupKey][]groupItem
}

type groupKey struct {
	provider string
	ev       incident.Event
}

type groupItem struct {
	in incident.Incident
	t  *config.Target
}

// NewGrouper groups the webhook's messages.
func NewGrouper(w *Webhook, wait time.Duration) *Grouper {
	return &Grouper{Wait: wait, Send: w.post, PublicURL: w.PublicURL}
}

func (g *Grouper) Notify(ctx context.Context, ev incident.Event, in *incident.Incident, t *config.Target) {
	if ev != incident.Opened && ev != incident.Closed {
		g.Send(ctx, Message(ev, in, t, g.PublicURL))
		return
	}
	k := groupKey{t.Provider, ev}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.pending == nil {
		g.pending = map[groupKey][]groupItem{}
	}
	if len(g.pending[k]) == 0 {
		time.AfterFunc(g.Wait, func() { g.flush(k) })
	}
	g.pending[k] = append(g.pending[k], groupItem{*in, t})
}

func (g *Grouper) flush(k groupKey) {
	g.mu.Lock()
	items := g.pending[k]
	delete(g.pending, k)
	g.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if len(items) == 1 {
		g.Send(ctx, Message(k.ev, &items[0].in, items[0].t, g.PublicURL))
		return
	}
	g.Send(ctx, groupMessage(k.provider, k.ev, items, g.PublicURL))
}

// groupMessage is the text for several of one agency's incidents opening or closing together.
func groupMessage(provider string, ev incident.Event, items []groupItem, publicURL string) string {
	var b strings.Builder
	if ev == incident.Closed {
		fmt.Fprintf(&b, "🟢 [기관 복구] %s API %d개 복구", provider, len(items))
	} else {
		fmt.Fprintf(&b, "🔴 [기관 장애] %s API %d개 동시 장애 — 기관 쪽 문제일 가능성", provider, len(items))
	}
	for _, it := range items {
		if ev == incident.Closed {
			fmt.Fprintf(&b, "\n· %s — %s 동안", it.t.Name, humanDuration(it.in.Ended.Sub(it.in.Started)))
		} else {
			fmt.Fprintf(&b, "\n· %s — %s", it.t.Name, it.in.Reason)
		}
	}
	if publicURL != "" {
		fmt.Fprintf(&b, "\n%s/", strings.TrimRight(publicURL, "/"))
	}
	return b.String()
}

func (g *Grouper) NotifySchema(ctx context.Context, e store.SchemaEvent, t *config.Target) {
	g.Send(ctx, SchemaMessage(e, t, g.PublicURL))
}

// NotifyNote goes out at once (notes are not grouped).
func (g *Grouper) NotifyNote(ctx context.Context, in *incident.Incident, t *config.Target, n store.Note) {
	g.Send(ctx, NoteMessage(in, t, n, g.PublicURL))
}
