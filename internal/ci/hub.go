package ci

import (
	"io"
	"sync"
)

// Hub fans live pipeline output out to SSE subscribers.
type Hub struct {
	mu     sync.RWMutex
	topics map[string]map[chan []byte]struct{}
}

func NewHub() *Hub { return &Hub{topics: map[string]map[chan []byte]struct{}{}} }

func (h *Hub) Subscribe(topic string) (<-chan []byte, func()) {
	ch := make(chan []byte, 256)
	h.mu.Lock()
	if h.topics[topic] == nil {
		h.topics[topic] = map[chan []byte]struct{}{}
	}
	h.topics[topic][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.topics[topic], ch)
		if len(h.topics[topic]) == 0 {
			delete(h.topics, topic)
		}
		h.mu.Unlock()
	}
}

func (h *Hub) Publish(topic, text string) {
	if text == "" {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.topics[topic] {
		select {
		case ch <- []byte(text):
		default: // slow subscriber; drop rather than block CI
		}
	}
}

func (h *Hub) Writer(topic string) io.Writer { return hubWriter{hub: h, topic: topic} }

type hubWriter struct {
	hub   *Hub
	topic string
}

func (w hubWriter) Write(p []byte) (int, error) {
	w.hub.Publish(w.topic, string(p))
	return len(p), nil
}
