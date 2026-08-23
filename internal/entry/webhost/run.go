package webhost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

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

func Run(ctx context.Context, options Options) error {
	if options.ConfigPath == "" {
		return errors.New("config path is required")
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
		cleanup()
		if errors.Is(serverErr, http.ErrServerClosed) {
			return nil
		}
		return serverErr
	}
}
