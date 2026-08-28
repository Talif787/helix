package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// This file serves GET /api/v1/stream as Server-Sent Events: a periodic snapshot of membership and
// metrics so the console can show live topology and dashboards without polling. The stream stays
// behind the operator bearer token like every other API route, so the browser consumes it with a
// fetch streaming reader (which can send the Authorization header) rather than EventSource.

// streamSnapshot is one pushed frame: a timestamp, the aggregated membership, and the aggregated
// metrics. The console diffs successive frames to update the view.
type streamSnapshot struct {
	TS      int64            `json:"ts"`
	Members ClusterAggregate `json:"members"`
	Metrics ClusterMetrics   `json:"metrics"`
}

func (s *Server) snapshot(ctx context.Context) streamSnapshot {
	return streamSnapshot{
		TS:      time.Now().UnixMilli(),
		Members: s.aggregate(ctx, "/api/v1/members"),
		Metrics: s.clusterMetrics(ctx),
	}
}

func (s *Server) streamInterval() time.Duration {
	if s.cfg.StreamInterval > 0 {
		return s.cfg.StreamInterval
	}
	return 2 * time.Second
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// writeSnapshot returns false when the client has gone (a write error), ending the stream.
	writeSnapshot := func() bool {
		b, err := json.Marshal(s.snapshot(r.Context()))
		if err != nil {
			return true // skip this frame, keep the stream open
		}
		if _, err := fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	// Push an initial frame immediately so the console renders without waiting a full interval.
	if !writeSnapshot() {
		return
	}

	ticker := time.NewTicker(s.streamInterval())
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !writeSnapshot() {
				return
			}
		}
	}
}
