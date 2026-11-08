package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Hub 是 SSE 广播中心：一条事件只序列化一次，然后扇出给所有浏览器。
// 慢客户端直接丢帧，绝不阻塞下载协程。
type Hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
	seq     int
}

func newHub() *Hub {
	return &Hub{clients: make(map[chan []byte]struct{})}
}

func (h *Hub) subscribe() (chan []byte, int) {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.seq++
	id := h.seq
	h.mu.Unlock()
	return ch, id
}

func (h *Hub) unsubscribe(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.clients[ch]; ok {
		delete(h.clients, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *Hub) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func (h *Hub) Broadcast(event string, data any) {
	b, err := json.Marshal(data)
	if err != nil {
		return
	}
	frame := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, b))
	h.mu.Lock()
	for ch := range h.clients {
		select {
		case ch <- frame:
		default: // 客户端太慢，丢弃这一帧
		}
	}
	h.mu.Unlock()
}

func (h *Hub) heartbeat() time.Duration { return 20 * time.Second }
