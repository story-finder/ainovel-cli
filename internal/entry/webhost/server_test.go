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
	startEntered   chan struct{}
	startRelease   chan struct{}
	startEnterOnce sync.Once
}

type modelSelectionFake struct {
	provider string
	model    string
	explicit bool
}

type modelRuntimeFake struct {
	*fakeRuntime
	providers []string
	models    map[string][]string
	current   map[string]modelSelectionFake

	switchedRole     string
	switchedProvider string
	switchedModel    string
	switchErr        error
}

type coCreateRuntimeFake struct {
	*fakeRuntime
	entered     chan struct{}
	release     chan struct{}
	enterOnce   sync.Once
	history     []host.CoCreateMessage
	progress    []struct{ kind, text string }
	reply       host.CoCreateReply
	streamError error
}

func newCoCreateRuntimeFake() *coCreateRuntimeFake {
	return &coCreateRuntimeFake{
		fakeRuntime: newFakeRuntime(),
		entered:     make(chan struct{}),
		release:     make(chan struct{}),
		reply: host.CoCreateReply{
			Message:     "Phản hồi đồng sáng tác",
			Prompt:      "Bản nháp chỉ thị sáng tác",
			Ready:       true,
			Suggestions: []string{"Thêm một nhân vật đồng hành"},
		},
	}
}

func (f *coCreateRuntimeFake) CoCreateStream(ctx context.Context, history []host.CoCreateMessage, onProgress func(string, string)) (host.CoCreateReply, error) {
	f.mu.Lock()
	f.history = append([]host.CoCreateMessage(nil), history...)
	progress := append([]struct{ kind, text string }(nil), f.progress...)
	reply, streamError := f.reply, f.streamError
	f.mu.Unlock()

	if f.entered != nil {
		f.enterOnce.Do(func() { close(f.entered) })
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return host.CoCreateReply{}, ctx.Err()
		}
	}
	for _, item := range progress {
		if onProgress != nil {
			onProgress(item.kind, item.text)
		}
	}
	return reply, streamError
}

func newFakeRuntimeWithCommandCapabilities() *modelRuntimeFake {
	return &modelRuntimeFake{
		fakeRuntime: newFakeRuntime(),
		providers:   []string{"ollama", "openrouter"},
		models: map[string][]string{
			"openrouter": {"google/gemini-2.5-pro"},
			"ollama":     {"qwen3.5:27b"},
		},
		current: map[string]modelSelectionFake{
			"default": {provider: "openrouter", model: "google/gemini-2.5-pro", explicit: true},
		},
	}
}

func (f *modelRuntimeFake) ConfiguredProviders() []string {
	return append([]string(nil), f.providers...)
}

func (f *modelRuntimeFake) ConfiguredModels(provider string) []string {
	return append([]string(nil), f.models[provider]...)
}

func (f *modelRuntimeFake) CurrentModelSelection(role string) (string, string, bool) {
	selection, ok := f.current[role]
	if !ok && role != "default" {
		selection, ok = f.current["default"]
	}
	if !ok {
		return "", "", false
	}
	return selection.provider, selection.model, selection.explicit
}

