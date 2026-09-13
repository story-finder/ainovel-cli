package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/voocel/ainovel-cli/internal/entry/webhost"
)

const (
	defaultAddr         = "0.0.0.0:8080"
	envStartupOperation = "AINOVEL_STARTUP_OPERATION"
	envInstruction      = "AINOVEL_INSTRUCTION"
)

func main() {
	options, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "flags: %v\n", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := webhost.Run(ctx, options); err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		os.Exit(1)
	}
}

// parseOptions đọc cờ dòng lệnh cùng các biến môi trường AINOVEL_STARTUP_OPERATION và
// AINOVEL_INSTRUCTION, đồng thời xác thực thao tác khởi động có thuộc tập hỗ trợ hay không
// trước khi gọi webhost.Run. Chỉ thị và thao tác đều được trim trắng; thao tác bắt buộc phải có.
func parseOptions(args []string) (webhost.Options, error) {
	flags := flag.NewFlagSet("ainovel-web-host", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to the configuration file")
	addr := flags.String("addr", defaultAddr, "HTTP listen address")
	if err := flags.Parse(args); err != nil {
		return webhost.Options{}, err
	}
	if flags.NArg() != 0 {
		return webhost.Options{}, fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*configPath) == "" {
		return webhost.Options{}, fmt.Errorf("--config is required")
	}
	if err := webhost.ValidateAddress(*addr); err != nil {
		return webhost.Options{}, err
	}

	operation := strings.TrimSpace(os.Getenv(envStartupOperation))
	if operation == "" {
		return webhost.Options{}, fmt.Errorf("%s is required", envStartupOperation)
	}
	if _, ok := webhost.SupportedStartupOperations()[operation]; !ok {
		return webhost.Options{}, fmt.Errorf("unsupported startup operation %q", operation)
	}

	return webhost.Options{
		ConfigPath:       *configPath,
		Addr:             *addr,
		StartupOperation: operation,
		Instruction:      strings.TrimSpace(os.Getenv(envInstruction)),
	}, nil
}
