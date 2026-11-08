package engine

import (
	"sync"
)

// Event is a server-sent event broadcast to panel subscribers.
type Event struct {
	Type      string `json:"type"`
	RunID     int64  `json:"run_id,omitempty"`
	AccountID int64  `json:"account_id,omitempty"`
	TS        int64  `json:"ts"`
	Data      any    `json:"data,omitempty"`
}

// Hub fans out events to SSE subscribers, dropping for slow clients.
type Hub struct {
	mu   sync.Mutex
	next int
	subs map[int]chan Event
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{subs: map[int]chan Event{}}
}

// Subscribe registers a subscriber.
func (h *Hub) Subscribe() (int, <-chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	id := h.next
	ch := make(chan Event, 128)
	h.subs[id] = ch
	return id, ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (h *Hub) Unsubscribe(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch, ok := h.subs[id]; ok {
		delete(h.subs, id)
		close(ch)
	}
}

// CloseAll removes and closes every subscriber. Called on shutdown so SSE
// handlers stop waiting on their channel and the HTTP server can go idle.
func (h *Hub) CloseAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, ch := range h.subs {
		delete(h.subs, id)
		close(ch)
	}
}

// Publish broadcasts an event without blocking.
func (h *Hub) Publish(ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
