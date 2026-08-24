// Package config loads node configuration from the environment following
// twelve-factor principles: configuration lives in the environment, has safe
// defaults, and is validated once at startup so the process fails fast on
// misconfiguration rather than at first use.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/talifpathan/helix/internal/storage"
)

// Environment variable names. Every tunable is namespaced with HELIX_.
const (
	envDataDir          = "HELIX_DATA_DIR"
	envSyncWrites       = "HELIX_SYNC_WRITES"
	envMaxKeyBytes      = "HELIX_MAX_KEY_BYTES"
	envMaxValueBytes    = "HELIX_MAX_VALUE_BYTES"
	envMemtableMaxBytes = "HELIX_MEMTABLE_MAX_BYTES"
	envBlockCacheBytes  = "HELIX_BLOCK_CACHE_BYTES"
	envCompactionMin    = "HELIX_COMPACTION_MIN_THRESHOLD"
	envLogLevel         = "HELIX_LOG_LEVEL"
	envLogFormat        = "HELIX_LOG_FORMAT"
)

// Config is the fully resolved, validated node configuration.
type Config struct {
	DataDir                string
	SyncWrites             bool
	MaxKeyBytes            int
	MaxValueBytes          int
	MemtableMaxBytes       int
	BlockCacheBytes        int
	CompactionMinThreshold int
	LogLevel               string
	LogFormat              string
}

// Default returns configuration suitable for local development.
func Default() Config {
	return Config{
		DataDir:                "./data",
		SyncWrites:             true,
		MaxKeyBytes:            storage.DefaultMaxKeyBytes,
		MaxValueBytes:          storage.DefaultMaxValueBytes,
		MemtableMaxBytes:       64 << 20, // 64 MiB, consulted by the flush path
		BlockCacheBytes:        storage.DefaultBlockCacheBytes,
		CompactionMinThreshold: storage.DefaultCompactionMinThreshold,
		LogLevel:               "info",
		LogFormat:              "json",
	}
}

// Load reads configuration from the environment, layering any set variables over
// the defaults, then validates the result.
func Load() (Config, error) {
	c := Default()
	var err error

	c.DataDir = getEnvString(envDataDir, c.DataDir)
	if c.SyncWrites, err = getEnvBool(envSyncWrites, c.SyncWrites); err != nil {
		return Config{}, err
	}
	if c.MaxKeyBytes, err = getEnvInt(envMaxKeyBytes, c.MaxKeyBytes); err != nil {
		return Config{}, err
	}
	if c.MaxValueBytes, err = getEnvInt(envMaxValueBytes, c.MaxValueBytes); err != nil {
		return Config{}, err
	}
	if c.MemtableMaxBytes, err = getEnvInt(envMemtableMaxBytes, c.MemtableMaxBytes); err != nil {
		return Config{}, err
	}
	if c.BlockCacheBytes, err = getEnvInt(envBlockCacheBytes, c.BlockCacheBytes); err != nil {
		return Config{}, err
	}
	if c.CompactionMinThreshold, err = getEnvInt(envCompactionMin, c.CompactionMinThreshold); err != nil {
		return Config{}, err
	}
	c.LogLevel = strings.ToLower(getEnvString(envLogLevel, c.LogLevel))
	c.LogFormat = strings.ToLower(getEnvString(envLogFormat, c.LogFormat))

	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Validate enforces invariants that the rest of the system relies on.
func (c Config) Validate() error {
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("config: %s must not be empty", envDataDir)
	}
	if c.MaxKeyBytes <= 0 {
		return fmt.Errorf("config: %s must be positive, got %d", envMaxKeyBytes, c.MaxKeyBytes)
	}
	if c.MaxValueBytes <= 0 {
		return fmt.Errorf("config: %s must be positive, got %d", envMaxValueBytes, c.MaxValueBytes)
	}
	if c.MemtableMaxBytes <= 0 {
		return fmt.Errorf("config: %s must be positive, got %d", envMemtableMaxBytes, c.MemtableMaxBytes)
	}
	if c.BlockCacheBytes <= 0 {
		return fmt.Errorf("config: %s must be positive, got %d", envBlockCacheBytes, c.BlockCacheBytes)
	}
	if c.CompactionMinThreshold < 2 {
		return fmt.Errorf("config: %s must be at least 2, got %d", envCompactionMin, c.CompactionMinThreshold)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("config: %s must be one of debug, info, warn, error, got %q", envLogLevel, c.LogLevel)
	}
	switch c.LogFormat {
	case "json", "text":
	default:
		return fmt.Errorf("config: %s must be one of json, text, got %q", envLogFormat, c.LogFormat)
	}
	return nil
}

// ToStorageOptions projects the config onto the storage engine's option object,
// keeping the storage package free of any dependency on this one.
func (c Config) ToStorageOptions(logger *slog.Logger) storage.Options {
	return storage.Options{
		DataDir:                c.DataDir,
		SyncWrites:             c.SyncWrites,
		MaxKeyBytes:            c.MaxKeyBytes,
		MaxValueBytes:          c.MaxValueBytes,
		MemtableMaxBytes:       c.MemtableMaxBytes,
		BlockCacheBytes:        c.BlockCacheBytes,
		CompactionMinThreshold: c.CompactionMinThreshold,
		Logger:                 logger,
	}
}

func getEnvString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer, got %q", key, v)
	}
	return n, nil
}

func getEnvBool(key string, fallback bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return fallback, nil
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		return false, fmt.Errorf("config: %s must be a boolean, got %q", key, v)
	}
	return b, nil
}
