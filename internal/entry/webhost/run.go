package webhost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/voocel/ainovel-cli/assets"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/host"
	"github.com/voocel/ainovel-cli/internal/rules"
)

const (
	replayLimit     = 256
	shutdownTimeout = 5 * time.Second
)

type Options struct {
	ConfigPath string
	Addr       string
}

// ValidateAddress validates a TCP listen address without resolving DNS names.
func ValidateAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid address %q: want host:port", addr)
	}
	if strings.IndexFunc(host, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("invalid address %q: host contains whitespace or control characters", addr)
	}
	if host != "" && !validHost(host) {
		return fmt.Errorf("invalid address %q: host is malformed", addr)
	}
	if port == "" || strings.Trim(port, "0123456789") != "" {
		return fmt.Errorf("invalid address %q: port must be between 0 and 65535", addr)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return fmt.Errorf("invalid address %q: port must be between 0 and 65535", addr)
	}
	return nil
}

func validHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if percent := strings.LastIndexByte(host, '%'); percent >= 0 {
		if percent == 0 || percent == len(host)-1 || strings.Contains(host[percent+1:], "%") {
			return false
		}
		if net.ParseIP(host[:percent]) == nil {
			return false
		}
		return true
	}
	if strings.Contains(host, ":") {
		return false
	}
	if strings.HasSuffix(host, ".") {
		host = strings.TrimSuffix(host, ".")
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}

func Run(ctx context.Context, options Options) error {
	if options.ConfigPath == "" {
		return errors.New("config path is required")
	}
	if err := ValidateAddress(options.Addr); err != nil {
		return err
	}

	cfg, err := bootstrap.LoadConfig(options.ConfigPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	rules.EnsureHomeRulesDir()
	bundle := assets.Load(cfg.Style)
	runtime, err := host.New(cfg, bundle)
	if err != nil {
		return fmt.Errorf("create host: %w", err)
	}
	return serve(ctx, runtime, options.Addr)
}

func serve(ctx context.Context, runtime runtime, addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		runtime.Close()
		return err
	}
	return serveListener(ctx, runtime, addr, listener)
}

func serveListener(ctx context.Context, runtime runtime, addr string, listener net.Listener) error {
	app := newServer(runtime, replayLimit)
	httpServer := &http.Server{Addr: addr, Handler: app.Handler()}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- httpServer.Serve(listener)
	}()

	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			app.Close()
			runtime.Close()
		})
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		shutdownErr := httpServer.Shutdown(shutdownCtx)
		cancel()
		_ = httpServer.Close()
		cleanup()
		serverErr := <-serveErr
		if shutdownErr != nil {
			return shutdownErr
		}
		if serverErr != nil && !errors.Is(serverErr, http.ErrServerClosed) {
			return serverErr
		}
		return nil
	case serverErr := <-serveErr:
		if serverErr != nil && !errors.Is(serverErr, http.ErrServerClosed) {
			_ = httpServer.Close()
		}
		cleanup()
		if errors.Is(serverErr, http.ErrServerClosed) {
			return nil
		}
		return serverErr
	}
}
