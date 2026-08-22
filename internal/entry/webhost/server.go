package webhost

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

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

type server struct {
	rt     runtime
	mux    *http.ServeMux
	hub    *eventHub
	broker *questionBroker
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
	s.mux.HandleFunc("/commands", s.handleCommands)
	s.mux.HandleFunc("/questions", s.handleNotFound)
	s.mux.HandleFunc("/questions/", s.handleQuestions)
	s.mux.HandleFunc("/", s.handleNotFound)
	return s
}

func (s *server) Handler() http.Handler { return s.mux }

type commandRequest struct {
	Action string `json:"action"`
	Text   string `json:"text"`
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

	action := strings.TrimSpace(request.Action)
	text := strings.TrimSpace(request.Text)
	switch action {
	case "start":
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
