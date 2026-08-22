package webhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/host"
)

type sseFrame struct {
	ID    int64
	Event string
	Data  json.RawMessage
}

type sseResult struct {
	frame sseFrame
	err   error
}

type sseClient struct {
	frames chan sseResult
	close  func()
	once   sync.Once
}

type pipeResponseWriter struct {
	header http.Header
	writer *io.PipeWriter
	status int
}

func (w *pipeResponseWriter) Header() http.Header { return w.header }

func (w *pipeResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *pipeResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.writer.Write(data)
}

func (w *pipeResponseWriter) Flush() {}

func openSSE(t *testing.T, handler http.Handler, lastEventID string) *sseClient {
	return openSSERequest(t, handler, "http://example.test/events", lastEventID)
}

func openSSEQuery(t *testing.T, handler http.Handler, query, lastEventID string) *sseClient {
	return openSSERequest(t, handler, "http://example.test/events?"+query, lastEventID)
}

func openSSERequest(t *testing.T, handler http.Handler, target, lastEventID string) *sseClient {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
	if lastEventID != "" {
		request.Header.Set("Last-Event-ID", lastEventID)
	}

	reader, writer := io.Pipe()
	responseWriter := &pipeResponseWriter{
		header: make(http.Header),
		writer: writer,
	}
	client := &sseClient{
		frames: make(chan sseResult, 1),
		close: func() {
			cancel()
			_ = reader.Close()
			_ = writer.Close()
		},
	}

	go func() {
		handler.ServeHTTP(responseWriter, request)
		_ = writer.Close()
	}()
	go readSSEFrames(reader, client.frames)

	t.Cleanup(client.Close)
	return client
}

func (c *sseClient) Close() {
	c.once.Do(c.close)
}

func (c *sseClient) Next(t *testing.T) sseFrame {
	t.Helper()
	select {
	case result := <-c.frames:
		if result.err != nil {
			t.Fatalf("read SSE frame: %v", result.err)
		}
		return result.frame
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for SSE frame")
		return sseFrame{}
	}
}

func readSSEFrames(reader *io.PipeReader, results chan<- sseResult) {
	scanner := bufio.NewScanner(reader)
	var current sseFrame
	var hasField bool
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if hasField && (current.ID != 0 || current.Event != "" || len(current.Data) > 0) {
				results <- sseResult{frame: current}
			}
			current = sseFrame{}
			hasField = false
			continue
		}
		switch {
		case strings.HasPrefix(line, "id: "):
			id, err := strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			if err != nil {
				results <- sseResult{err: fmt.Errorf("parse event id: %w", err)}
				return
			}
			current.ID = id
			hasField = true
		case strings.HasPrefix(line, "event: "):
			current.Event = strings.TrimPrefix(line, "event: ")
			hasField = true
		case strings.HasPrefix(line, "data: "):
			current.Data = json.RawMessage(strings.TrimPrefix(line, "data: "))
			hasField = true
		}
	}
	if err := scanner.Err(); err != nil {
		results <- sseResult{err: err}
	}
}

func waitForFrame(t *testing.T, hub *eventHub, events ...string) frame {
	return waitForFrameAfter(t, hub, 0, events...)
}

func waitForFrameAfter(t *testing.T, hub *eventHub, afterID int64, events ...string) frame {
	t.Helper()
	sub := hub.subscribe(afterID)
	defer sub.Cancel()
	if sub.Reset {
		t.Fatalf("event hub replay reset after frame ID %d", afterID)
		return frame{}
	}

	matches := func(next frame) bool {
		if next.ID <= afterID {
			return false
		}
		if len(events) == 0 {
			return true
		}
		for _, event := range events {
			if next.Event == event {
				return true
			}
		}
		return false
	}
	for index := len(sub.Replay) - 1; index >= 0; index-- {
		if matches(sub.Replay[index]) {
			return sub.Replay[index]
		}
	}

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case next, ok := <-sub.Frames:
			if !ok {
				t.Fatal("event hub subscription closed while waiting for frame")
				return frame{}
			}
			if matches(next) {
				return next
			}
		case <-timer.C:
			t.Fatal("timed out waiting for hub frame")
			return frame{}
		}
	}
}

func TestWaitForFrameConsumesRetainedReplay(t *testing.T) {
	hub := newEventHub(8)
	want := hub.publish("host_event", "retained")

	got := waitForFrame(t, hub)
	if got.ID != want.ID {
		t.Fatalf("frame ID = %d, want %d", got.ID, want.ID)
	}
}

