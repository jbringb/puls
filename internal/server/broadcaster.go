package server

import (
	"sync"

	"github.com/google/uuid"
)

type Event struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

type Broadcaster struct {
	mu   sync.Mutex
	subs map[string]chan Event
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: make(map[string]chan Event)}
}

func (b *Broadcaster) Subscribe() (<-chan Event, func()) {
	id := uuid.New().String()
	ch := make(chan Event, 32)
	b.mu.Lock()
	b.subs[id] = ch
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
}

func (b *Broadcaster) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default: // slow subscriber - drop
		}
	}
}
