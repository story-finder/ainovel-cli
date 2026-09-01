package webhost

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/voocel/ainovel-cli/internal/host"
)

type trackingRuntime struct {
	*fakeRuntime
	closed                         bool
	observeClose                   func()
	serverClosedBeforeRuntimeClose bool
}

func (r *trackingRuntime) Close() {
	r.closed = true
	if r.observeClose != nil {
		r.observeClose()
	}
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

func TestRunRejectsInvalidAddressBeforeLoadingConfig(t *testing.T) {
	missingConfig := filepath.Join(t.TempDir(), "missing.json")
	err := Run(context.Background(), Options{
		ConfigPath: missingConfig,
		Addr:       "foo bar:8080",
	})
	if err == nil {
		t.Fatal("Run invalid address error = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "invalid address") {
		t.Fatalf("Run invalid address error = %q, want invalid address", err)
	}
	if strings.Contains(err.Error(), "load config") {
		t.Fatalf("Run invalid address error = %q, want validation before config loading", err)
	}
}

func TestStartHostedOperationContinueUsesExactInstruction(t *testing.T) {
	rt := newFakeRuntime()
	err := startHostedOperation(rt, Options{
		StartupOperation: "continue",
		Instruction:      "Đổi góc nhìn sang nhân vật An.",
	})
	if err != nil {
		t.Fatalf("startHostedOperation continue: %v", err)
	}
	want := []string{"Đổi góc nhìn sang nhân vật An."}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if !reflect.DeepEqual(rt.continueTexts, want) {
		t.Fatalf("Continue calls = %v, want %v", rt.continueTexts, want)
	}
	if !reflect.DeepEqual(rt.calls, []string{"continue"}) {
		t.Fatalf("calls = %v, want [continue]", rt.calls)
	}
}

func TestStartHostedOperationStartRequiresInstructionAndCallsStartPrepared(t *testing.T) {
	t.Run("missing instruction is rejected", func(t *testing.T) {
		rt := newFakeRuntime()
		err := startHostedOperation(rt, Options{StartupOperation: "start"})
		if err == nil {
			t.Fatal("startHostedOperation start without instruction: err = nil, want validation error")
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if len(rt.calls) != 0 {
			t.Fatalf("calls = %v, want no runtime calls", rt.calls)
		}
	})

	t.Run("whitespace instruction is rejected", func(t *testing.T) {
		rt := newFakeRuntime()
		err := startHostedOperation(rt, Options{StartupOperation: "start", Instruction: "   "})
		if err == nil {
			t.Fatal("startHostedOperation start with whitespace instruction: err = nil, want validation error")
		}
	})

	t.Run("instruction is wrapped by BuildStartPrompt", func(t *testing.T) {
		rt := newFakeRuntime()
		err := startHostedOperation(rt, Options{
			StartupOperation: "start",
			Instruction:      "Viết truyện về một hành trình xuyên Việt.",
		})
		if err != nil {
			t.Fatalf("startHostedOperation start: %v", err)
		}
		want := []string{host.BuildStartPrompt("Viết truyện về một hành trình xuyên Việt.")}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if !reflect.DeepEqual(rt.startPrompts, want) {
			t.Fatalf("StartPrepared prompts = %v, want %v", rt.startPrompts, want)
		}
	})
}

func TestStartHostedOperationResumeFailsOnBlankLabel(t *testing.T) {
	t.Run("blank label is rejected", func(t *testing.T) {
		rt := newFakeRuntime()
		rt.resumeLabel = "   "
		err := startHostedOperation(rt, Options{StartupOperation: "resume"})
		if err == nil {
			t.Fatal("startHostedOperation resume with blank label: err = nil, want validation error")
		}
		if !strings.Contains(err.Error(), "empty recovery label") {
			t.Fatalf("resume error = %q, want empty recovery label", err)
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if !reflect.DeepEqual(rt.calls, []string{"resume"}) {
			t.Fatalf("calls = %v, want [resume]", rt.calls)
		}
	})

	t.Run("non-blank label succeeds", func(t *testing.T) {
		rt := newFakeRuntime()
		rt.resumeLabel = "chương 5"
		err := startHostedOperation(rt, Options{StartupOperation: "resume"})
		if err != nil {
			t.Fatalf("startHostedOperation resume with valid label: %v", err)
		}
	})

	t.Run("resume error is propagated", func(t *testing.T) {
		rt := newFakeRuntime()
		resumeErr := errors.New("không tìm thấy tiến độ")
		rt.resumeErr = resumeErr
		err := startHostedOperation(rt, Options{StartupOperation: "resume"})
		if !errors.Is(err, resumeErr) {
			t.Fatalf("resume error = %v, want %v", err, resumeErr)
		}
	})
}

func TestStartHostedOperationContinueRequiresInstruction(t *testing.T) {
	t.Run("missing instruction is rejected", func(t *testing.T) {
		rt := newFakeRuntime()
		err := startHostedOperation(rt, Options{StartupOperation: "continue"})
		if err == nil {
			t.Fatal("startHostedOperation continue without instruction: err = nil, want validation error")
		}
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if len(rt.calls) != 0 {
			t.Fatalf("calls = %v, want no runtime calls", rt.calls)
		}
	})

	t.Run("whitespace instruction is rejected", func(t *testing.T) {
		rt := newFakeRuntime()
		err := startHostedOperation(rt, Options{StartupOperation: "continue", Instruction: "\t\n"})
		if err == nil {
			t.Fatal("startHostedOperation continue with whitespace instruction: err = nil, want validation error")
		}
	})
}

func TestStartHostedOperationRejectsUnsupportedOperationBeforeConfigLoading(t *testing.T) {
	rt := newFakeRuntime()
	err := startHostedOperation(rt, Options{StartupOperation: "rewind", Instruction: "anything"})
	if err == nil {
		t.Fatal("startHostedOperation unsupported op: err = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "unsupported startup operation") {
		t.Fatalf("error = %q, want unsupported startup operation", err)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.calls) != 0 {
		t.Fatalf("calls = %v, want no runtime calls", rt.calls)
	}
}

func TestValidateAddressAcceptsSupportedAddresses(t *testing.T) {
	for _, addr := range []string{":0", "127.0.0.1:0", "[::1]:0", "example.com:8080"} {
		t.Run(addr, func(t *testing.T) {
			if err := ValidateAddress(addr); err != nil {
				t.Fatalf("ValidateAddress(%q): %v", addr, err)
			}
		})
	}
}

func TestValidateAddressRejectsMalformedAddresses(t *testing.T) {
	for _, addr := range []string{
		"foo bar:8080",
		"invalid-address",
		"localhost:not-a-port",
		"localhost:65536",
		"localhost:-1",
		"foo:8080:1",
		"foo/:8080",
	} {
		t.Run(addr, func(t *testing.T) {
			if err := ValidateAddress(addr); err == nil {
				t.Fatalf("ValidateAddress(%q) = nil, want validation error", addr)
			}
		})
	}
}

type closeTrackingConn struct {
	net.Conn
	closed      chan struct{}
	readStarted chan struct{}
	closeOnce   sync.Once
	readOnce    sync.Once
}

func (c *closeTrackingConn) Read(p []byte) (int, error) {
	c.readOnce.Do(func() { close(c.readStarted) })
	return c.Conn.Read(p)
}

func (c *closeTrackingConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.Conn.Close()
}

type scriptedListener struct {
	conn        net.Conn
	serveErr    error
	readStarted <-chan struct{}
	accepted    bool
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.readStarted
	return nil, l.serveErr
}

func (l *scriptedListener) Close() error { return nil }

func (l *scriptedListener) Addr() net.Addr { return scriptedAddr{} }

type scriptedAddr struct{}

func (scriptedAddr) Network() string { return "scripted" }
func (scriptedAddr) String() string  { return "scripted" }

func TestServeClosesHTTPServerBeforeRuntimeOnUnexpectedServeError(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	trackedConn := &closeTrackingConn{
		Conn:        serverConn,
		closed:      make(chan struct{}),
		readStarted: make(chan struct{}),
	}
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	runtime := &trackingRuntime{fakeRuntime: newFakeRuntime()}
	runtime.observeClose = func() {
		select {
		case <-trackedConn.closed:
			runtime.serverClosedBeforeRuntimeClose = true
		default:
		}
	}
	serveErr := errors.New("serve failed")
	err := serveListener(context.Background(), runtime, "scripted", &scriptedListener{
		conn:        trackedConn,
		serveErr:    serveErr,
		readStarted: trackedConn.readStarted,
	})
	if !errors.Is(err, serveErr) {
		t.Fatalf("serveListener error = %v, want %v", err, serveErr)
	}
	if !runtime.serverClosedBeforeRuntimeClose {
		t.Fatal("runtime.Close observed an open HTTP connection, want http.Server.Close first")
	}
	if runtime.fakeRuntime.closeCallCount != 1 {
		t.Fatalf("runtime Close calls = %d, want 1", runtime.fakeRuntime.closeCallCount)
	}
}
