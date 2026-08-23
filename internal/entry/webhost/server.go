package webhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/voocel/ainovel-cli/internal/diag"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/host/exp"
	"github.com/voocel/ainovel-cli/internal/host/imp"
	"github.com/voocel/ainovel-cli/internal/host/sim"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

type runtime interface {
	StartPrepared(string) error
	Resume() (string, error)
	Continue(string) error
	Steer(string)
	Abort() bool
	Events() <-chan host.Event
	Stream() <-chan string
	Done() <-chan struct{}
	Snapshot() host.UISnapshot
	ReplayQueue(int64) ([]domain.RuntimeQueueItem, error)
	AskUser() *tools.AskUserTool
	Close()
}

type modelRuntime interface {
	ConfiguredProviders() []string
	ConfiguredModels(string) []string
	CurrentModelSelection(string) (string, string, bool)
	SwitchModel(string, string, string) error
}

type coCreateRuntime interface {
	CoCreateStream(context.Context, []host.CoCreateMessage, func(string, string)) (host.CoCreateReply, error)
}

type dirRuntime interface {
	Dir() string
}

type exportRuntime interface {
	Export(context.Context, exp.Options) (*exp.Result, error)
}

type importRuntime interface {
	ImportFrom(context.Context, imp.Options) (<-chan imp.Event, error)
}

type simulateRuntime interface {
	Simulate(context.Context) (<-chan sim.Event, error)
}

type pauseCoCreateRuntime interface {
	PauseForCoCreate() bool
}

type stageCoCreateRuntime interface {
	StageCoCreateStream(context.Context, []host.CoCreateMessage, func(string, string)) (host.CoCreateReply, error)
}

type resumeCoCreateRuntime interface {
	ResumeFromCoCreate(string) error
}

type cancelCoCreateRuntime interface {
	CancelCoCreate()
}

type server struct {
	rt                 runtime
	mux                *http.ServeMux
	hub                *eventHub
	broker             *questionBroker
	pumpCancel         context.CancelFunc
	pumpDone           chan struct{}
	closeOnce          sync.Once
	commandMu          sync.Mutex
	commandLifeMu      sync.Mutex
	commandCtx         context.Context
	commandCancel      context.CancelFunc
	commandWG          sync.WaitGroup
	commandClosed      bool
	coCreateMu         sync.Mutex
	coCreateSession    *startup.CoCreateSession
	coCreateCancel     context.CancelFunc
	coCreateReply      host.CoCreateReply
	coCreateActive     bool
	coCreateStage      bool
	coCreateInFlight   bool
	coCreateDone       chan struct{}
	coCreateGeneration uint64
	coCreateClosed     bool
}

func newServer(rt runtime, replayLimit int) *server {
	commandCtx, commandCancel := context.WithCancel(context.Background())
	nextEventID := int64(0)
	if items, err := rt.ReplayQueue(0); err == nil {
		for _, item := range items {
			if item.Seq > nextEventID {
				nextEventID = item.Seq
			}
		}
	}
	s := &server{
		rt:            rt,
		mux:           http.NewServeMux(),
		hub:           newEventHubWithNextID(replayLimit, nextEventID),
		commandCtx:    commandCtx,
		commandCancel: commandCancel,
	}
	s.broker = newQuestionBroker(func(event string, value any) {
		s.hub.publish(event, value)
	})
	rt.AskUser().SetHandler(s.broker.handle)

	s.mux.HandleFunc("/status", s.handleStatus)
	s.mux.HandleFunc("/models", s.handleModels)
	s.mux.HandleFunc("/commands", s.handleCommands)
	s.mux.HandleFunc("/events", s.handleEvents)
	s.mux.HandleFunc("/questions", s.handleNotFound)
	s.mux.HandleFunc("/questions/", s.handleQuestions)
	s.mux.HandleFunc("/", s.handleStatic)
	s.startPump()
	return s
}

func (s *server) Handler() http.Handler { return s.mux }

