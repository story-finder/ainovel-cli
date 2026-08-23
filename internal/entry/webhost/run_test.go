package webhost

import (
	"context"
	"testing"
)

type trackingRuntime struct {
	*fakeRuntime
	closed bool
}

func (r *trackingRuntime) Close() {
	r.closed = true
	r.fakeRuntime.Close()
}

func TestServeClosesExistingHostWhenContextEnds(t *testing.T) {
	runtime := &trackingRuntime{fakeRuntime: newFakeRuntime()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := serve(ctx, runtime, "127.0.0.1:0"); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !runtime.closed {
		t.Fatal("runtime.closed = false, want true")
	}
}
