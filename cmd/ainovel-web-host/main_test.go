package main

import (
	"strings"
	"testing"
)

func TestParseOptionsRequiresExplicitConfigAndDefaultsFixedAddress(t *testing.T) {
	t.Setenv("AINOVEL_STARTUP_OPERATION", "start")
	t.Setenv("AINOVEL_INSTRUCTION", "ý tưởng khởi đầu")

	if _, err := parseOptions(nil); err == nil {
		t.Fatal("parseOptions(nil) error = nil, want explicit config error")
	}

	opts, err := parseOptions([]string{"--config", "config.json"})
	if err != nil {
		t.Fatalf("parseOptions with config: %v", err)
	}
	if opts.ConfigPath != "config.json" {
		t.Fatalf("ConfigPath = %q, want config.json", opts.ConfigPath)
	}
	if opts.Addr != "0.0.0.0:8080" {
		t.Fatalf("Addr = %q, want 0.0.0.0:8080", opts.Addr)
	}
}

func TestParseOptionsRejectsInvalidAddress(t *testing.T) {
	t.Setenv("AINOVEL_STARTUP_OPERATION", "start")
	if _, err := parseOptions([]string{
		"--config", "config.json",
		"--addr", "invalid-address",
	}); err == nil {
		t.Fatal("parseOptions invalid address error = nil, want host:port validation error")
	}
}

func TestParseOptionsRejectsAddressWithWhitespaceInHost(t *testing.T) {
	t.Setenv("AINOVEL_STARTUP_OPERATION", "start")
	if _, err := parseOptions([]string{
		"--config", "missing.json",
		"--addr", "foo bar:8080",
	}); err == nil {
		t.Fatal("parseOptions malformed address error = nil, want host validation error")
	}
}

func TestParseOptionsRequiresStartupOperationEnv(t *testing.T) {
	t.Setenv("AINOVEL_STARTUP_OPERATION", "")
	_, err := parseOptions([]string{"--config", "config.json"})
	if err == nil {
		t.Fatal("parseOptions missing AINOVEL_STARTUP_OPERATION: err = nil, want required env error")
	}
	if !strings.Contains(err.Error(), "AINOVEL_STARTUP_OPERATION") {
		t.Fatalf("error = %q, want mention of AINOVEL_STARTUP_OPERATION", err)
	}
}

func TestParseOptionsRejectsUnsupportedStartupOperation(t *testing.T) {
	t.Setenv("AINOVEL_STARTUP_OPERATION", "rewind")
	_, err := parseOptions([]string{"--config", "config.json"})
	if err == nil {
		t.Fatal("parseOptions unsupported op: err = nil, want membership error")
	}
	if !strings.Contains(err.Error(), "unsupported startup operation") {
		t.Fatalf("error = %q, want unsupported startup operation", err)
	}
}

func TestParseOptionsTrimsStartupOperationAndInstruction(t *testing.T) {
	t.Setenv("AINOVEL_STARTUP_OPERATION", "  resume  ")
	t.Setenv("AINOVEL_INSTRUCTION", "  chương 7: đêm trước trận đánh  ")

	opts, err := parseOptions([]string{"--config", "config.json"})
	if err != nil {
		t.Fatalf("parseOptions with padded env: %v", err)
	}
	if opts.StartupOperation != "resume" {
		t.Fatalf("StartupOperation = %q, want resume", opts.StartupOperation)
	}
	if opts.Instruction != "chương 7: đêm trước trận đánh" {
		t.Fatalf("Instruction = %q, want trimmed value", opts.Instruction)
	}
}

func TestParseOptionsAcceptsEachSupportedOperation(t *testing.T) {
	for _, op := range []string{"start", "resume", "continue"} {
		t.Run(op, func(t *testing.T) {
			t.Setenv("AINOVEL_STARTUP_OPERATION", op)
			t.Setenv("AINOVEL_INSTRUCTION", "ý tưởng")
			opts, err := parseOptions([]string{"--config", "config.json"})
			if err != nil {
				t.Fatalf("parseOptions op=%q: %v", op, err)
			}
			if opts.StartupOperation != op {
				t.Fatalf("StartupOperation = %q, want %q", opts.StartupOperation, op)
			}
		})
	}
}
