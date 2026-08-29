package webhost

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/internal/tools"
)

type questionFrame struct {
	ID        string
	Questions []tools.Question
}

type answerRequest struct {
	Answers map[string]string `json:"answers"`
	Notes   map[string]string `json:"notes"`
}

var (
	errQuestionNotFound   = errors.New("question not found")
	errQuestionIncomplete = errors.New("question answer incomplete")
)

type pendingQuestion struct {
	frame    *questionFrame
	resultCh chan *tools.AskUserResponse
}

type questionBroker struct {
	mu      sync.Mutex
	publish func(event string, value any)
	nextID  int64
	pending map[string]pendingQuestion
}

func newQuestionBroker(publish func(event string, value any)) *questionBroker {
	return &questionBroker{
		publish: publish,
		pending: make(map[string]pendingQuestion),
	}
}

func (b *questionBroker) handle(ctx context.Context, questions []tools.Question) (*tools.AskUserResponse, error) {
	b.mu.Lock()
	b.nextID++
	frame := &questionFrame{
		ID:        "q-" + strconv.FormatInt(b.nextID, 10),
		Questions: questions,
	}
	pending := pendingQuestion{
		frame:    frame,
		resultCh: make(chan *tools.AskUserResponse, 1),
	}
	b.pending[frame.ID] = pending
	b.mu.Unlock()

	b.publish("question", *frame)
	defer b.removePending(frame.ID)

	select {
	case response := <-pending.resultCh:
		return response, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *questionBroker) answer(id string, request answerRequest) error {
	b.mu.Lock()
	pending, ok := b.pending[id]
	if !ok {
		b.mu.Unlock()
		return errQuestionNotFound
	}
	for _, question := range pending.frame.Questions {
		answer, ok := request.Answers[question.Question]
		if !ok || strings.TrimSpace(answer) == "" {
			b.mu.Unlock()
			return errQuestionIncomplete
		}
	}

	response := &tools.AskUserResponse{
		Answers: copyStringMap(request.Answers),
		Notes:   copyStringMap(request.Notes),
	}
	delete(b.pending, id)
	b.mu.Unlock()

	pending.resultCh <- response
	return nil
}

func (b *questionBroker) pendingFrame() *questionFrame {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, pending := range b.pending {
		frame := *pending.frame
		frame.Questions = append([]tools.Question(nil), pending.frame.Questions...)
		return &frame
	}
	return nil
}

func (b *questionBroker) removePending(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

func copyStringMap(values map[string]string) map[string]string {
	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}
	return copy
}