func TestWaitForFrameAfterSkipsEarlierRetainedFrames(t *testing.T) {
	hub := newEventHub(8)
	first := hub.publish("host_event", "one")
	second := hub.publish("host_event", "two")

	got := waitForFrameAfter(t, hub, first.ID, "host_event")
	if got.ID != second.ID {
		t.Fatalf("frame ID = %d, want %d", got.ID, second.ID)
	}
}

func TestHubResetReservesIDWithoutBroadcasting(t *testing.T) {
	hub := newEventHub(1)
	first := hub.publish("host_event", "one")
	hub.publish("host_event", "two")
	hub.publish("host_event", "three")

	stale := hub.subscribe(first.ID)
	defer stale.Cancel()
	if !stale.Reset {
		t.Fatal("stale subscription did not request reset")
	}

	other := hub.subscribe(0)
	defer other.Cancel()
	live := hub.publish("host_event", "live")
	if live.ID <= stale.ResetID {
		t.Fatalf("live ID = %d, want > reserved reset ID %d", live.ID, stale.ResetID)
	}

	select {
	case got, ok := <-other.Frames:
		if !ok || got.ID != live.ID {
			t.Fatalf("unrelated subscriber frame = (%+v, %t), want live frame ID %d", got, ok, live.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live frame")
	}
}

func TestHubReservedResetIDsDoNotCauseFalseReset(t *testing.T) {
	hub := newEventHub(1)
	first := hub.publish("host_event", "one")
	hub.publish("host_event", "two")
	hub.publish("host_event", "three")

	firstStale := hub.subscribe(first.ID)
	if !firstStale.Reset {
		t.Fatal("first stale subscription did not request reset")
	}
	firstResetID := firstStale.ResetID
	firstStale.Cancel()

	secondStale := hub.subscribe(first.ID)
	if !secondStale.Reset {
		t.Fatal("second stale subscription did not request reset")
	}
	secondStale.Cancel()

	live := hub.publish("host_event", "four")
	sub := hub.subscribe(firstResetID)
	defer sub.Cancel()
	if sub.Reset {
		t.Fatalf("subscription after private reset ID %d unexpectedly requested reset", firstResetID)
	}
	if len(sub.Replay) != 1 || sub.Replay[0].ID != live.ID {
		t.Fatalf("replay = %+v, want retained live frame ID %d", sub.Replay, live.ID)
	}
}

func TestLastEventIDUsesQueryWhenHeaderIsMissing(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://example.test/events?after=7", nil)

	if got := lastEventID(request); got != 7 {
		t.Fatalf("query Last-Event-ID = %d, want 7", got)
	}
}

func TestLastEventIDHeaderTakesPrecedenceOverQuery(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://example.test/events?after=7", nil)
	request.Header.Set("Last-Event-ID", "3")

	if got := lastEventID(request); got != 3 {
		t.Fatalf("header Last-Event-ID = %d, want 3", got)
	}
}

func TestSSEQueryLastEventIDReplaysCurrentProcessFrame(t *testing.T) {
	rt := newFakeRuntime()
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	rt.events <- host.Event{Category: "SYSTEM", Summary: "host event"}
	first := waitForFrame(t, app.hub, "host_event")
	rt.stream <- "next delta"
	stream := waitForFrameAfter(t, app.hub, first.ID, "stream_delta")

	client := openSSEQuery(t, app.Handler(), "after="+strconv.FormatInt(first.ID, 10), "")
	got := client.Next(t)
	if got.Event != "stream_delta" || got.ID != stream.ID {
		t.Fatalf("query replay frame = (%s, %d), want (stream_delta, %d)", got.Event, got.ID, stream.ID)
	}
}

func TestSSEHeaderLastEventIDTakesPrecedenceOverQueryReplay(t *testing.T) {
	rt := newFakeRuntime()
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	rt.events <- host.Event{Category: "SYSTEM", Summary: "first"}
	first := waitForFrame(t, app.hub, "host_event")
	rt.stream <- "stream"
	stream := waitForFrameAfter(t, app.hub, first.ID, "stream_delta")
	rt.events <- host.Event{Category: "SYSTEM", Summary: "after stream"}
	third := waitForFrameAfter(t, app.hub, stream.ID, "host_event")

	client := openSSEQuery(t, app.Handler(), "after="+strconv.FormatInt(first.ID, 10), strconv.FormatInt(stream.ID, 10))
	got := client.Next(t)
	if got.Event != "host_event" || got.ID != third.ID {
		t.Fatalf("header-precedence frame = (%s, %d), want (host_event, %d)", got.Event, got.ID, third.ID)
	}
}

func TestSSEResetCarriesIncreasingID(t *testing.T) {
	rt := newFakeRuntime()
	app := newServer(rt, 1)
	t.Cleanup(app.Close)

	rt.events <- host.Event{Category: "SYSTEM", Summary: "one"}
	first := waitForFrame(t, app.hub, "host_event")
	rt.events <- host.Event{Category: "SYSTEM", Summary: "two"}
	second := waitForFrameAfter(t, app.hub, first.ID, "host_event")
	rt.events <- host.Event{Category: "SYSTEM", Summary: "three"}
	_ = waitForFrameAfter(t, app.hub, second.ID, "host_event")

	client := openSSE(t, app.Handler(), strconv.FormatInt(first.ID, 10))
	got := client.Next(t)
	if got.Event != "reset" {
		t.Fatalf("event = %q, want reset", got.Event)
	}
	if got.ID <= first.ID {
		t.Fatalf("reset ID = %d, want > stale client ID %d", got.ID, first.ID)
	}
	live := app.hub.publish("host_event", host.Event{Category: "SYSTEM", Summary: "after reset"})
	if live.ID <= got.ID {
		t.Fatalf("live ID = %d, want > reset ID %d", live.ID, got.ID)
	}
}

func TestEventPumpDoesNotPublishReplayQueueErrors(t *testing.T) {
	rt := newFakeRuntime()
	rt.replayErr = errors.New("replay failed")
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	rt.events <- host.Event{Category: "SYSTEM", Summary: "host event"}
	_ = waitForFrame(t, app.hub, "host_event")

	app.hub.mu.Lock()
	history := append([]frame(nil), app.hub.history...)
	app.hub.mu.Unlock()
	for _, next := range history {
		if next.Event == "runtime_error" {
			t.Fatalf("unexpected runtime_error frame: %+v", next)
		}
	}
}

func newSSETestServer(t *testing.T, rt *fakeRuntime, replayLimit int) (*server, *httptest.Server) {
	t.Helper()
	app := newServer(rt, replayLimit)
	server := httptest.NewServer(app.Handler())
	t.Cleanup(func() {
		server.Close()
		app.Close()
	})
	return app, server
}

func TestEventPumpFansOutHostStreamToTwoSSEClients(t *testing.T) {
	rt := newFakeRuntime()
	_, server := newSSETestServer(t, rt, 8)
	first := openSSE(t, server.Config.Handler, "")
	second := openSSE(t, server.Config.Handler, "")

	rt.stream <- "Nội dung chương một"

	for _, client := range []*sseClient{first, second} {
		got := client.Next(t)
		if got.Event != "stream_delta" {
			t.Fatalf("event = %q, want stream_delta", got.Event)
		}
		var data map[string]string
		if err := json.Unmarshal(got.Data, &data); err != nil {
			t.Fatalf("decode stream_delta data: %v", err)
		}
		if data["text"] != "Nội dung chương một" {
			t.Fatalf("stream_delta text = %q, want Nội dung chương một", data["text"])
		}
	}
}

func TestSSELastEventIDReplaysCurrentProcessFrames(t *testing.T) {
	rt := newFakeRuntime()
	app, server := newSSETestServer(t, rt, 8)

	rt.events <- host.Event{Category: "SYSTEM", Summary: "host event"}
	first := waitForFrame(t, app.hub, "host_event")
	rt.stream <- "next delta"
	stream := waitForFrameAfter(t, app.hub, first.ID, "stream_delta")

	client := openSSE(t, server.Config.Handler, strconv.FormatInt(first.ID, 10))
	got := client.Next(t)
	if got.Event != "stream_delta" {
		t.Fatalf("event = %q, want stream_delta", got.Event)
	}
	if got.ID != stream.ID {
		t.Fatalf("replayed frame ID = %d, want %d", got.ID, stream.ID)
	}
}

func TestSSEStaleLastEventIDEmitsReset(t *testing.T) {
	rt := newFakeRuntime()
	app, server := newSSETestServer(t, rt, 1)

	rt.events <- host.Event{Category: "SYSTEM", Summary: "one"}
	first := waitForFrame(t, app.hub, "host_event")
	rt.events <- host.Event{Category: "SYSTEM", Summary: "two"}
	second := waitForFrameAfter(t, app.hub, first.ID, "host_event")
	rt.events <- host.Event{Category: "SYSTEM", Summary: "three"}
	_ = waitForFrameAfter(t, app.hub, second.ID, "host_event")

	client := openSSE(t, server.Config.Handler, strconv.FormatInt(first.ID, 10))
	got := client.Next(t)
	if got.Event != "reset" {
		t.Fatalf("event = %q, want reset", got.Event)
	}
}
