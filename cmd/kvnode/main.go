// Command kvnode is the Helix node entrypoint. In this phase it exposes the
// storage engine through a minimal line-oriented shell so the full write, read,
// and recovery path is runnable end to end. Network serving (gRPC) arrives in a
// later phase; the bootstrap, configuration, logging, and graceful-shutdown
// scaffolding here is what that phase builds on.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/talifpathan/helix/internal/config"
	"github.com/talifpathan/helix/internal/observability"
	"github.com/talifpathan/helix/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := observability.NewLogger(cfg.LogLevel, cfg.LogFormat)

	engine, err := storage.Open(cfg.ToStorageOptions(logger))
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer func() {
		if cerr := engine.Close(); cerr != nil {
			logger.Error("engine close failed", "error", cerr)
		} else {
			logger.Info("engine closed cleanly")
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("kvnode ready", "shell", "type HELP for commands")
	repl(ctx, engine, logger, os.Stdin, os.Stdout)
	return nil
}

// repl runs the interactive shell until the context is cancelled (SIGINT or
// SIGTERM) or the input stream reaches EOF. Reading happens on a goroutine so a
// shutdown signal interrupts an otherwise-blocking read.
func repl(ctx context.Context, engine *storage.Engine, logger *slog.Logger, in io.Reader, out io.Writer) {
	lines := make(chan string)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(in)
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			logger.Info("shutdown signal received")
			return
		case line, ok := <-lines:
			if !ok {
				return
			}
			if quit := execute(engine, strings.TrimSpace(line), out); quit {
				return
			}
		}
	}
}

// execute runs a single shell command and returns true when the shell should exit.
func execute(engine *storage.Engine, line string, out io.Writer) bool {
	if line == "" {
		return false
	}
	fields := strings.Fields(line)
	cmd := strings.ToUpper(fields[0])

	switch cmd {
	case "HELP":
		fmt.Fprintln(out, "commands: SET <key> <value> | GET <key> | DEL <key> | FLUSH | STATS | HELP | EXIT")
	case "EXIT", "QUIT":
		return true
	case "SET":
		if len(fields) < 3 {
			fmt.Fprintln(out, "usage: SET <key> <value>")
			return false
		}
		key := fields[1]
		value := strings.Join(fields[2:], " ")
		if err := engine.Put([]byte(key), []byte(value)); err != nil {
			fmt.Fprintln(out, "ERR", err)
			return false
		}
		fmt.Fprintln(out, "OK")
	case "GET":
		if len(fields) != 2 {
			fmt.Fprintln(out, "usage: GET <key>")
			return false
		}
		v, err := engine.Get([]byte(fields[1]))
		if errors.Is(err, storage.ErrNotFound) {
			fmt.Fprintln(out, "(nil)")
			return false
		}
		if err != nil {
			fmt.Fprintln(out, "ERR", err)
			return false
		}
		fmt.Fprintln(out, string(v))
	case "DEL":
		if len(fields) != 2 {
			fmt.Fprintln(out, "usage: DEL <key>")
			return false
		}
		if err := engine.Delete([]byte(fields[1])); err != nil {
			fmt.Fprintln(out, "ERR", err)
			return false
		}
		fmt.Fprintln(out, "OK")
	case "FLUSH":
		if err := engine.Flush(); err != nil {
			fmt.Fprintln(out, "ERR", err)
			return false
		}
		fmt.Fprintln(out, "OK")
	case "STATS":
		s := engine.Stats()
		fmt.Fprintln(out, "keys="+strconv.Itoa(s.Keys),
			"approx_bytes="+strconv.FormatInt(s.ApproxBytes, 10),
			"sstables="+strconv.Itoa(s.SSTables),
			"immutable="+strconv.Itoa(s.Immutable),
			"next_seq="+strconv.FormatUint(s.NextSeq, 10))
	default:
		fmt.Fprintln(out, "unknown command:", cmd, "(type HELP)")
	}
	return false
}
