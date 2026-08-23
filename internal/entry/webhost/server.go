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

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/entry/startup"
	"github.com/voocel/ainovel-cli/internal/host"
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

type server struct {
	rt         runtime
	mux        *http.ServeMux
	hub        *eventHub
	broker     *questionBroker
	pumpCancel context.CancelFunc
	pumpDone   chan struct{}
	closeOnce  sync.Once
	commandMu  sync.Mutex
}

func newServer(rt runtime, replayLimit int) *server {
	s := &server{
		rt:  rt,
		mux: http.NewServeMux(),
		hub: newEventHub(replayLimit),
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
	Host    host.UISnapshot `json:"host"`
	Pending *questionFrame  `json:"pending"`
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, statusResponse{
		Host:    s.rt.Snapshot(),
		Pending: s.broker.pendingFrame(),
	})
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

	providers := append([]string(nil), rt.ConfiguredProviders()...)
	sort.Strings(providers)
	response := modelCatalogResponse{
		Role:      role,
		Roles:     webModelRoleList(),
		Providers: make([]modelProviderResponse, 0, len(providers)),
	}
	for _, provider := range providers {
		models := append([]string(nil), rt.ConfiguredModels(provider)...)
		if models == nil {
			models = []string{}
		}
		response.Providers = append(response.Providers, modelProviderResponse{
			Provider: provider,
			Models:   models,
		})
	}
	provider, model, explicit := rt.CurrentModelSelection(role)
	response.Current = modelSelectionResponse{Provider: provider, Model: model, Explicit: explicit}
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
		if err := s.handleWebCommand(text, request.Provider, request.Model); err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, errModelCapabilityUnavailable) {
				status = http.StatusNotImplemented
			}
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	case "start":
		mode := strings.ToLower(strings.TrimSpace(request.Mode))
		if mode == "" {
			mode = "quick"
		}
		if mode != "quick" {
			writeError(w, http.StatusBadRequest, "chế độ khởi động này chưa được hỗ trợ trên web")
			return
		}
		if s.rt.Snapshot().RecoveryLabel != "" {
			writeError(w, http.StatusConflict, "workspace has saved progress; use resume")
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
			writeError(w, http.StatusConflict, "no saved workspace to resume")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "label": label})
		return
	case "steer", "rewrite":
		if text == "" {
			writeError(w, http.StatusBadRequest, "text is required")
			return
		}
		s.rt.Steer(text)
	case "continue":
		if text == "" {
			writeError(w, http.StatusBadRequest, "text is required")
			return
		}
		if err := s.rt.Continue(text); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	case "pause":
		if !s.rt.Abort() {
			writeError(w, http.StatusConflict, "host is not running")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown action %q", request.Action))
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

var errModelCapabilityUnavailable = errors.New("runtime hiện tại không hỗ trợ chuyển mô hình")

func (s *server) handleWebCommand(text, provider, model string) error {
	command, err := parseSlashCommand(text)
	if err != nil {
		return err
	}
	if command.Name != "model" {
		return fmt.Errorf("lệnh /%s chưa được hỗ trợ trên web", command.Name)
	}
	if len(command.Args) != 1 {
		return errors.New("lệnh /model cần đúng một vai trò")
	}
	role, ok := normalizeWebModelRole(command.Args[0])
	if !ok {
		return fmt.Errorf("vai trò không hợp lệ %q; chọn default, coordinator, architect, writer hoặc editor", command.Args[0])
	}
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" {
		return errors.New("provider không được để trống")
	}
	if model == "" {
		return errors.New("model không được để trống")
	}

	rt, ok := s.rt.(modelRuntime)
	if !ok {
		return errModelCapabilityUnavailable
	}
	if err := rt.SwitchModel(role, provider, model); err != nil {
		return fmt.Errorf("không thể chuyển mô hình: %w", err)
	}

	s.hub.publish("command_result", commandResultFrame{
		Command:  text,
		Markdown: fmt.Sprintf("Đã chuyển mô hình cho vai trò %s sang %s/%s.", role, provider, model),
		Level:    "success",
		Done:     true,
	})
	return nil
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