func (f *modelRuntimeFake) SwitchModel(role, provider, model string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.switchedRole = role
	f.switchedProvider = provider
	f.switchedModel = model
	return f.switchErr
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
	f.calls = append(f.calls, "start")
	f.startPrompts = append(f.startPrompts, prompt)
	err := f.startErr
	f.mu.Unlock()
	if f.startEntered != nil {
		f.startEnterOnce.Do(func() { close(f.startEntered) })
	}
	if f.startRelease != nil {
		<-f.startRelease
	}
	return err
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
	app := newServer(rt, replayLimit)
	server := httptest.NewServer(app.Handler())
	t.Cleanup(func() {
		server.Close()
		app.Close()
	})
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

func TestModelsReturnsTUIRolesAndConfiguredModels(t *testing.T) {
	rt := newFakeRuntimeWithCommandCapabilities()
	rt.current["writer"] = modelSelectionFake{provider: "ollama", model: "qwen3.5:27b", explicit: true}
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodGet, "/models?role=writer", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /models status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}

	var body struct {
		Role      string   `json:"role"`
		Roles     []string `json:"roles"`
		Providers []struct {
			Provider string   `json:"provider"`
			Models   []string `json:"models"`
		} `json:"providers"`
		Current struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Explicit bool   `json:"explicit"`
		} `json:"current"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode model catalog: %v", err)
	}
	wantRoles := []string{"default", "coordinator", "architect", "writer", "editor"}
	if !reflect.DeepEqual(body.Roles, wantRoles) {
		t.Fatalf("roles = %#v, want %#v", body.Roles, wantRoles)
	}
	if body.Role != "writer" {
		t.Fatalf("role = %q, want writer", body.Role)
	}
	if len(body.Providers) != 2 || body.Current.Provider != "ollama" || body.Current.Model != "qwen3.5:27b" || !body.Current.Explicit {
		t.Fatalf("model catalog = %#v", body)
	}
}

func TestModelsReturnNotImplementedWithoutModelCapability(t *testing.T) {
	app := newServer(newFakeRuntime(), 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodGet, "/models", nil)
	assertRecorderJSONError(t, response, http.StatusNotImplemented, "không hỗ trợ")
}

func TestModelsRejectInvalidRole(t *testing.T) {
	app := newServer(newFakeRuntimeWithCommandCapabilities(), 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodGet, "/models?role=writer2", nil)
	assertRecorderJSONError(t, response, http.StatusBadRequest, "vai trò")
}

func TestCommandModelUsesSelectedProviderAndModel(t *testing.T) {
	rt := newFakeRuntimeWithCommandCapabilities()
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "command", "text": "/model writer",
		"provider": "openrouter", "model": "google/gemini-2.5-pro",
	})
	assertRecorderJSONOK(t, response, http.StatusOK)

	rt.mu.Lock()
	role, provider, model := rt.switchedRole, rt.switchedProvider, rt.switchedModel
	rt.mu.Unlock()
	if role != "writer" || provider != "openrouter" || model != "google/gemini-2.5-pro" {
		t.Fatalf("switch = %q/%q/%q", role, provider, model)
	}
}

func TestCommandModelPublishesVietnameseResult(t *testing.T) {
	app := newServer(newFakeRuntimeWithCommandCapabilities(), 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "command", "text": "/model writer",
		"provider": "openrouter", "model": "google/gemini-2.5-pro",
	})
	assertRecorderJSONOK(t, response, http.StatusOK)

	result := waitForFrame(t, app.hub, "command_result")
	var payload struct {
		Command  string `json:"command"`
		Markdown string `json:"markdown"`
		Level    string `json:"level"`
		Done     bool   `json:"done"`
	}
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatalf("decode command result: %v", err)
	}
	if payload.Command != "/model writer" || !strings.Contains(payload.Markdown, "Đã chuyển mô hình") || payload.Level != "success" || !payload.Done {
		t.Fatalf("command result = %#v", payload)
	}
}

func TestCommandModelRejectsInvalidRoleAndMissingSelectors(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			name: "invalid role",
			body: map[string]any{"action": "command", "text": "/model writer2", "provider": "openrouter", "model": "model"},
			want: "vai trò",
		},
		{
			name: "missing role",
			body: map[string]any{"action": "command", "text": "/model", "provider": "openrouter", "model": "model"},
			want: "vai trò",
		},
		{
			name: "missing provider",
			body: map[string]any{"action": "command", "text": "/model writer", "model": "model"},
			want: "provider",
		},
		{
			name: "missing model",
			body: map[string]any{"action": "command", "text": "/model writer", "provider": "openrouter"},
			want: "model",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := newServer(newFakeRuntimeWithCommandCapabilities(), 8)
			t.Cleanup(app.Close)
			response := serveAppJSON(t, app, http.MethodPost, "/commands", tc.body)
			assertRecorderJSONError(t, response, http.StatusBadRequest, tc.want)
		})
	}
}

func TestCommandRejectsHelpLifecycleAndUnknownCommands(t *testing.T) {
	for _, command := range []string{"/help", "/start", "/steer", "/continue", "/rewrite", "/unknown"} {
		t.Run(command, func(t *testing.T) {
			app := newServer(newFakeRuntimeWithCommandCapabilities(), 8)
			t.Cleanup(app.Close)
			response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
				"action": "command", "text": command,
			})
			assertRecorderJSONError(t, response, http.StatusBadRequest, "lệnh")
		})
	}
}

func TestStartCoCreateRequiresInitialText(t *testing.T) {
	app := newServer(newCoCreateRuntimeFake(), 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "start", "mode": "cocreate", "text": "   ",
	})
	assertRecorderJSONError(t, response, http.StatusBadRequest, "yêu cầu")
}

func TestStartCoCreateReturnsNotImplementedWithoutCapability(t *testing.T) {
	app := newServer(newFakeRuntime(), 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "start", "mode": "cocreate", "text": "ý tưởng truyện",
	})
	assertRecorderJSONError(t, response, http.StatusNotImplemented, "đồng sáng tác")
}

func TestStartCoCreateReturnsAcceptedAndPublishesProgressAndResult(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.release = nil
	rt.progress = []struct{ kind, text string }{
		{kind: host.CoCreateProgressThinking, text: "Đang suy nghĩ"},
		{kind: host.CoCreateProgressReply, text: "Đang trả lời"},
	}
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "start", "mode": "cocreate", "text": "Một truyện trinh thám",
	})
	assertRecorderJSONOK(t, response, http.StatusAccepted)

	firstProgress := waitForFrame(t, app.hub, "command_progress")
	secondProgress := waitForFrameAfter(t, app.hub, firstProgress.ID, "command_progress")
	var progress struct {
		Command string `json:"command"`
		Text    string `json:"text"`
		Stage   string `json:"stage"`
		Level   string `json:"level"`
	}
	if err := json.Unmarshal(secondProgress.Data, &progress); err != nil {
		t.Fatalf("decode command progress: %v", err)
	}
	if progress.Command != "cocreate" || progress.Stage != host.CoCreateProgressReply || progress.Text != "Đang trả lời" || progress.Level != "info" {
		t.Fatalf("command progress = %#v", progress)
	}

	result := waitForFrameAfter(t, app.hub, secondProgress.ID, "command_result")
	var payload struct {
		Command     string   `json:"command"`
		Markdown    string   `json:"markdown"`
		Prompt      string   `json:"prompt"`
		Ready       bool     `json:"ready"`
		Suggestions []string `json:"suggestions"`
		Level       string   `json:"level"`
		Done        bool     `json:"done"`
	}
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatalf("decode co-create result: %v", err)
	}
	if payload.Command != "cocreate" || payload.Markdown != rt.reply.Message || payload.Prompt != rt.reply.Prompt || !payload.Ready || !reflect.DeepEqual(payload.Suggestions, rt.reply.Suggestions) || payload.Level != "success" || !payload.Done {
		t.Fatalf("co-create result = %#v", payload)
	}

	rt.mu.Lock()
	history := append([]host.CoCreateMessage(nil), rt.history...)
	rt.mu.Unlock()
	if want := []host.CoCreateMessage{{Role: "user", Content: "Một truyện trinh thám"}}; !reflect.DeepEqual(history, want) {
		t.Fatalf("co-create history = %#v, want %#v", history, want)
	}
}

func TestStartCoCreateRejectsConcurrentSession(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	first := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "start", "mode": "cocreate", "text": "Truyện đầu tiên",
	})
	assertRecorderJSONOK(t, first, http.StatusAccepted)
	select {
	case <-rt.entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cold co-create to start")
	}

	second := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "start", "mode": "cocreate", "text": "Truyện thứ hai",
	})
	assertRecorderJSONError(t, second, http.StatusConflict, "đang")
	close(rt.release)
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

func TestCommandsSerializeRuntimeMutations(t *testing.T) {
	rt := newFakeRuntime()
	rt.startEntered = make(chan struct{})
	rt.startRelease = make(chan struct{})
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	startBody, err := json.Marshal(commandRequest{Action: "start", Text: "new story"})
	if err != nil {
		t.Fatalf("marshal start command: %v", err)
	}
	steerBody, err := json.Marshal(commandRequest{Action: "steer", Text: "change direction"})
	if err != nil {
		t.Fatalf("marshal steer command: %v", err)
	}
	postCommand := func(body []byte) <-chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			request := httptest.NewRequest(http.MethodPost, "/commands", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			app.Handler().ServeHTTP(response, request)
			done <- response
		}()
		return done
	}

	startDone := postCommand(startBody)
	select {
	case <-rt.startEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for start to enter runtime")
	}

	steerDone := postCommand(steerBody)
	select {
	case response := <-steerDone:
		t.Fatalf("steer completed before start released with status %d", response.Code)
	case <-time.After(50 * time.Millisecond):
	}

	close(rt.startRelease)
	if response := <-startDone; response.Code != http.StatusOK {
		t.Fatalf("start status = %d, want %d", response.Code, http.StatusOK)
	}
	if response := <-steerDone; response.Code != http.StatusOK {
		t.Fatalf("steer status = %d, want %d", response.Code, http.StatusOK)
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

func serveAppJSON(t *testing.T, app *server, method, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if value != nil {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal JSON request: %v", err)
		}
		body = bytes.NewReader(data)
	}
	request := httptest.NewRequest(method, path, body)
	if value != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func assertRecorderJSONOK(t *testing.T, response *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, wantStatus, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode JSON success: %v", err)
	}
	if ok, exists := body["ok"]; !exists || ok != true {
		t.Fatalf("JSON success body = %#v, want ok=true", body)
	}
}

func assertRecorderJSONError(t *testing.T, response *httptest.ResponseRecorder, wantStatus int, wantMessage string) {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, wantStatus, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q, want application/json", got)
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode JSON error: %v", err)
	}
	if !strings.Contains(body["error"], wantMessage) {
		t.Fatalf("JSON error = %q, want substring %q", body["error"], wantMessage)
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
