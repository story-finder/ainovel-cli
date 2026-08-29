package webhost

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
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
