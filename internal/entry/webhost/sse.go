package webhost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/host"
)

func (s *server) startPump() {
	ctx, cancel := context.WithCancel(context.Background())
	s.pumpCancel = cancel
	s.pumpDone = make(chan struct{})
	go func() {
		defer close(s.pumpDone)
		s.pump(ctx)
	}()
}

func (s *server) Close() {
	s.closeOnce.Do(func() {
		s.pumpCancel()
		<-s.pumpDone
	})
}

func (s *server) pump(ctx context.Context) {
	items, err := s.rt.ReplayQueue(0)
	if err == nil {
		for _, item := range items {
			s.hub.publish("runtime_replay", item)
		}
	}

	events := s.rt.Events()
	stream := s.rt.Stream()
	done := s.rt.Done()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				return
			}
			s.hub.publish("host_event", event)
		case delta, ok := <-stream:
			if !ok {
				stream = nil
				continue
			}
			switch delta {
			case host.StreamClearSentinel:
				s.hub.publish("stream_clear", struct{}{})
			case "":
				continue
			default:
				s.hub.publish("stream_delta", map[string]string{"text": delta})
			}
		case _, ok := <-done:
			if !ok {
				return
			}
			s.hub.publish("terminal", s.rt.Snapshot())
		case <-ctx.Done():
			return
		}
	}
}

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}

	subscription := s.hub.subscribe(lastEventID(r))
	defer subscription.Cancel()

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if err := writeSSEDirective(w, flusher, "retry: 3000\n\n"); err != nil {
		return
	}
	if subscription.Reset {
		if err := writeSSEFrame(w, flusher, frame{ID: subscription.ResetID, Event: "reset", Data: json.RawMessage(`{}`)}); err != nil {
			return
		}
	} else {
		for _, next := range subscription.Replay {
			if err := writeSSEFrame(w, flusher, next); err != nil {
				return
			}
		}
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case next, ok := <-subscription.Frames:
			if !ok {
				return
			}
			if err := writeSSEFrame(w, flusher, next); err != nil {
				return
			}
		case <-heartbeat.C:
			s.hub.publish("heartbeat", struct{}{})
		case <-r.Context().Done():
			return
		}
	}
}

func lastEventID(r *http.Request) int64 {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("after"))
	}
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 0 {
		return 0
	}
	return id
}

func writeSSEDirective(w io.Writer, flusher http.Flusher, directive string) error {
	if _, err := io.WriteString(w, directive); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeSSEFrame(w io.Writer, flusher http.Flusher, next frame) error {
	if next.ID > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", next.ID); err != nil {
			return err
		}
	}
	if next.Event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", next.Event); err != nil {
			return err
		}
	}
	if len(next.Data) == 0 {
		next.Data = json.RawMessage(`{}`)
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", next.Data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}
