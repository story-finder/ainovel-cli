package webhost

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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
	steerTexts     []string
	continueTexts  []string
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
	f.continueTexts = append(f.continueTexts, text)
	return f.continueErr
}

func (f *fakeRuntime) Steer(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "steer")
	f.steerTexts = append(f.steerTexts, text)
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

func TestQuestionsRootReturnsJSONNotFound(t *testing.T) {
	server := newTestServer(t, newFakeRuntime())

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		request, err := http.NewRequest(method, server.URL+"/questions", nil)
		if err != nil {
			t.Fatalf("new %s /questions request: %v", method, err)
		}
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatalf("%s /questions: %v", method, err)
		}
		assertJSONError(t, response, http.StatusNotFound)
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
	if want := []string{host.BuildStartPrompt("new story")}; !reflect.DeepEqual(rt.startPrompts, want) {
		t.Fatalf("start prompts = %q, want %q", rt.startPrompts, want)
	}
	if want := []string{"change direction", "rewrite the scene"}; !reflect.DeepEqual(rt.steerTexts, want) {
		t.Fatalf("steer texts = %q, want %q", rt.steerTexts, want)
	}
	if want := []string{"keep going"}; !reflect.DeepEqual(rt.continueTexts, want) {
		t.Fatalf("continue texts = %q, want %q", rt.continueTexts, want)
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

func assertJSONError(t *testing.T, response *http.Response, wantStatus int) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d", response.StatusCode, wantStatus)
	}
	if got := response.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode JSON error: %v", err)
	}
	if body["error"] == "" {
		t.Fatalf("JSON error body = %#v, want non-empty error", body)
	}
}

func TestCommandsRejectMalformedAndOversizedBodiesAsJSON(t *testing.T) {
	server := newTestServer(t, newFakeRuntime())

	assertJSONError(t, postBody(t, server, "/commands", []byte(`{"action":`)), http.StatusBadRequest)
	oversized := []byte(`{"action":"start","text":"` + strings.Repeat("x", 64<<10) + `"}`)
	assertJSONError(t, postBody(t, server, "/commands", oversized), http.StatusRequestEntityTooLarge)
}

func TestQuestionRouteAnswersPendingQuestionAndReturnsJSONErrors(t *testing.T) {
	rt := newFakeRuntime()
	server := newTestServer(t, rt)

	assertJSONError(t, postJSON(t, server, "/questions/q-unknown/answer", answerRequest{
		Answers: map[string]string{"Direction?": "forward"},
	}), http.StatusNotFound)

	question := tools.Question{
		Question: "Direction?",
		Header:   "Direction",
		Options: []tools.Option{
			{Label: "Forward", Description: "Continue forward"},
			{Label: "Back", Description: "Go back"},
		},
	}
	args, err := json.Marshal(map[string]any{"questions": []tools.Question{question}})
	if err != nil {
		t.Fatalf("marshal question args: %v", err)
	}
	resultCh := make(chan struct {
		response []byte
		err      error
	}, 1)
	go func() {
		response, err := rt.ask.Execute(context.Background(), args)
		resultCh <- struct {
			response []byte
			err      error
		}{response: response, err: err}
	}()

	pending := waitForPendingQuestion(t, server)
	assertJSONError(t, postJSON(t, server, "/questions/"+pending.ID+"/answer", answerRequest{}), http.StatusBadRequest)
	assertJSONOK(t, postJSON(t, server, "/questions/"+pending.ID+"/answer", answerRequest{
		Answers: map[string]string{"Direction?": "forward"},
	}), http.StatusOK)

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("AskUserTool.Execute error = %v", result.err)
		}
		if !strings.Contains(string(result.response), "forward") {
			t.Fatalf("AskUserTool.Execute response = %s, want submitted answer", result.response)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for answered question")
	}
}

func postJSON(t *testing.T, server *httptest.Server, path string, value any) *http.Response {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON request: %v", err)
	}
	return postBody(t, server, path, body)
}

func postBody(t *testing.T, server *httptest.Server, path string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new POST request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return response
}

func assertJSONOK(t *testing.T, response *http.Response, wantStatus int) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != wantStatus {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want %d; body = %s", response.StatusCode, wantStatus, data)
	}
	if got := response.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode JSON success: %v", err)
	}
	if ok, exists := body["ok"]; !exists || ok != true {
		t.Fatalf("JSON success body = %#v, want ok=true", body)
	}
}

func waitForPendingQuestion(t *testing.T, server *httptest.Server) *questionFrame {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		response, err := server.Client().Get(server.URL + "/status")
		if err != nil {
			t.Fatalf("GET /status while waiting for question: %v", err)
		}
		var body statusResponse
		decodeErr := json.NewDecoder(response.Body).Decode(&body)
		response.Body.Close()
		if decodeErr != nil {
			t.Fatalf("decode pending status: %v", decodeErr)
		}
		if body.Pending != nil {
			return body.Pending
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for pending question")
	return nil
}
