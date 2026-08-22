package webhost

import (
	"testing"
	"time"
)

func TestHubReplaysFramesAfterLastID(t *testing.T) {
	hub := newEventHub(3)
	first := hub.publish("host_event", "one")
	hub.publish("stream_delta", "two")
	third := hub.publish("terminal", "paused")

	sub := hub.subscribe(first.ID)
	defer sub.Cancel()

	if sub.Reset {
		t.Fatal("subscribe unexpectedly requested a reset")
	}
	if len(sub.Replay) != 2 {
		t.Fatalf("Replay length = %d, want 2", len(sub.Replay))
	}
	if got, want := sub.Replay[0].ID, first.ID+1; got != want {
		t.Errorf("Replay[0].ID = %d, want %d", got, want)
	}
	if got, want := sub.Replay[1].ID, third.ID; got != want {
		t.Errorf("Replay[1].ID = %d, want %d", got, want)
	}
}

func TestHubRequestsResetWhenLastIDPredatesBuffer(t *testing.T) {
	hub := newEventHub(2)
	for i := 0; i < 4; i++ {
		hub.publish("host_event", i)
	}

	sub := hub.subscribe(1)
	defer sub.Cancel()

	if !sub.Reset {
		t.Fatal("subscribe did not request a reset")
	}
	if len(sub.Replay) != 0 {
		t.Fatalf("Replay length = %d, want 0", len(sub.Replay))
	}
}

func TestHubFansOutWithoutBlockingPublisher(t *testing.T) {
	hub := newEventHub(8)
	first := hub.subscribe(0)
	defer first.Cancel()
	second := hub.subscribe(0)
	defer second.Cancel()

	published := hub.publish("stream_delta", "chunk")
	if got := (<-first.Frames).ID; got != published.ID {
		t.Errorf("first subscriber received ID %d, want %d", got, published.ID)
	}
	if got := (<-second.Frames).ID; got != published.ID {
		t.Errorf("second subscriber received ID %d, want %d", got, published.ID)
	}
}

func TestHubClosesSlowSubscriberInsteadOfBlockingPublisher(t *testing.T) {
	hub := newEventHub(1)
	sub := hub.subscribe(0)

	hub.publish("stream_delta", "first")
	publishDone := make(chan struct{})
	go func() {
		hub.publish("stream_delta", "second")
		close(publishDone)
	}()

	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("publisher blocked on a slow subscriber")
	}

	if got, ok := <-sub.Frames; !ok || got.ID != 1 {
		t.Fatalf("first receive = (%+v, %t), want frame ID 1", got, ok)
	}
	if _, ok := <-sub.Frames; ok {
		t.Fatal("slow subscriber channel remained open")
	}

	sub.Cancel()
}
