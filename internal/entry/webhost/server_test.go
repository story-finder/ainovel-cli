package webhost

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/tools"
)

type fakeRuntime struct {
	events chan host.Event
	stream chan string
	done   chan struct{}
	ask    *tools.AskUserTool

	mu             sync.Mutex
	snapshot       host.UISnapshot
	calls          []string
	startPrompts   []string
	resumeLabel    string
	resumeErr      error
	startErr       error
	continueErr    error
	abortResult    bool
	replay         []domain.RuntimeQueueItem
	replayErr      error
	closeCallCount int
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{
		events: make(chan host.Event, 8),
		stream: make(chan string, 8),
		done:   make(chan struct{}, 1),
		ask:    tools.NewAskUserTool(),
	}
}

func (f *fakeRuntime) StartPrepared(prompt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "start")
	f.startPrompts = append(f.startPrompts, prompt)
	return f.startErr
}

func (f *fakeRuntime) Resume() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "resume")
	return f.resumeLabel, f.resumeErr
}

func (f *fakeRuntime) Continue(text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "continue")
	return f.continueErr
}

func (f *fakeRuntime) Steer(string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "steer")
}

func (f *fakeRuntime) Abort() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "abort")
	return f.abortResult
}

func (f *fakeRuntime) Events() <-chan host.Event { return f.events }
func (f *fakeRuntime) Stream() <-chan string     { return f.stream }
func (f *fakeRuntime) Done() <-chan struct{}     { return f.done }

func (f *fakeRuntime) Snapshot() host.UISnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot
}

func (f *fakeRuntime) ReplayQueue(after int64) ([]domain.RuntimeQueueItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.replay, f.replayErr
}

func (f *fakeRuntime) AskUser() *tools.AskUserTool { return f.ask }

func (f *fakeRuntime) Close() {
	f.mu.Lock()
	f.closeCallCount++
	f.mu.Unlock()
}

func newTestServer(t *testing.T, rt *fakeRuntime) *httptest.Server {
	t.Helper()
	return newTestServerWithReplayLimit(t, rt, 8)
}

func newTestServerWithReplayLimit(t *testing.T, rt *fakeRuntime, replayLimit int) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(newServer(rt, replayLimit).Handler())
	t.Cleanup(server.Close)
	return server
}

func assertStatus(t *testing.T, server *httptest.Server, action, text string, want int) {
	t.Helper()
	body, err := json.Marshal(commandRequest{Action: action, Text: text})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL+"/commands", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new command request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("post command: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("POST /commands action %q status = %d, want %d; body = %s", action, response.StatusCode, want, data)
	}
}

func TestStatusReturnsExistingHostSnapshot(t *testing.T) {
	rt := newFakeRuntime()
	rt.snapshot.CurrentChapter = 3
	server := newTestServer(t, rt)

	response, err := server.Client().Get(server.URL + "/status")
	if err != nil {
		t.Fatalf("GET /status: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("GET /status status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	var body struct {
		Host    host.UISnapshot `json:"host"`
		Pending *questionFrame  `json:"pending"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.Host.CurrentChapter != 3 {
		t.Fatalf("status host current chapter = %d, want 3", body.Host.CurrentChapter)
	}
	if body.Pending != nil {
		t.Fatalf("status pending = %#v, want nil", body.Pending)
	}
}

func TestCommandsMapOnlyToExistingHostMethods(t *testing.T) {
	rt := newFakeRuntime()
	rt.abortResult = true
	server := newTestServer(t, rt)

	assertStatus(t, server, "start", "new story", http.StatusOK)
	assertStatus(t, server, "steer", "change direction", http.StatusOK)
	assertStatus(t, server, "rewrite", "rewrite the scene", http.StatusOK)
	assertStatus(t, server, "continue", "keep going", http.StatusOK)
	assertStatus(t, server, "pause", "", http.StatusOK)

	rt.mu.Lock()
	calls := append([]string(nil), rt.calls...)
	rt.mu.Unlock()
	if want := []string{"start", "steer", "steer", "continue", "abort"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("recorded calls = %v, want %v", calls, want)
	}
}

func TestResumeRequiresSavedWorkspaceAndStartRefusesRecoverableWorkspace(t *testing.T) {
	resumeRuntime := newFakeRuntime()
	resumeServer := newTestServer(t, resumeRuntime)
	assertStatus(t, resumeServer, "resume", "", http.StatusConflict)

	startRuntime := newFakeRuntime()
	startRuntime.snapshot.RecoveryLabel = "chapter-3 checkpoint"
	startServer := newTestServer(t, startRuntime)
	assertStatus(t, startServer, "start", "new story", http.StatusConflict)

	startRuntime.mu.Lock()
	defer startRuntime.mu.Unlock()
	if len(startRuntime.calls) != 0 {
		t.Fatalf("start calls = %v, want none", startRuntime.calls)
	}
}
