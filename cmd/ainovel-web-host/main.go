package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/voocel/ainovel-cli/internal/entry/webhost"
)

const defaultAddr = "0.0.0.0:8080"

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
	if err := validateAddress(*addr); err != nil {
		return webhost.Options{}, err
	}
	return webhost.Options{ConfigPath: *configPath, Addr: *addr}, nil
}

func validateAddress(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid --addr %q: want host:port", addr)
	}
	if strings.TrimSpace(host) != host {
		return fmt.Errorf("invalid --addr %q: host contains whitespace", addr)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return fmt.Errorf("invalid --addr %q: port must be between 0 and 65535", addr)
	}
	return nil
}
