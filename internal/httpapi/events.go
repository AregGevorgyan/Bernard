package httpapi

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Event is one server-sent message. Kind is the SSE event name; the display
// and the admin console each ignore the kinds they do not care about.
type Event struct {
	Kind string `json:"kind"` // playlist | command | hello
	Data any    `json:"data,omitempty"`
}

// Hub fans events out to every connected browser over Server-Sent Events.
//
// SSE replaces the old build's HTTP long-polling: it is one held-open GET with
// automatic browser-side reconnection, and it lets an approval reach the TV in
// milliseconds instead of on the next poll.
type Hub struct {
	mu      sync.RWMutex
	clients map[chan Event]struct{}
}

func NewHub() *Hub { return &Hub{clients: map[chan Event]struct{}{}} }

func (h *Hub) subscribe() chan Event {
	// Buffered so one wedged client cannot block a broadcast; if it overflows
	// we drop that client's updates rather than stalling everyone else.
	ch := make(chan Event, 8)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan Event) {
	h.mu.Lock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// Broadcast delivers an event to every subscriber, skipping any whose buffer
// is full.
func (h *Hub) Broadcast(ev Event) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.clients {
		select {
		case ch <- ev:
		default:
			slog.Warn("sse client is not keeping up, dropping event", "kind", ev.Kind)
		}
	}
}

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// PlaylistChanged tells every display to re-fetch. We deliberately send a
// nudge rather than the playlist itself so there is exactly one code path that
// builds a playlist.
func (h *Hub) PlaylistChanged() { h.Broadcast(Event{Kind: "playlist"}) }

// Command sends a one-off instruction to the display: next, prev, reload.
func (h *Hub) Command(name string) { h.Broadcast(Event{Kind: "command", Data: name}) }

// handleEvents is the SSE endpoint. It is public: the display has no session,
// and nothing here is sensitive (it carries no ad content, only nudges).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	// Defeats proxy buffering (nginx and some Cloudflare paths) which would
	// otherwise hold these tiny frames until a buffer fills.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)

	// Tell the browser to wait 2s before reconnecting after a drop.
	fmt.Fprint(w, "retry: 2000\n\n")
	writeEvent(w, Event{Kind: "hello"})
	_ = rc.Flush()

	// A comment line every 20s keeps intermediaries from reaping an idle
	// connection, and surfaces a dead display quickly on the server side.
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			writeEvent(w, ev)
			if err := rc.Flush(); err != nil {
				return
			}
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

func writeEvent(w http.ResponseWriter, ev Event) {
	payload, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Kind, payload)
}
