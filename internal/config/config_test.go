package config

import "testing"

func TestConfigDefaultIsValid(t *testing.T) {
	c := Default()
	if err := c.Validate(); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
	if c.LogFormat != "json" {
		t.Fatalf("expected default json format, got %q", c.LogFormat)
	}
}

func TestConfigLoadDefaults(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.DataDir == "" {
		t.Fatal("expected a non-empty default data dir")
	}
}

func TestConfigEnvOverride(t *testing.T) {
	t.Setenv(envDataDir, "/tmp/helix-test")
	t.Setenv(envSyncWrites, "false")
	t.Setenv(envMaxKeyBytes, "128")
	t.Setenv(envLogLevel, "DEBUG")
	t.Setenv(envLogFormat, "text")

	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.DataDir != "/tmp/helix-test" {
		t.Fatalf("data dir: %q", c.DataDir)
	}
	if c.SyncWrites {
		t.Fatal("expected sync writes disabled")
	}
	if c.MaxKeyBytes != 128 {
		t.Fatalf("max key bytes: %d", c.MaxKeyBytes)
	}
	if c.LogLevel != "debug" {
		t.Fatalf("log level should be lowercased to debug, got %q", c.LogLevel)
	}
	if c.LogFormat != "text" {
		t.Fatalf("log format: %q", c.LogFormat)
	}
}

func TestConfigInvalidInteger(t *testing.T) {
	t.Setenv(envMaxKeyBytes, "not-a-number")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a non-integer size")
	}
}

func TestConfigInvalidBool(t *testing.T) {
	t.Setenv(envSyncWrites, "maybe")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a non-boolean flag")
	}
}

func TestConfigInvalidLogLevel(t *testing.T) {
	t.Setenv(envLogLevel, "verbose")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for an unknown log level")
	}
}
