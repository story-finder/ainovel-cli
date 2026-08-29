package webhost

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/tools"
)

type publishedQuestion struct {
	event string
	value any
}

type questionResult struct {
	response *tools.AskUserResponse
	err      error
}

func TestQuestionBrokerPublishesAndReturnsSubmittedAnswer(t *testing.T) {
	published := make(chan publishedQuestion, 1)
	broker := newQuestionBroker(func(event string, value any) {
		published <- publishedQuestion{event: event, value: value}
	})
	questions := []tools.Question{
		{
			Question: "Độ dài?",
			Header:   "Độ dài",
			Options: []tools.Option{
				{Label: "Ngắn", Description: "Truyện ngắn"},
				{Label: "Dài", Description: "Truyện dài"},
			},
		},
	}

	resultCh := make(chan questionResult, 1)
	go func() {
		response, err := broker.handle(context.Background(), questions)
		resultCh <- questionResult{response: response, err: err}
	}()

	publishedEvent := receivePublishedQuestion(t, published)
	if publishedEvent.event != "question" {
		t.Fatalf("published event = %q, want %q", publishedEvent.event, "question")
	}
	frame := questionFrameFromValue(t, publishedEvent.value)
	if frame.ID != "q-1" {
		t.Fatalf("frame ID = %q, want %q", frame.ID, "q-1")
	}
	if len(frame.Questions) != 1 || frame.Questions[0].Question != "Độ dài?" {
		t.Fatalf("published questions = %#v, want the Vietnamese question", frame.Questions)
	}
	if got := len(frame.Questions[0].Options); got != 2 {
		t.Fatalf("published option count = %d, want 2", got)
	}

	if err := broker.answer(frame.ID, answerRequest{
		Answers: map[string]string{"Độ dài?": "Dài"},
	}); err != nil {
		t.Fatalf("answer() error = %v, want nil", err)
	}

	result := receiveQuestionResult(t, resultCh)
	if result.err != nil {
		t.Fatalf("handle() error = %v, want nil", result.err)
	}
	if got := result.response.Answers["Độ dài?"]; got != "Dài" {
		t.Fatalf("response answer = %q, want %q", got, "Dài")
	}
	if got := broker.pendingFrame(); got != nil {
		t.Fatalf("pendingFrame() = %#v after answer, want nil", got)
	}
}

func TestQuestionBrokerRejectsUnknownAndIncompleteAnswers(t *testing.T) {
	published := make(chan publishedQuestion, 1)
	broker := newQuestionBroker(func(event string, value any) {
		published <- publishedQuestion{event: event, value: value}
	})
	if err := broker.answer("q-unknown", answerRequest{}); err != errQuestionNotFound {
		t.Fatalf("answer() unknown ID error = %v, want %v", err, errQuestionNotFound)
	}

	questions := []tools.Question{{
		Question: "Bạn chọn gì?",
		Header:   "Lựa chọn",
		Options: []tools.Option{
			{Label: "Một", Description: "Lựa chọn một"},
			{Label: "Hai", Description: "Lựa chọn hai"},
		},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resultCh := make(chan questionResult, 1)
	go func() {
		response, err := broker.handle(ctx, questions)
		resultCh <- questionResult{response: response, err: err}
	}()

	frame := questionFrameFromValue(t, receivePublishedQuestion(t, published).value)
	if err := broker.answer(frame.ID, answerRequest{}); err != errQuestionIncomplete {
		t.Fatalf("answer() incomplete error = %v, want %v", err, errQuestionIncomplete)
	}

	cancel()
	result := receiveQuestionResult(t, resultCh)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("handle() cancellation error = %v, want %v", result.err, context.Canceled)
	}
}

func TestQuestionBrokerReturnsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	broker := newQuestionBroker(func(string, any) {})
	response, err := broker.handle(ctx, []tools.Question{{
		Question: "Tiếp tục?",
		Header:   "Tiếp tục",
		Options: []tools.Option{
			{Label: "Có", Description: "Tiếp tục"},
			{Label: "Không", Description: "Dừng lại"},
		},
	}})
	if response != nil {
		t.Fatalf("handle() response = %#v, want nil", response)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("handle() error = %v, want %v", err, context.Canceled)
	}
	if got := broker.pendingFrame(); got != nil {
		t.Fatalf("pendingFrame() = %#v after cancellation, want nil", got)
	}
}

func receivePublishedQuestion(t *testing.T, published <-chan publishedQuestion) publishedQuestion {
	t.Helper()
	select {
	case event := <-published:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published question")
		return publishedQuestion{}
	}
}

func receiveQuestionResult(t *testing.T, results <-chan questionResult) questionResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for question result")
		return questionResult{}
	}
}

func questionFrameFromValue(t *testing.T, value any) *questionFrame {
	t.Helper()
	switch frame := value.(type) {
	case questionFrame:
		return &frame
	case *questionFrame:
		return frame
	default:
		t.Fatalf("published value has type %T, want questionFrame", value)
		return nil
	}
}