type commandRequest struct {
	Action   string `json:"action"`
	Text     string `json:"text"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Mode     string `json:"mode"`
}

type statusResponse struct {
	Host     host.UISnapshot `json:"host"`
	Pending  *questionFrame  `json:"pending"`
	CoCreate coCreateStatus  `json:"cocreate"`
}

type coCreateStatus struct {
	Active   bool `json:"active"`
	Stage    bool `json:"stage"`
	InFlight bool `json:"inFlight"`
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{
		Host:     s.rt.Snapshot(),
		Pending:  s.broker.pendingFrame(),
		CoCreate: s.coCreateStatusSnapshot(),
	})
}

func (s *server) coCreateStatusSnapshot() coCreateStatus {
	s.coCreateMu.Lock()
	defer s.coCreateMu.Unlock()
	return coCreateStatus{
		Active:   s.coCreateSession != nil || s.coCreateActive || s.coCreateInFlight,
		Stage:    s.coCreateStage,
		InFlight: s.coCreateInFlight,
	}
}

func (s *server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "phương thức không được hỗ trợ")
		return
	}

	rt, ok := s.rt.(modelRuntime)
	if !ok {
		writeError(w, http.StatusNotImplemented, "runtime hiện tại không hỗ trợ danh mục mô hình")
		return
	}

	role, ok := normalizeWebModelRole(r.URL.Query().Get("role"))
	if !ok {
		writeError(w, http.StatusBadRequest, "vai trò không hợp lệ; chọn default, coordinator, architect, writer hoặc editor")
		return
	}

	response := modelCatalogResponse{
		Role:  role,
		Roles: webModelRoleList(),
	}

	s.commandMu.Lock()
	providers := append([]string(nil), rt.ConfiguredProviders()...)
	sort.Strings(providers)
	response.Providers = make([]modelProviderResponse, 0, len(providers))
	for _, provider := range providers {
		models := append([]string(nil), rt.ConfiguredModels(provider)...)
		if models == nil {
			models = []string{}
		}
		sort.Strings(models)
		response.Providers = append(response.Providers, modelProviderResponse{
			Provider: provider,
			Models:   models,
		})
	}
	provider, model, explicit := rt.CurrentModelSelection(role)
	response.Current = modelSelectionResponse{Provider: provider, Model: model, Explicit: explicit}
	s.commandMu.Unlock()

	writeJSON(w, http.StatusOK, response)
}

func (s *server) handleCommands(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var request commandRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, err)
		return
	}

	s.commandMu.Lock()
	defer s.commandMu.Unlock()

	action := strings.TrimSpace(request.Action)
	text := strings.TrimSpace(request.Text)
	switch action {
	case "command":
		accepted, err := s.handleWebCommand(text, request.Provider, request.Model)
		if err != nil {
			status := webCommandErrorStatus(err)
			writeError(w, status, err.Error())
			return
		}
		status := http.StatusOK
		if accepted {
			status = http.StatusAccepted
		}
		writeJSON(w, status, map[string]any{"ok": true})
		return
	case "cocreate_message", "chat":
		if text == "" {
			writeError(w, http.StatusBadRequest, "văn bản không được để trống")
			return
		}
		accepted, err := s.startCoCreateMessage(text)
		if err != nil {
			writeError(w, webCommandErrorStatus(err), err.Error())
			return
		}
		if !accepted {
			writeError(w, http.StatusConflict, errCoCreateActive.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
		return
	case "cocreate_apply":
		if err := s.applyCoCreate(text); err != nil {
			writeError(w, webCommandErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
		return
	case "cocreate_cancel":
		if err := s.cancelCoCreate(); err != nil {
			writeError(w, webCommandErrorStatus(err), err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	case "start":
		mode := strings.ToLower(strings.TrimSpace(request.Mode))
		if mode == "" {
			mode = "quick"
		}
		if mode == "cocreate" {
			if s.rt.Snapshot().RecoveryLabel != "" {
				writeError(w, http.StatusConflict, "không gian làm việc có tiến độ đã được lưu; hãy dùng tiếp tục khôi phục")
				return
			}
			if text == "" {
				writeError(w, http.StatusBadRequest, "yêu cầu đồng sáng tác không được để trống")
				return
			}
			if err := s.startColdCoCreate(text); err != nil {
				status := http.StatusBadRequest
				switch {
				case errors.Is(err, errCoCreateCapabilityUnavailable):
					status = http.StatusNotImplemented
				case errors.Is(err, errCoCreateActive):
					status = http.StatusConflict
				}
				writeError(w, status, err.Error())
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
			return
		}
		if mode != "quick" {
			writeError(w, http.StatusBadRequest, "chế độ khởi động này chưa được hỗ trợ trên web")
			return
		}
		if s.rt.Snapshot().RecoveryLabel != "" {
			writeError(w, http.StatusConflict, "không gian làm việc có tiến độ đã được lưu; hãy dùng tiếp tục khôi phục")
			return
		}
		if s.coCreateStatusSnapshot().Active {
			writeError(w, http.StatusConflict, errCoCreateActive.Error())
			return
		}
		plan, err := startup.PrepareQuick(startup.Request{
			Mode:        startup.ModeQuick,
			UserPrompt:  text,
			Interactive: true,
		})
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := s.rt.StartPrepared(plan.StartPrompt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	case "resume":
		label, err := s.rt.Resume()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if strings.TrimSpace(label) == "" {
			writeError(w, http.StatusConflict, "không có không gian làm việc đã lưu để tiếp tục khôi phục")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "label": label})
		return
	case "steer", "rewrite":
		if text == "" {
			writeError(w, http.StatusBadRequest, "văn bản không được để trống")
			return
		}
		s.rt.Steer(text)
	case "continue":
		if text == "" {
			writeError(w, http.StatusBadRequest, "văn bản không được để trống")
			return
		}
		if s.coCreateStageActive() {
			if _, err := s.startCoCreateMessage(text); err != nil {
				writeError(w, webCommandErrorStatus(err), err.Error())
				return
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
			return
		}
		if err := s.rt.Continue(text); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	case "pause":
		if !s.rt.Abort() {
			writeError(w, http.StatusConflict, "lượt sáng tác hiện không chạy")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("thao tác không xác định %q", request.Action))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

var errModelCapabilityUnavailable = errors.New("runtime hiện tại không hỗ trợ chuyển mô hình")
var errCoCreateCapabilityUnavailable = errors.New("runtime hiện tại không hỗ trợ đồng sáng tác")
var errDirCapabilityUnavailable = errors.New("runtime hiện tại không hỗ trợ thư mục workspace")
var errExportCapabilityUnavailable = errors.New("runtime hiện tại không hỗ trợ xuất truyện")
var errImportCapabilityUnavailable = errors.New("runtime hiện tại không hỗ trợ nhập truyện")
var errSimulateCapabilityUnavailable = errors.New("runtime hiện tại không hỗ trợ hồ sơ mô phỏng")
var errCoCreateActive = errors.New("đang có một lượt đồng sáng tác khác")
var errCoCreateClosed = errors.New("web host đã đóng")

const coCreateShutdownWait = time.Second

func webCommandErrorStatus(err error) int {
	switch {
	case errors.Is(err, errModelCapabilityUnavailable),
		errors.Is(err, errDirCapabilityUnavailable),
		errors.Is(err, errExportCapabilityUnavailable),
		errors.Is(err, errImportCapabilityUnavailable),
		errors.Is(err, errSimulateCapabilityUnavailable),
		errors.Is(err, errCoCreateCapabilityUnavailable):
		return http.StatusNotImplemented
	case errors.Is(err, errCoCreateActive), errors.Is(err, errCoCreateClosed):
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

func (s *server) launchCommand(run func(context.Context)) error {
	s.commandLifeMu.Lock()
	if s.commandClosed {
		s.commandLifeMu.Unlock()
		return errCoCreateClosed
	}
	ctx := s.commandCtx
	s.commandWG.Add(1)
	s.commandLifeMu.Unlock()

	go func() {
		defer s.commandWG.Done()
		run(ctx)
	}()
	return nil
}

func (s *server) publishCommand(event string, value any) bool {
	s.commandLifeMu.Lock()
	defer s.commandLifeMu.Unlock()
	if s.commandClosed {
		return false
	}
	s.hub.publish(event, value)
	return true
}

func (s *server) publishCommandResult(command, markdown, level string, resultErr error) {
	frame := commandResultFrame{
		Command:  command,
		Markdown: markdown,
		Level:    level,
		Done:     true,
	}
	if resultErr != nil {
		frame.Error = resultErr.Error()
	}
	s.publishCommand("command_result", frame)
}

func (s *server) handleWebCommand(text, provider, model string) (bool, error) {
	command, err := parseSlashCommand(text)
	if err != nil {
		return false, err
	}
	switch command.Name {
	case "model":
		if len(command.Args) > 1 {
			return false, errors.New("lệnh /model chỉ nhận tối đa một vai trò")
		}
		roleArg := ""
		if len(command.Args) == 1 {
			roleArg = command.Args[0]
		}
		role, ok := normalizeWebModelRole(roleArg)
		if !ok {
			return false, fmt.Errorf("vai trò không hợp lệ %q; chọn default, coordinator, architect, writer hoặc editor", roleArg)
		}
		provider = strings.TrimSpace(provider)
		model = strings.TrimSpace(model)
		if provider == "" {
			return false, errors.New("provider không được để trống")
		}
		if model == "" {
			return false, errors.New("model không được để trống")
		}
		rt, ok := s.rt.(modelRuntime)
		if !ok {
			return false, errModelCapabilityUnavailable
		}
		if err := rt.SwitchModel(role, provider, model); err != nil {
			return false, fmt.Errorf("không thể chuyển mô hình: %w", err)
		}
		s.publishCommandResult(text, fmt.Sprintf("Đã chuyển mô hình cho vai trò %s sang %s/%s.", role, provider, model), "success", nil)
		return false, nil
	case "diag":
		if len(command.Args) != 0 {
			return false, errors.New("cách dùng: /diag")
		}
		rt, ok := s.rt.(dirRuntime)
		if !ok {
			return false, errDirCapabilityUnavailable
		}
		return true, s.launchCommand(func(ctx context.Context) { s.runDiag(ctx, rt) })
	case "export":
		opts, err := parseExportArgs(command.Args)
		if err != nil {
			return false, err
		}
		rt, ok := s.rt.(exportRuntime)
		if !ok {
			return false, errExportCapabilityUnavailable
		}
		return true, s.launchCommand(func(ctx context.Context) { s.runExport(ctx, rt, opts) })
	case "import":
		opts, err := parseImportArgs(command.Args)
		if err != nil {
			return false, err
		}
		rt, ok := s.rt.(importRuntime)
		if !ok {
			return false, errImportCapabilityUnavailable
		}
		return true, s.launchCommand(func(ctx context.Context) { s.runImport(ctx, rt, opts) })
	case "simulate":
		if len(command.Args) != 0 {
			return false, errors.New("cách dùng: /simulate")
		}
		rt, ok := s.rt.(simulateRuntime)
		if !ok {
			return false, errSimulateCapabilityUnavailable
		}
		return true, s.launchCommand(func(ctx context.Context) { s.runSimulate(ctx, rt) })
	case "cocreate":
		if len(command.Args) != 0 {
			return false, errors.New("cách dùng: /cocreate")
		}
		return true, s.startStageCoCreate()
	default:
		return false, fmt.Errorf("lệnh /%s chưa được hỗ trợ trên web", command.Name)
	}
}

func (s *server) runDiag(ctx context.Context, rt dirRuntime) {
	if ctx.Err() != nil {
		return
	}
	workspace := store.NewStore(rt.Dir())
	report, capture := diag.Diagnose(workspace)
	path, err := diag.WriteExport(workspace, report, capture)
	if err != nil {
		s.publishCommandResult("diag", fmt.Sprintf("Chẩn đoán hoàn tất nhưng không thể ghi báo cáo: %v", err), "error", err)
		return
	}
	markdown := string(diag.RenderExport(report, capture))
	markdown += fmt.Sprintf("\n\n**Đường dẫn báo cáo:** `%s`", path)
	s.publishCommandResult("diag", markdown, "success", nil)
}

func (s *server) runExport(parent context.Context, rt exportRuntime, opts exp.Options) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	result, err := rt.Export(ctx, opts)
	if err != nil {
		s.publishCommandResult("export", fmt.Sprintf("Xuất truyện thất bại: %v", err), "error", err)
		return
	}
	if result == nil {
		err := errors.New("export trả về kết quả rỗng")
		s.publishCommandResult("export", "Xuất truyện thất bại: kết quả rỗng.", "error", err)
		return
	}
	var skipped string
	if len(result.Skipped) > 0 {
		skipped = fmt.Sprintf("; bỏ qua %d chương chưa hoàn thành: %s", len(result.Skipped), briefIntList(result.Skipped, 5))
	}
	markdown := fmt.Sprintf("Đã xuất truyện %d chương, %s sang `%s`%s.", result.Chapters, humanBytes(result.Bytes), result.Path, skipped)
	s.publishCommandResult("export", markdown, "success", nil)
}

func (s *server) runImport(ctx context.Context, rt importRuntime, opts imp.Options) {
	stream, err := rt.ImportFrom(ctx, opts)
	if err != nil {
		s.publishCommandResult("import", fmt.Sprintf("Nhập truyện thất bại: %v", err), "error", err)
		return
	}
	if stream == nil {
		err := errors.New("import trả về luồng sự kiện rỗng")
		s.publishCommandResult("import", "Nhập truyện thất bại: luồng sự kiện rỗng.", "error", err)
		return
	}

	terminal := false
	for event := range stream {
		level := "info"
		text := event.Message
		if event.Err != nil {
			level = "error"
			text = strings.TrimSpace(strings.TrimSpace(text) + ": " + event.Err.Error())
		}
		s.publishCommand("command_progress", commandProgressFrame{
			Command: "import",
			Text:    text,
			Stage:   string(event.Stage),
			Current: event.Current,
			Total:   event.Total,
			Level:   level,
		})
		if terminal {
			continue
		}
		switch event.Stage {
		case imp.StageError:
			if event.Err == nil {
				event.Err = errors.New("quy trình nhập kết thúc với lỗi")
			}
			s.publishCommandResult("import", fmt.Sprintf("Nhập truyện thất bại: %v", event.Err), "error", event.Err)
			terminal = true
		case imp.StageDone:
			s.publishCommandResult("import", fmt.Sprintf("Đã nhập truyện từ `%s`.", opts.SourcePath), "success", nil)
			terminal = true
		}
	}
	if !terminal {
		if err := ctx.Err(); err != nil {
			s.publishCommandResult("import", fmt.Sprintf("Nhập truyện thất bại: %v", err), "error", err)
			return
		}
		err := errors.New("luồng nhập kết thúc trước khi hoàn tất")
		s.publishCommandResult("import", "Nhập truyện thất bại: luồng kết thúc trước khi hoàn tất.", "error", err)
	}
}

func (s *server) runSimulate(ctx context.Context, rt simulateRuntime) {
	stream, err := rt.Simulate(ctx)
	if err != nil {
		s.publishCommandResult("simulate", fmt.Sprintf("Tạo hồ sơ mô phỏng thất bại: %v", err), "error", err)
		return
	}
	if stream == nil {
		err := errors.New("simulate trả về luồng sự kiện rỗng")
		s.publishCommandResult("simulate", "Tạo hồ sơ mô phỏng thất bại: luồng sự kiện rỗng.", "error", err)
		return
	}

	terminal := false
	for event := range stream {
		level := "info"
		text := event.Message
		if event.Err != nil {
			level = "error"
			text = strings.TrimSpace(strings.TrimSpace(text) + ": " + event.Err.Error())
		}
		s.publishCommand("command_progress", commandProgressFrame{
			Command: "simulate",
			Text:    text,
			Stage:   string(event.Stage),
			Current: event.Current,
			Total:   event.Total,
			Level:   level,
		})
		if terminal {
			continue
		}
		switch event.Stage {
		case sim.StageError:
			if event.Err == nil {
				event.Err = errors.New("quy trình mô phỏng kết thúc với lỗi")
			}
			s.publishCommandResult("simulate", fmt.Sprintf("Tạo hồ sơ mô phỏng thất bại: %v", event.Err), "error", event.Err)
			terminal = true
		case sim.StageDone:
			s.publishCommandResult("simulate", "Đã tạo hoặc cập nhật hồ sơ mô phỏng.", "success", nil)
			terminal = true
		}
	}
	if !terminal {
		if err := ctx.Err(); err != nil {
			s.publishCommandResult("simulate", fmt.Sprintf("Tạo hồ sơ mô phỏng thất bại: %v", err), "error", err)
			return
		}
		err := errors.New("luồng mô phỏng kết thúc trước khi hoàn tất")
		s.publishCommandResult("simulate", "Tạo hồ sơ mô phỏng thất bại: luồng kết thúc trước khi hoàn tất.", "error", err)
	}
}

const stageCoCreateOpener = "Tôi tạm dừng một chút, muốn cùng bạn lên kế hoạch cho hướng đi tiếp theo."

func (s *server) startColdCoCreate(initial string) error {
	rt, ok := s.rt.(coCreateRuntime)
	if !ok {
		return errCoCreateCapabilityUnavailable
	}

	ctx, cancel := context.WithCancel(s.commandCtx)
	session := startup.NewCoCreateSession(initial)
	s.coCreateMu.Lock()
	if s.coCreateClosed {
		s.coCreateMu.Unlock()
		cancel()
		return errCoCreateClosed
	}
	if s.coCreateSession != nil || s.coCreateActive || s.coCreateInFlight {
		s.coCreateMu.Unlock()
		cancel()
		return errCoCreateActive
	}
	s.coCreateGeneration++
	generation := s.coCreateGeneration
	done := make(chan struct{})
	s.coCreateActive = true
	s.coCreateStage = false
	s.coCreateInFlight = true
	s.coCreateSession = session
	s.coCreateCancel = cancel
	s.coCreateReply = host.CoCreateReply{}
	s.coCreateDone = done
	s.coCreateMu.Unlock()

	if err := s.launchCommand(func(_ context.Context) {
		defer close(done)
		defer cancel()
		s.runColdCoCreate(ctx, rt, session, generation)
	}); err != nil {
		cancel()
		close(done)
		s.coCreateMu.Lock()
		if s.coCreateSession == session {
			s.coCreateActive = false
			s.coCreateInFlight = false
			s.coCreateCancel = nil
		}
		s.coCreateMu.Unlock()
		return err
	}
	return nil
}

func (s *server) startStageCoCreate() error {
	streamRT, ok := s.rt.(stageCoCreateRuntime)
	if !ok {
		return errCoCreateCapabilityUnavailable
	}
	pauseRT, ok := s.rt.(pauseCoCreateRuntime)
	if !ok {
		return errCoCreateCapabilityUnavailable
	}
	s.coCreateMu.Lock()
	if s.coCreateClosed {
		s.coCreateMu.Unlock()
		return errCoCreateClosed
	}
	if s.coCreateActive || s.coCreateInFlight || s.coCreateSession != nil {
		s.coCreateMu.Unlock()
		return errCoCreateActive
	}
	s.coCreateMu.Unlock()
	if !pauseRT.PauseForCoCreate() {
		return errCoCreateActive
	}

	ctx, cancel := context.WithCancel(s.commandCtx)
	session := startup.NewCoCreateSession(stageCoCreateOpener)
	s.coCreateMu.Lock()
	if s.coCreateClosed {
		s.coCreateMu.Unlock()
		cancel()
		if rt, ok := s.rt.(cancelCoCreateRuntime); ok {
			rt.CancelCoCreate()
		}
		return errCoCreateClosed
	}
	if s.coCreateActive || s.coCreateInFlight || s.coCreateSession != nil {
		s.coCreateMu.Unlock()
		cancel()
		if rt, ok := s.rt.(cancelCoCreateRuntime); ok {
			rt.CancelCoCreate()
		}
		return errCoCreateActive
	}
	s.coCreateGeneration++
	generation := s.coCreateGeneration
	done := make(chan struct{})
	s.coCreateActive = true
	s.coCreateStage = true
	s.coCreateInFlight = true
	s.coCreateSession = session
	s.coCreateCancel = cancel
	s.coCreateReply = host.CoCreateReply{}
	s.coCreateDone = done
	s.coCreateMu.Unlock()

	if err := s.launchCommand(func(_ context.Context) {
		defer close(done)
		defer cancel()
		s.runStageCoCreate(ctx, streamRT, session, generation)
	}); err != nil {
		cancel()
		close(done)
		s.coCreateMu.Lock()
		if s.coCreateSession == session {
			s.coCreateActive = false
			s.coCreateInFlight = false
			s.coCreateCancel = nil
		}
		s.coCreateMu.Unlock()
		return err
	}
	return nil
}

func (s *server) runColdCoCreate(ctx context.Context, rt coCreateRuntime, session *startup.CoCreateSession, generation uint64) {
	reply, err := rt.CoCreateStream(ctx, session.History(), func(kind, text string) {
		s.publishCoCreate(generation, session, commandProgressFrame{
			Command: "cocreate",
			Text:    text,
			Stage:   kind,
			Level:   "info",
		})
	})

	s.commandLifeMu.Lock()
	s.coCreateMu.Lock()
	defer s.coCreateMu.Unlock()
	defer s.commandLifeMu.Unlock()
	if !s.coCreateCurrentLocked(generation, session) {
		return
	}
	if err == nil {
		session.ApplyReply(reply)
		s.coCreateReply = reply
	}
	s.coCreateActive = false
	s.coCreateInFlight = false
	s.coCreateCancel = nil
	if err != nil {
		s.hub.publish("command_result", commandResultFrame{
			Command:  "cocreate",
			Markdown: fmt.Sprintf("Đồng sáng tác thất bại: %v", err),
			Error:    err.Error(),
			Level:    "error",
			Done:     true,
		})
		return
	}
	s.hub.publish("command_result", commandResultFrame{
		Command:     "cocreate",
		Markdown:    reply.Message,
		Prompt:      reply.Prompt,
		Ready:       reply.Ready,
		Suggestions: append([]string(nil), reply.Suggestions...),
		Level:       "success",
		Done:        true,
	})
}

func (s *server) runStageCoCreate(ctx context.Context, rt stageCoCreateRuntime, session *startup.CoCreateSession, generation uint64) {
	reply, err := rt.StageCoCreateStream(ctx, session.History(), func(kind, text string) {
		s.publishCoCreate(generation, session, commandProgressFrame{
			Command: "cocreate",
			Text:    text,
			Stage:   kind,
			Level:   "info",
		})
	})

	s.commandLifeMu.Lock()
	s.coCreateMu.Lock()
	defer s.coCreateMu.Unlock()
	defer s.commandLifeMu.Unlock()
	if !s.coCreateCurrentLocked(generation, session) {
		return
	}
	if err == nil {
		session.ApplyReply(reply)
		s.coCreateReply = reply
	}
	s.coCreateInFlight = false
	s.coCreateCancel = nil
	if err != nil {
		s.hub.publish("command_result", commandResultFrame{
			Command:  "cocreate",
			Markdown: fmt.Sprintf("Đồng sáng tác giai đoạn thất bại: %v", err),
			Error:    err.Error(),
			Level:    "error",
			Done:     true,
		})
		return
	}
	s.hub.publish("command_result", commandResultFrame{
		Command:     "cocreate",
		Markdown:    reply.Message,
		Prompt:      reply.Prompt,
		Ready:       reply.Ready,
		Suggestions: append([]string(nil), reply.Suggestions...),
		Level:       "success",
		Done:        true,
	})
}

func (s *server) publishCoCreate(generation uint64, session *startup.CoCreateSession, progress commandProgressFrame) bool {
	s.commandLifeMu.Lock()
	defer s.commandLifeMu.Unlock()
	s.coCreateMu.Lock()
	defer s.coCreateMu.Unlock()
	if !s.coCreateCurrentLocked(generation, session) {
		return false
	}
	s.hub.publish("command_progress", progress)
	return true
}

func (s *server) coCreateCurrentLocked(generation uint64, session *startup.CoCreateSession) bool {
	return !s.coCreateClosed && s.coCreateGeneration == generation && s.coCreateSession == session && s.coCreateInFlight
}

func (s *server) coCreateStageActive() bool {
	s.coCreateMu.Lock()
	defer s.coCreateMu.Unlock()
	return s.coCreateStage && s.coCreateActive && s.coCreateSession != nil
}

func (s *server) startCoCreateMessage(text string) (bool, error) {
	coldRT, coldCapable := s.rt.(coCreateRuntime)
	stageRT, stageCapable := s.rt.(stageCoCreateRuntime)
	ctx, cancel := context.WithCancel(s.commandCtx)
	s.coCreateMu.Lock()
	if s.coCreateClosed {
		s.coCreateMu.Unlock()
		cancel()
		return false, errCoCreateClosed
	}
	if s.coCreateSession == nil || s.coCreateInFlight || (s.coCreateStage && !s.coCreateActive) {
		s.coCreateMu.Unlock()
		cancel()
		return false, errCoCreateActive
	}
	stage := s.coCreateStage
	if (stage && !stageCapable) || (!stage && !coldCapable) {
		s.coCreateMu.Unlock()
		cancel()
		return false, errCoCreateCapabilityUnavailable
	}
	s.coCreateSession.AppendUser(text)
	s.coCreateGeneration++
	generation := s.coCreateGeneration
	session := s.coCreateSession
	done := make(chan struct{})
	history := session.History()
	s.coCreateInFlight = true
	s.coCreateCancel = cancel
	s.coCreateDone = done
	s.coCreateMu.Unlock()

	if err := s.launchCommand(func(_ context.Context) {
		defer close(done)
		defer cancel()
		stream := func(context.Context, []host.CoCreateMessage, func(string, string)) (host.CoCreateReply, error) {
			return host.CoCreateReply{}, errCoCreateCapabilityUnavailable
		}
		if stage {
			stream = stageRT.StageCoCreateStream
		} else {
			stream = coldRT.CoCreateStream
		}
		reply, streamErr := stream(ctx, history, func(kind, text string) {
			s.publishCoCreate(generation, session, commandProgressFrame{
				Command: "cocreate",
				Text:    text,
				Stage:   kind,
				Level:   "info",
			})
		})

		s.commandLifeMu.Lock()
		s.coCreateMu.Lock()
		defer s.coCreateMu.Unlock()
		defer s.commandLifeMu.Unlock()
		if !s.coCreateCurrentLocked(generation, session) {
			return
		}
		if streamErr == nil {
			session.ApplyReply(reply)
			s.coCreateReply = reply
		}
		s.coCreateInFlight = false
		s.coCreateCancel = nil
		if streamErr != nil {
			prefix := "Đồng sáng tác"
			if stage {
				prefix = "Đồng sáng tác giai đoạn"
			}
			s.hub.publish("command_result", commandResultFrame{
				Command:  "cocreate",
				Markdown: fmt.Sprintf("%s thất bại: %v", prefix, streamErr),
				Error:    streamErr.Error(),
				Level:    "error",
				Done:     true,
			})
			return
		}
		s.hub.publish("command_result", commandResultFrame{
			Command:     "cocreate",
			Markdown:    reply.Message,
			Prompt:      reply.Prompt,
			Ready:       reply.Ready,
			Suggestions: append([]string(nil), reply.Suggestions...),
			Level:       "success",
			Done:        true,
		})
	}); err != nil {
		cancel()
		close(done)
		s.coCreateMu.Lock()
		if s.coCreateSession == session && s.coCreateGeneration == generation {
			s.coCreateInFlight = false
			s.coCreateCancel = nil
		}
		s.coCreateMu.Unlock()
		return false, err
	}
	return true, nil
}

func (s *server) applyCoCreate(text string) error {
	stageRT, stageCapable := s.rt.(resumeCoCreateRuntime)
	s.coCreateMu.Lock()
	if s.coCreateClosed || s.coCreateSession == nil || (s.coCreateStage && !s.coCreateActive) || s.coCreateInFlight {
		s.coCreateMu.Unlock()
		return errCoCreateActive
	}
	draft := strings.TrimSpace(text)
	if draft == "" {
		draft = strings.TrimSpace(s.coCreateSession.DraftPrompt())
	}
	if draft == "" {
		s.coCreateMu.Unlock()
		return errors.New("draft không được để trống")
	}
	stage := s.coCreateStage
	if stage && !stageCapable {
		s.coCreateMu.Unlock()
		return errCoCreateCapabilityUnavailable
	}
	session := s.coCreateSession
	s.coCreateGeneration++
	generation := s.coCreateGeneration
	done := make(chan struct{})
	s.coCreateInFlight = true
	s.coCreateCancel = nil
	s.coCreateDone = done
	s.coCreateMu.Unlock()

	if stage {
		if err := s.launchCommand(func(_ context.Context) {
			defer close(done)
			err := stageRT.ResumeFromCoCreate(draft)
			s.finishCoCreateApply(generation, session, "cocreate_apply", err, true)
		}); err != nil {
			close(done)
			s.coCreateMu.Lock()
			if s.coCreateSession == session && s.coCreateGeneration == generation {
				s.coCreateInFlight = false
			}
			s.coCreateMu.Unlock()
			return err
		}
		return nil
	}

	if err := s.launchCommand(func(_ context.Context) {
		defer close(done)
		err := s.rt.StartPrepared(host.BuildStartPrompt(draft))
		s.finishCoCreateApply(generation, session, "cocreate_apply", err, false)
	}); err != nil {
		close(done)
		s.coCreateMu.Lock()
		if s.coCreateSession == session && s.coCreateGeneration == generation {
			s.coCreateInFlight = false
		}
		s.coCreateMu.Unlock()
		return err
	}
	return nil
}

func (s *server) finishCoCreateApply(generation uint64, session *startup.CoCreateSession, command string, err error, stage bool) {
	s.commandLifeMu.Lock()
	defer s.commandLifeMu.Unlock()
	s.coCreateMu.Lock()
	defer s.coCreateMu.Unlock()
	if !s.coCreateCurrentLocked(generation, session) {
		return
	}
	s.coCreateInFlight = false
	s.coCreateCancel = nil
	if err == nil {
		s.coCreateActive = false
		s.coCreateStage = false
		s.coCreateSession = nil
		s.coCreateReply = host.CoCreateReply{}
		if stage {
			s.hub.publish("command_result", commandResultFrame{Command: command, Markdown: "Đã áp dụng hướng đi đồng sáng tác và tiếp tục sáng tác.", Level: "success", Done: true})
		} else {
			s.hub.publish("command_result", commandResultFrame{Command: command, Markdown: "Đã áp dụng bản nháp và bắt đầu sáng tác.", Level: "success", Done: true})
		}
		return
	}
	if stage {
		s.coCreateActive = true
	}
	s.hub.publish("command_result", commandResultFrame{
		Command:  command,
		Markdown: fmt.Sprintf("Áp dụng bản nháp thất bại: %v", err),
		Error:    err.Error(),
		Level:    "error",
		Done:     true,
	})
}

func (s *server) cancelCoCreate() error {
	s.coCreateMu.Lock()
	if s.coCreateSession == nil || s.coCreateClosed {
		s.coCreateMu.Unlock()
		return errCoCreateActive
	}
	s.coCreateGeneration++
	cancel := s.coCreateCancel
	stage := s.coCreateStage
	s.coCreateActive = false
	s.coCreateInFlight = false
	s.coCreateSession = nil
	s.coCreateReply = host.CoCreateReply{}
	s.coCreateCancel = nil
	s.coCreateMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if stage {
		if rt, ok := s.rt.(cancelCoCreateRuntime); ok {
			rt.CancelCoCreate()
		}
	}
	return nil
}

func (s *server) shutdownColdCoCreate() {
	s.coCreateMu.Lock()
	s.coCreateClosed = true
	s.coCreateGeneration++
	cancel := s.coCreateCancel
	done := s.coCreateDone
	stage := s.coCreateStage
	if cancel != nil {
		cancel()
	}
	s.coCreateMu.Unlock()

	if stage {
		if rt, ok := s.rt.(cancelCoCreateRuntime); ok {
			rt.CancelCoCreate()
		}
	}
	if done == nil {
		return
	}
	timer := time.NewTimer(coCreateShutdownWait)
	defer timer.Stop()
	select {
	case <-done:
		s.coCreateMu.Lock()
		if s.coCreateDone == done {
			s.coCreateActive = false
			s.coCreateInFlight = false
			s.coCreateCancel = nil
		}
		s.coCreateMu.Unlock()
	case <-timer.C:
		// The core owns the stream and may not observe cancellation immediately.
		// The closed/generation guard prevents any late callback from reaching the hub.
	}
}

func (s *server) shutdownCommands() {
	s.commandLifeMu.Lock()
	if !s.commandClosed {
		s.commandClosed = true
		s.commandCancel()
	}
	s.commandLifeMu.Unlock()

	done := make(chan struct{})
	go func() {
		s.commandWG.Wait()
		close(done)
	}()
	timer := time.NewTimer(coCreateShutdownWait)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		// Non-cooperative core methods may outlive Close; publishCommand guards them.
	}
}

func (s *server) handleQuestions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	const prefix = "/questions/"
	path := strings.TrimPrefix(r.URL.Path, prefix)
	if path == r.URL.Path || !strings.HasSuffix(path, "/answer") {
		writeError(w, http.StatusNotFound, errQuestionNotFound.Error())
		return
	}
	id := strings.TrimSuffix(path, "/answer")
	id = strings.TrimSuffix(id, "/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, errQuestionNotFound.Error())
		return
	}

	var request answerRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := s.broker.answer(id, request); err != nil {
		switch {
		case errors.Is(err, errQuestionNotFound):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, errQuestionIncomplete):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not found")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var sizeError *http.MaxBytesError
	if errors.As(err, &sizeError) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
		return
	}
	writeError(w, http.StatusBadRequest, "malformed JSON")
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
