package webhost

import (
	"bufio"
	"context"
	"encoding/json"
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
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "http://example.test/events", nil).WithContext(ctx)
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

func waitForFrame(t *testing.T, hub *eventHub) frame {
	t.Helper()
	sub := hub.subscribe(0)
	defer sub.Cancel()

	select {
	case next, ok := <-sub.Frames:
		if !ok {
			t.Fatal("event hub subscription closed while waiting for frame")
		}
		return next
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hub frame")
		return frame{}
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
	first := waitForFrame(t, app.hub)
	rt.stream <- "next delta"

	client := openSSE(t, server.Config.Handler, strconv.FormatInt(first.ID, 10))
	got := client.Next(t)
	if got.Event != "stream_delta" {
		t.Fatalf("event = %q, want stream_delta", got.Event)
	}
}

func TestSSEStaleLastEventIDEmitsReset(t *testing.T) {
	rt := newFakeRuntime()
	app, server := newSSETestServer(t, rt, 1)

	rt.events <- host.Event{Category: "SYSTEM", Summary: "one"}
	first := waitForFrame(t, app.hub)
	rt.events <- host.Event{Category: "SYSTEM", Summary: "two"}
	_ = waitForFrame(t, app.hub)
	rt.events <- host.Event{Category: "SYSTEM", Summary: "three"}
	_ = waitForFrame(t, app.hub)

	client := openSSE(t, server.Config.Handler, strconv.FormatInt(first.ID, 10))
	got := client.Next(t)
	if got.Event != "reset" {
		t.Fatalf("event = %q, want reset", got.Event)
	}
}
