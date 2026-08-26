package simulation

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"

	"github.com/talifpathan/helix/internal/storage"
)

// WorkloadConfig configures a concurrent workload. Each key gets one writer goroutine that writes
// the sequence "<key>#1".."<key>#WritesPerKey" (so the single-writer-per-key assumption of the
// freshness check holds), and Readers reader goroutines issue random reads until the writers
// finish. WriterNodes and ReaderNodes are the nodes to coordinate through; restrict them to one
// side of a partition to exercise that side.
type WorkloadConfig struct {
	Keys         []string
	WritesPerKey int
	Readers      int
	Seed         int64
	WriterNodes  []string
	ReaderNodes  []string
}

// RunWorkload runs the configured workload against the cluster and returns the recorded history.
func RunWorkload(ctx context.Context, c *Cluster, cfg WorkloadConfig) *History {
	h := NewHistory()

	var writers sync.WaitGroup
	for wi, key := range cfg.Keys {
		writers.Add(1)
		go func(wi int, key string) {
			defer writers.Done()
			for i := 1; i <= cfg.WritesPerKey; i++ {
				via := cfg.WriterNodes[(wi+i)%len(cfg.WriterNodes)]
				val := fmt.Sprintf("%s#%d", key, i)
				start := h.tick()
				err := c.Put(ctx, via, []byte(key), []byte(val))
				h.record(Event{
					Kind: OpPut, Key: key, Value: val, OK: err == nil,
					Via: via, StartSeq: start, EndSeq: h.tick(),
				})
			}
		}(wi, key)
	}

	writersDone := make(chan struct{})
	var readers sync.WaitGroup
	for r := 0; r < cfg.Readers; r++ {
		readers.Add(1)
		go func(r int) {
			defer readers.Done()
			rng := rand.New(rand.NewSource(cfg.Seed + int64(r) + 1))
			for {
				select {
				case <-writersDone:
					return
				default:
				}
				key := cfg.Keys[rng.Intn(len(cfg.Keys))]
				via := cfg.ReaderNodes[rng.Intn(len(cfg.ReaderNodes))]
				start := h.tick()
				v, err := c.Get(ctx, via, []byte(key))
				ev := Event{Kind: OpGet, Key: key, Via: via, StartSeq: start, EndSeq: h.tick()}
				switch {
				case err == nil:
					ev.OK, ev.Found, ev.Value = true, true, string(v)
				case errors.Is(err, storage.ErrNotFound):
					ev.OK, ev.Found = true, false
				default:
					ev.OK = false
				}
				h.record(ev)
			}
		}(r)
	}

	writers.Wait()
	close(writersDone)
	readers.Wait()
	return h
}
