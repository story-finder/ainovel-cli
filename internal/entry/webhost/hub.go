package webhost

import (
	"encoding/json"
	"sync"
)

type frame struct {
	ID    int64
	Event string
	Data  json.RawMessage
}

type subscription struct {
	Replay  []frame
	Reset   bool
	ResetID int64
	Frames  <-chan frame
	Cancel  func()
}

type eventHub struct {
	mu      sync.Mutex
	limit   int
	nextID  int64
	history []frame
	clients map[chan frame]struct{}
}

func (h *eventHub) nextIDLocked() int64 {
	h.nextID++
	return h.nextID
}

func newEventHub(limit int) *eventHub {
	return &eventHub{
		limit:   limit,
		clients: make(map[chan frame]struct{}),
	}
}

func (h *eventHub) publish(event string, value any) frame {
	data, _ := json.Marshal(value)

	h.mu.Lock()
	defer h.mu.Unlock()

	f := frame{ID: h.nextIDLocked(), Event: event, Data: data}
	h.history = append(h.history, f)
	if len(h.history) > h.limit {
		h.history = h.history[len(h.history)-h.limit:]
	}

	for client := range h.clients {
		select {
		case client <- f:
		default:
			delete(h.clients, client)
			close(client)
		}
	}

	return f
}

func (h *eventHub) subscribe(after int64) subscription {
	h.mu.Lock()
	defer h.mu.Unlock()

	client := make(chan frame, h.limit)
	sub := subscription{
		Frames: client,
		Cancel: func() {
			h.mu.Lock()
			defer h.mu.Unlock()

			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client)
			}
		},
	}

	if after == 0 {
		sub.Replay = append([]frame(nil), h.history...)
	} else if len(h.history) > 0 {
		oldest := h.history[0].ID
		if after > 0 && after < oldest-1 {
			sub.Reset = true
			sub.ResetID = h.nextIDLocked()
		} else {
			for _, f := range h.history {
				if f.ID > after {
					sub.Replay = append(sub.Replay, f)
				}
			}
		}
	}

	h.clients[client] = struct{}{}
	return sub
}
