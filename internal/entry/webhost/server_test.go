package webhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/host/sim"
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

	selectionEntered   chan struct{}
	selectionRelease   chan struct{}
	selectionEnterOnce sync.Once
	switchEntered      chan struct{}
	switchEnterOnce    sync.Once

	switchedRole     string
	switchedProvider string
	switchedModel    string
	switchErr        error
}

type coCreateRuntimeFake struct {
	*fakeRuntime
	entered       chan struct{}
	release       chan struct{}
	enterOnce     sync.Once
	history       []host.CoCreateMessage
	progress      []struct{ kind, text string }
	reply         host.CoCreateReply
	streamError   error
	finished      chan struct{}
	finishOnce    sync.Once
	ignoreContext bool

	dir           string
	exportResult  *exp.Result
	exportErr     error
	exportOptions exp.Options
	importEvents  []imp.Event
	importErr     error
	importOptions imp.Options
	simEvents     []sim.Event
	simErr        error

	stageEntered       chan struct{}
	stageRelease       chan struct{}
	stageEnterOnce     sync.Once
	stageHistory       []host.CoCreateMessage
	stageProgress      []struct{ kind, text string }
	stageReply         host.CoCreateReply
	stageStreamError   error
	stageIgnoreContext bool
	pauseResult        bool
	resumeDrafts       []string
	resumeErr          error
	cancelCount        int
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
		stageEntered: make(chan struct{}),
		stageRelease: make(chan struct{}),
		stageReply: host.CoCreateReply{
			Message:     "Kế hoạch giai đoạn",
			Prompt:      "Hướng đi giai đoạn",
			Ready:       true,
			Suggestions: []string{"Đổi nhịp chương"},
		},
		pauseResult: true,
	}
}

func (f *coCreateRuntimeFake) CoCreateStream(ctx context.Context, history []host.CoCreateMessage, onProgress func(string, string)) (host.CoCreateReply, error) {
	defer func() {
		if f.finished != nil {
			f.finishOnce.Do(func() { close(f.finished) })
		}
	}()

	f.mu.Lock()
	f.history = append([]host.CoCreateMessage(nil), history...)
	progress := append([]struct{ kind, text string }(nil), f.progress...)
	reply, streamError := f.reply, f.streamError
	f.mu.Unlock()

	if f.entered != nil {
		f.enterOnce.Do(func() { close(f.entered) })
	}
	if f.release != nil {
		if f.ignoreContext {
			<-f.release
		} else {
			select {
			case <-f.release:
			case <-ctx.Done():
				return host.CoCreateReply{}, ctx.Err()
			}
		}
	}
	for _, item := range progress {
		if onProgress != nil {
			onProgress(item.kind, item.text)
		}
	}
	return reply, streamError
}

func (f *coCreateRuntimeFake) Dir() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dir
}

func (f *coCreateRuntimeFake) Export(_ context.Context, opts exp.Options) (*exp.Result, error) {
	f.mu.Lock()
	f.exportOptions = opts
	result, err := f.exportResult, f.exportErr
	f.mu.Unlock()
	return result, err
}

