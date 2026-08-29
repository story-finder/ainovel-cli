package main

import "testing"

func TestParseOptionsRequiresExplicitConfigAndDefaultsFixedAddress(t *testing.T) {
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
	if _, err := parseOptions([]string{
		"--config", "config.json",
		"--addr", "invalid-address",
	}); err == nil {
		t.Fatal("parseOptions invalid address error = nil, want host:port validation error")
	}
}

func TestParseOptionsRejectsAddressWithWhitespaceInHost(t *testing.T) {
	if _, err := parseOptions([]string{
		"--config", "missing.json",
		"--addr", "foo bar:8080",
	}); err == nil {
		t.Fatal("parseOptions malformed address error = nil, want host validation error")
	}
}