func (f *coCreateRuntimeFake) ImportFrom(_ context.Context, opts imp.Options) (<-chan imp.Event, error) {
	f.mu.Lock()
	f.importOptions = opts
	events, err := append([]imp.Event(nil), f.importEvents...), f.importErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ch := make(chan imp.Event, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func (f *coCreateRuntimeFake) Simulate(_ context.Context) (<-chan sim.Event, error) {
	f.mu.Lock()
	events, err := append([]sim.Event(nil), f.simEvents...), f.simErr
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ch := make(chan sim.Event, len(events))
	for _, event := range events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func (f *coCreateRuntimeFake) PauseForCoCreate() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pauseResult
}

func (f *coCreateRuntimeFake) StageCoCreateStream(ctx context.Context, history []host.CoCreateMessage, onProgress func(string, string)) (host.CoCreateReply, error) {
	f.mu.Lock()
	f.stageHistory = append([]host.CoCreateMessage(nil), history...)
	progress := append([]struct{ kind, text string }(nil), f.stageProgress...)
	reply, streamErr := f.stageReply, f.stageStreamError
	release := f.stageRelease
	ignoreContext := f.stageIgnoreContext
	f.mu.Unlock()

	if f.stageEntered != nil {
		f.stageEnterOnce.Do(func() { close(f.stageEntered) })
	}
	if release != nil {
		if ignoreContext {
			<-release
		} else {
			select {
			case <-release:
			case <-ctx.Done():
				return host.CoCreateReply{}, ctx.Err()
			}
		}
	}
	for _, item := range progress {
		if onProgress != nil {
			onProgress(item.kind, item.text)
		}
	}
	return reply, streamErr
}

func (f *coCreateRuntimeFake) ResumeFromCoCreate(draft string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resumeDrafts = append(f.resumeDrafts, draft)
	return f.resumeErr
}

func (f *coCreateRuntimeFake) CancelCoCreate() {
	f.mu.Lock()
	f.cancelCount++
	f.mu.Unlock()
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
	if f.selectionEntered != nil {
		f.selectionEnterOnce.Do(func() { close(f.selectionEntered) })
	}
	if f.selectionRelease != nil {
		<-f.selectionRelease
	}
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
	if f.switchEntered != nil {
		f.switchEnterOnce.Do(func() { close(f.switchEntered) })
	}
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

func TestStatusIncludesStableCoCreateState(t *testing.T) {
	rt := newFakeRuntime()
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	app.coCreateMu.Lock()
	app.coCreateActive = true
	app.coCreateStage = true
	app.coCreateInFlight = true
	app.coCreateMu.Unlock()

	response := serveAppJSON(t, app, http.MethodGet, "/status", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /status status = %d, want %d", response.Code, http.StatusOK)
	}
	var body struct {
		CoCreate struct {
			Active   bool `json:"active"`
			Stage    bool `json:"stage"`
			InFlight bool `json:"inFlight"`
		} `json:"cocreate"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if body.CoCreate.Active != true || body.CoCreate.Stage != true || body.CoCreate.InFlight != true {
		t.Fatalf("co-create status = %#v, want active/stage/inFlight true", body.CoCreate)
	}
}

func TestWebAppRuntimeStatusOverridesBackendLabelForTerminalStates(t *testing.T) {
	_, filename, _, ok := goruntime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	appSource, err := os.ReadFile(filepath.Join(filepath.Dir(filename), "web", "app.js"))
	if err != nil {
		t.Fatalf("read web app: %v", err)
	}

	source := string(appSource)
	start := strings.Index(source, "function renderStatus(")
	end := strings.Index(source[start:], "function refreshStatus(")
	if start < 0 || end < 0 {
		t.Fatal("could not isolate renderStatus")
	}
	renderStatus := source[start : start+end]
	for _, expected := range []string{
		`const runtimeState = String(get(snapshot, "runtimeState", "RuntimeState") || "").toLowerCase();`,
		`["paused", "pausing", "completed"].includes(runtimeState)`,
		`setField("statusText", ["paused", "pausing", "completed"].includes(runtimeState) ? runtime : translatedStatus === "Chưa xác định" || translatedStatus === "Chưa có dữ liệu" ? runtime : translatedStatus);`,
	} {
		if !strings.Contains(renderStatus, expected) {
			t.Fatalf("renderStatus missing terminal runtime precedence: %s", expected)
		}
	}
}

func TestWebAppResetsStreamingBeforeRefreshingStatus(t *testing.T) {
	content := embeddedAppJS(t)
	start := strings.Index(content, `onEvent("reset"`)
	if start < 0 {
		t.Fatal("web/app.js does not register reset events")
	}
	end := strings.Index(content[start:], "});")
	if end < 0 {
		t.Fatal("could not isolate reset event handler")
	}
	handler := content[start : start+end]
	if !strings.Contains(handler, "finishAssistant();") || !strings.Contains(handler, "refreshStatus();") || strings.Index(handler, "finishAssistant();") > strings.Index(handler, "refreshStatus();") {
		t.Fatalf("reset handler must finish assistant before refresh: %s", handler)
	}
}

func TestWebAppHydratesAndReservesCoCreateState(t *testing.T) {
	content := embeddedAppJS(t)
	for _, want := range []string{
		`coCreatePending: false,`,
		`const coCreate = get(payload, "cocreate", "CoCreate");`,
		`state.coCreateActive = state.coCreatePending || Boolean(get(coCreate, "active", "Active")) || Boolean(get(coCreate, "inFlight", "InFlight"));`,
		`const startingCoCreate = action === "start" && body.mode === "cocreate";`,
		`if (startingCoCreate) state.coCreateActive = true;`,
		`if (startingCoCreate) state.coCreatePending = true;`,
		`if (command.trim().toLowerCase() === "/cocreate") state.coCreateActive = true;`,
		`if (command.trim().toLowerCase() === "/cocreate") state.coCreatePending = true;`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("web/app.js missing co-create state assertion %q", want)
		}
	}
}

func TestWebAppKeepsCoCreateReservedThroughAsyncApplyAndCleansFailedRequests(t *testing.T) {
	content := embeddedAppJS(t)
	for _, want := range []string{
		`if (action === "cocreate_apply") state.coCreatePending = true;`,
		`if (action === "cocreate_cancel") state.coCreatePending = true;`,
		`if (command === "cocreate_apply" && !error) {`,
		`state.coCreatePending = false;`,
		`state.coCreateActive = false;`,
		`if (coCreateCommand) { state.coCreatePending = false; state.coCreateActive = false; }`,
		`if (startingCoCreate) { state.coCreatePending = false; state.coCreateActive = false; }`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("web/app.js missing co-create reservation assertion %q", want)
		}
	}
}

func TestWebAppOpensModelPickerOnlyForExactCommand(t *testing.T) {
	content := embeddedAppJS(t)
	for _, want := range []string{
		`const modelCommand = value.match(/^\/model(?:\s+(.+))?$/i);`,
		`const modelArgs = (modelCommand[1] || "").trim().split(/\s+/).filter(Boolean);`,
		`if (modelArgs.length > 1) {`,
		`lệnh /model chỉ nhận tối đa một vai trò`,
		`await openModelPanel(role);`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("web/app.js missing exact /model handling %q", want)
		}
	}
	if strings.Contains(content, `value.toLowerCase().startsWith("/model")`) {
		t.Fatal("web/app.js still opens model picker for /modelish")
	}
}

func TestLifecycleErrorsAreVietnamese(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*fakeRuntime)
		action     string
		text       string
		mode       string
		wantStatus int
		want       string
	}{
		{name: "saved progress", configure: func(rt *fakeRuntime) { rt.snapshot.RecoveryLabel = "chương 3" }, action: "start", text: "ý tưởng", mode: "quick", wantStatus: http.StatusConflict, want: "tiến độ đã được lưu"},
		{name: "missing workspace", action: "resume", wantStatus: http.StatusConflict, want: "không có không gian làm việc đã lưu"},
		{name: "required text", action: "steer", wantStatus: http.StatusBadRequest, want: "văn bản không được để trống"},
		{name: "not running", action: "pause", wantStatus: http.StatusConflict, want: "lượt sáng tác hiện không chạy"},
		{name: "unknown action", action: "not-a-real-action", wantStatus: http.StatusBadRequest, want: "thao tác không xác định"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := newFakeRuntime()
			if tt.configure != nil {
				tt.configure(rt)
			}
			app := newServer(rt, 8)
			t.Cleanup(app.Close)
			response := serveAppJSON(t, app, http.MethodPost, "/commands", commandRequest{Action: tt.action, Text: tt.text, Mode: tt.mode})
			assertRecorderJSONError(t, response, tt.wantStatus, tt.want)
		})
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

func TestModelsSortConfiguredModels(t *testing.T) {
	rt := newFakeRuntimeWithCommandCapabilities()
	rt.models["openrouter"] = []string{"z-model", "a-model"}
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodGet, "/models", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /models status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
	var body modelCatalogResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode model catalog: %v", err)
	}
	for _, provider := range body.Providers {
		if provider.Provider == "openrouter" {
			if !reflect.DeepEqual(provider.Models, []string{"a-model", "z-model"}) {
				t.Fatalf("openrouter models = %#v, want sorted copy", provider.Models)
			}
			return
		}
	}
	t.Fatal("openrouter provider missing from model catalog")
}

func TestModelsSerializeCapabilitySnapshotWithCommand(t *testing.T) {
	rt := newFakeRuntimeWithCommandCapabilities()
	rt.selectionEntered = make(chan struct{})
	rt.selectionRelease = make(chan struct{})
	rt.switchEntered = make(chan struct{})
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	modelsDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		modelsDone <- serveAppJSON(t, app, http.MethodGet, "/models", nil)
	}()
	select {
	case <-rt.selectionEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for model snapshot")
	}

	commandDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		commandDone <- serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
			"action": "command", "text": "/model writer",
			"provider": "openrouter", "model": "google/gemini-2.5-pro",
		})
	}()
	select {
	case <-rt.switchEntered:
		t.Fatal("/model switched while /models snapshot was in progress")
	case <-time.After(50 * time.Millisecond):
	}

	close(rt.selectionRelease)
	assertRecorderJSONOK(t, <-commandDone, http.StatusOK)
	if response := <-modelsDone; response.Code != http.StatusOK {
		t.Fatalf("GET /models status = %d, want %d; body = %s", response.Code, http.StatusOK, response.Body.String())
	}
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

func TestCommandModelWithoutRoleUsesDefault(t *testing.T) {
	rt := newFakeRuntimeWithCommandCapabilities()
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "command", "text": "/model",
		"provider": "openrouter", "model": "google/gemini-2.5-pro",
	})
	assertRecorderJSONOK(t, response, http.StatusOK)

	rt.mu.Lock()
	role, provider, model := rt.switchedRole, rt.switchedProvider, rt.switchedModel
	rt.mu.Unlock()
	if role != "default" || provider != "openrouter" || model != "google/gemini-2.5-pro" {
		t.Fatalf("default model switch = %q/%q/%q", role, provider, model)
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
			name: "too many roles",
			body: map[string]any{"action": "command", "text": "/model writer editor", "provider": "openrouter", "model": "model"},
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

func TestCommandExportReturnsAcceptedAndVietnameseResult(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.exportResult = &exp.Result{Path: "/tmp/story.epub", Chapters: 3, Bytes: 4096, Skipped: []int{4, 5}}
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "command", "text": "/export story.epub from=2 to=5 --overwrite",
	})
	assertRecorderJSONOK(t, response, http.StatusAccepted)

	result := waitForFrame(t, app.hub, "command_result")
	var payload commandResultFrame
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatalf("decode export result: %v", err)
	}
	if payload.Command != "export" || payload.Level != "success" || !payload.Done || !strings.Contains(payload.Markdown, "/tmp/story.epub") || !strings.Contains(payload.Markdown, "3 chương") || !strings.Contains(payload.Markdown, "4,5") {
		t.Fatalf("export result = %#v", payload)
	}

	rt.mu.Lock()
	options := rt.exportOptions
	rt.mu.Unlock()
	want := exp.Options{OutPath: "story.epub", From: 2, To: 5, Overwrite: true}
	if !reflect.DeepEqual(options, want) {
		t.Fatalf("export options = %#v, want %#v", options, want)
	}
}

func TestCommandExportPublishesTerminalCoreError(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.exportErr = errors.New("ghi file thất bại")
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "command", "text": "/export story.txt",
	})
	assertRecorderJSONOK(t, response, http.StatusAccepted)

	result := waitForFrame(t, app.hub, "command_result")
	var payload commandResultFrame
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatalf("decode export error: %v", err)
	}
	if payload.Level != "error" || !payload.Done || payload.Error != rt.exportErr.Error() || !strings.Contains(payload.Markdown, rt.exportErr.Error()) {
		t.Fatalf("export error result = %#v", payload)
	}
}

func TestCommandImportPublishesEveryProgressEventAndResult(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.importEvents = []imp.Event{
		{Stage: imp.StageSplitting, Message: "Đang tách chương", Total: 2},
		{Stage: imp.StageChapter, Current: 1, Total: 2, Message: "Đang phân tích chương 1"},
		{Stage: imp.StageChapter, Current: 2, Total: 2, Message: "Đang phân tích chương 2"},
		{Stage: imp.StageDone, Current: 2, Total: 2, Message: "Đã nhập xong"},
	}
	app := newServer(rt, 16)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "command", "text": "/import story.md from=2",
	})
	assertRecorderJSONOK(t, response, http.StatusAccepted)

	result := waitForFrame(t, app.hub, "command_result")
	var payload commandResultFrame
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	if payload.Level != "success" || !payload.Done || !strings.Contains(payload.Markdown, "nhập") {
		t.Fatalf("import result = %#v", payload)
	}
	app.hub.mu.Lock()
	history := append([]frame(nil), app.hub.history...)
	app.hub.mu.Unlock()
	var progressCount int
	for _, next := range history {
		if next.Event != "command_progress" {
			continue
		}
		progressCount++
		var progress commandProgressFrame
		if err := json.Unmarshal(next.Data, &progress); err != nil {
			t.Fatalf("decode import progress: %v", err)
		}
		if progress.Command != "import" || progress.Level != "info" {
			t.Fatalf("import progress = %#v", progress)
		}
	}
	if progressCount != len(rt.importEvents) {
		t.Fatalf("import progress count = %d, want %d", progressCount, len(rt.importEvents))
	}

	rt.mu.Lock()
	options := rt.importOptions
	rt.mu.Unlock()
	if !reflect.DeepEqual(options, imp.Options{SourcePath: "story.md", ResumeFrom: 2}) {
		t.Fatalf("import options = %#v", options)
	}
}

func TestCommandSimulatePublishesProgressAndResult(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.simEvents = []sim.Event{
		{Stage: sim.StageScan, Message: "Đang quét", Total: 1},
		{Stage: sim.StageDone, Current: 1, Total: 1, Message: "Đã tạo hồ sơ"},
	}
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "command", "text": "/simulate",
	})
	assertRecorderJSONOK(t, response, http.StatusAccepted)
	first := waitForFrame(t, app.hub, "command_progress")
	var progress commandProgressFrame
	if err := json.Unmarshal(first.Data, &progress); err != nil {
		t.Fatal(err)
	}
	if progress.Command != "simulate" || progress.Stage != string(sim.StageScan) || progress.Total != 1 {
		t.Fatalf("simulate progress = %#v", progress)
	}
	result := waitForFrameAfter(t, app.hub, first.ID, "command_result")
	var payload commandResultFrame
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Command != "simulate" || payload.Level != "success" || !payload.Done || !strings.Contains(payload.Markdown, "mô phỏng") {
		t.Fatalf("simulate result = %#v", payload)
	}
}

func TestCommandImportAndSimulatePublishTerminalCoreErrors(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.importErr = errors.New("nguồn nhập không đọc được")
	rt.simErr = errors.New("không thể đọc thư mục simulate")
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	importResponse := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "command", "text": "/import story.md",
	})
	assertRecorderJSONOK(t, importResponse, http.StatusAccepted)
	importResult := waitForFrame(t, app.hub, "command_result")
	var importPayload commandResultFrame
	if err := json.Unmarshal(importResult.Data, &importPayload); err != nil {
		t.Fatal(err)
	}
	if importPayload.Command != "import" || importPayload.Level != "error" || importPayload.Error != rt.importErr.Error() || !importPayload.Done {
		t.Fatalf("import error result = %#v", importPayload)
	}

	simulateResponse := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "command", "text": "/simulate",
	})
	assertRecorderJSONOK(t, simulateResponse, http.StatusAccepted)
	simulateResult := waitForFrameAfter(t, app.hub, importResult.ID, "command_result")
	var simulatePayload commandResultFrame
	if err := json.Unmarshal(simulateResult.Data, &simulatePayload); err != nil {
		t.Fatal(err)
	}
	if simulatePayload.Command != "simulate" || simulatePayload.Level != "error" || simulatePayload.Error != rt.simErr.Error() || !simulatePayload.Done {
		t.Fatalf("simulate error result = %#v", simulatePayload)
	}
}

func TestCommandDiagPublishesAnonymizedMarkdownAndExportPath(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.dir = t.TempDir()
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "command", "text": "/diag",
	})
	assertRecorderJSONOK(t, response, http.StatusAccepted)
	result := waitForFrame(t, app.hub, "command_result")
	var payload commandResultFrame
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Command != "diag" || payload.Level != "success" || !payload.Done || !strings.Contains(payload.Markdown, "# diag-export") || !strings.Contains(payload.Markdown, "meta/diag-export.md") {
		t.Fatalf("diag result = %#v", payload)
	}
}

func TestSlashCommandCapabilityAndArgumentErrorsUseHTTPErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want int
		msg  string
	}{
		{name: "diag capability", text: "/diag", want: http.StatusNotImplemented, msg: "thư mục"},
		{name: "export capability", text: "/export", want: http.StatusNotImplemented, msg: "xuất"},
		{name: "import capability", text: "/import story.md", want: http.StatusNotImplemented, msg: "nhập"},
		{name: "simulate capability", text: "/simulate", want: http.StatusNotImplemented, msg: "mô phỏng"},
		{name: "simulate args", text: "/simulate now", want: http.StatusBadRequest, msg: "cách dùng"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newServer(newFakeRuntime(), 8)
			t.Cleanup(app.Close)
			response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
				"action": "command", "text": tc.text,
			})
			assertRecorderJSONError(t, response, tc.want, tc.msg)
		})
	}
}

func TestStageCoCreatePublishesOpenerProgressAndResult(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.stageRelease = nil
	rt.stageProgress = []struct{ kind, text string }{
		{kind: host.CoCreateProgressThinking, text: "Đang suy nghĩ kế hoạch"},
		{kind: host.CoCreateProgressReply, text: "Đang trả lời kế hoạch"},
	}
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "command", "text": "/cocreate",
	})
	assertRecorderJSONOK(t, response, http.StatusAccepted)
	first := waitForFrame(t, app.hub, "command_progress")
	second := waitForFrameAfter(t, app.hub, first.ID, "command_progress")
	if first.ID >= second.ID {
		t.Fatalf("co-create progress IDs = %d, %d", first.ID, second.ID)
	}
	result := waitForFrameAfter(t, app.hub, second.ID, "command_result")
	var payload commandResultFrame
	if err := json.Unmarshal(result.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Command != "cocreate" || payload.Markdown != rt.stageReply.Message || payload.Prompt != rt.stageReply.Prompt || !payload.Ready || !reflect.DeepEqual(payload.Suggestions, rt.stageReply.Suggestions) || payload.Level != "success" || !payload.Done {
		t.Fatalf("stage co-create result = %#v", payload)
	}

	rt.mu.Lock()
	history := append([]host.CoCreateMessage(nil), rt.stageHistory...)
	rt.mu.Unlock()
	want := []host.CoCreateMessage{{Role: "user", Content: "Tôi tạm dừng một chút, muốn cùng bạn lên kế hoạch cho hướng đi tiếp theo."}}
	if !reflect.DeepEqual(history, want) {
		t.Fatalf("stage co-create history = %#v, want %#v", history, want)
	}
}

func TestStageCoCreateMessageUsesSameSessionAndStream(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.stageRelease = nil
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	first := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{"action": "command", "text": "/cocreate"})
	assertRecorderJSONOK(t, first, http.StatusAccepted)
	result := waitForFrame(t, app.hub, "command_result")

	second := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "cocreate_message", "text": "Thêm một bước ngoặt ở chương sau",
	})
	assertRecorderJSONOK(t, second, http.StatusAccepted)
	_ = waitForFrameAfter(t, app.hub, result.ID, "command_result")

	rt.mu.Lock()
	history := append([]host.CoCreateMessage(nil), rt.stageHistory...)
	rt.mu.Unlock()
	if len(history) != 3 || history[1].Role != "assistant" || history[2] != (host.CoCreateMessage{Role: "user", Content: "Thêm một bước ngoặt ở chương sau"}) {
		t.Fatalf("stage message history = %#v", history)
	}
}

func TestStageCoCreateApplyAndCancelUseInternalActions(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.stageRelease = nil
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	start := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{"action": "command", "text": "/cocreate"})
	assertRecorderJSONOK(t, start, http.StatusAccepted)
	initialResult := waitForFrame(t, app.hub, "command_result")

	apply := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "cocreate_apply", "text": "Hướng đi đã chốt",
	})
	assertRecorderJSONOK(t, apply, http.StatusAccepted)
	applyResult := waitForFrameAfter(t, app.hub, initialResult.ID, "command_result")
	var applied commandResultFrame
	if err := json.Unmarshal(applyResult.Data, &applied); err != nil {
		t.Fatal(err)
	}
	if applied.Command != "cocreate_apply" || applied.Level != "success" || !applied.Done {
		t.Fatalf("apply result = %#v", applied)
	}
	rt.mu.Lock()
	resumed := append([]string(nil), rt.resumeDrafts...)
	rt.mu.Unlock()
	if !reflect.DeepEqual(resumed, []string{"Hướng đi đã chốt"}) {
		t.Fatalf("resumed drafts = %#v", resumed)
	}

	rt = newCoCreateRuntimeFake()
	rt.stageIgnoreContext = true
	app = newServer(rt, 8)
	t.Cleanup(app.Close)
	start = serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{"action": "command", "text": "/cocreate"})
	assertRecorderJSONOK(t, start, http.StatusAccepted)
	select {
	case <-rt.stageEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stage co-create")
	}
	cancel := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{"action": "cocreate_cancel"})
	assertRecorderJSONOK(t, cancel, http.StatusOK)
	rt.mu.Lock()
	cancelCount := rt.cancelCount
	rt.mu.Unlock()
	if cancelCount != 1 {
		t.Fatalf("cancel count = %d, want 1", cancelCount)
	}
	close(rt.stageRelease)
	time.Sleep(20 * time.Millisecond)
	sub := app.hub.subscribe(0)
	defer sub.Cancel()
	for _, next := range sub.Replay {
		if next.Event == "command_result" && strings.Contains(string(next.Data), "Kế hoạch giai đoạn") {
			t.Fatalf("stale stage result after cancellation: %s", next.Data)
		}
	}
}

func TestColdCoCreateApplyUsesBuildStartPrompt(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.release = nil
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	start := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "start", "mode": "cocreate", "text": "Ý tưởng truyện",
	})
	assertRecorderJSONOK(t, start, http.StatusAccepted)
	initialResult := waitForFrame(t, app.hub, "command_result")

	apply := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "cocreate_apply",
	})
	assertRecorderJSONOK(t, apply, http.StatusAccepted)
	applyResult := waitForFrameAfter(t, app.hub, initialResult.ID, "command_result")
	var payload commandResultFrame
	if err := json.Unmarshal(applyResult.Data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Command != "cocreate_apply" || payload.Level != "success" || !payload.Done {
		t.Fatalf("cold apply result = %#v", payload)
	}
	rt.mu.Lock()
	startPrompts := append([]string(nil), rt.startPrompts...)
	rt.mu.Unlock()
	if !reflect.DeepEqual(startPrompts, []string{host.BuildStartPrompt(rt.reply.Prompt)}) {
		t.Fatalf("cold start prompts = %#v", startPrompts)
	}
}

func TestStageCoCreateRejectsConcurrentRequests(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	first := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{"action": "command", "text": "/cocreate"})
	assertRecorderJSONOK(t, first, http.StatusAccepted)
	select {
	case <-rt.stageEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for stage co-create")
	}
	second := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{"action": "command", "text": "/cocreate"})
	assertRecorderJSONError(t, second, http.StatusConflict, "đang")
	message := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{"action": "cocreate_message", "text": "Tin nhắn thứ hai"})
	assertRecorderJSONError(t, message, http.StatusConflict, "đang")
	close(rt.stageRelease)
}

func lastID(ids []int64) int64 {
	if len(ids) == 0 {
		return 0
	}
	return ids[len(ids)-1]
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

func TestQuickStartRejectsActiveCoCreateSession(t *testing.T) {
	rt := newFakeRuntime()
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	app.coCreateMu.Lock()
	app.coCreateSession = startup.NewCoCreateSession("ý tưởng đang đồng sáng tác")
	app.coCreateMu.Unlock()

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "start", "mode": "quick", "text": "ý tưởng mới",
	})
	assertRecorderJSONError(t, response, http.StatusConflict, "đang có một lượt đồng sáng tác khác")

	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.startPrompts) != 0 {
		t.Fatalf("StartPrepared calls = %#v, want none", rt.startPrompts)
	}
}

func TestColdCoCreateMessageUsesSameSessionAndColdStream(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.release = nil
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	start := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "start", "mode": "cocreate", "text": "Một ý tưởng truyện",
	})
	assertRecorderJSONOK(t, start, http.StatusAccepted)
	initialResult := waitForFrame(t, app.hub, "command_result")

	followUp := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "cocreate_message", "text": "Thêm một bước ngoặt",
	})
	assertRecorderJSONOK(t, followUp, http.StatusAccepted)
	_ = waitForFrameAfter(t, app.hub, initialResult.ID, "command_result")

	rt.mu.Lock()
	history := append([]host.CoCreateMessage(nil), rt.history...)
	stageHistory := append([]host.CoCreateMessage(nil), rt.stageHistory...)
	rt.mu.Unlock()
	want := []host.CoCreateMessage{
		{Role: "user", Content: "Một ý tưởng truyện"},
		{Role: "assistant", Content: rt.reply.Message},
		{Role: "user", Content: "Thêm một bước ngoặt"},
	}
	if !reflect.DeepEqual(history, want) {
		t.Fatalf("cold message history = %#v, want %#v", history, want)
	}
	if len(stageHistory) != 0 {
		t.Fatalf("cold message used staged history = %#v", stageHistory)
	}
}

func TestColdCoCreateSessionRejectsReplacementAfterReply(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.release = nil
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	start := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "start", "mode": "cocreate", "text": "Ý tưởng đầu tiên",
	})
	assertRecorderJSONOK(t, start, http.StatusAccepted)
	_ = waitForFrame(t, app.hub, "command_result")

	replacement := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "start", "mode": "cocreate", "text": "Ý tưởng thay thế",
	})
	assertRecorderJSONError(t, replacement, http.StatusConflict, "đang")
}

func TestCloseWaitsForColdCoCreateAndSuppressesStaleResult(t *testing.T) {
	rt := newCoCreateRuntimeFake()
	rt.finished = make(chan struct{})
	app := newServer(rt, 8)
	t.Cleanup(app.Close)

	response := serveAppJSON(t, app, http.MethodPost, "/commands", map[string]any{
		"action": "start", "mode": "cocreate", "text": "Truyện cần hủy",
	})
	assertRecorderJSONOK(t, response, http.StatusAccepted)
	select {
	case <-rt.entered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for cold co-create to start")
	}

	closed := make(chan struct{})
	go func() {
		app.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("server close did not join cold co-create")
	}
	select {
	case <-rt.finished:
	case <-time.After(time.Second):
		t.Fatal("cold co-create did not exit after cancellation")
	}
	time.Sleep(20 * time.Millisecond)

	subscription := app.hub.subscribe(0)
	defer subscription.Cancel()
	for _, next := range subscription.Replay {
		if next.Event == "command_progress" || next.Event == "command_result" {
			t.Fatalf("stale co-create event after close: %s %s", next.Event, next.Data)
		}
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
