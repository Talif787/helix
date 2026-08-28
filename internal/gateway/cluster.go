package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// NodeStatusResult is one node's contribution to the aggregated cluster status. The node's raw
// /api/v1/status JSON is relayed verbatim so the console gets every field without the gateway
// having to mirror the node's schema. A node that cannot be reached is reported with OK false and
// an error, so the console can show a degraded cluster rather than failing the whole request.
type NodeStatusResult struct {
	NodeID string          `json:"node_id"`
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Status json.RawMessage `json:"status,omitempty"`
}

// ClusterStatus is the aggregated view returned by GET /api/v1/cluster/status.
type ClusterStatus struct {
	Nodes []NodeStatusResult `json:"nodes"`
}

func (s *Server) fetchNodeStatus(ctx context.Context, n Node) NodeStatusResult {
	res := NodeStatusResult{NodeID: n.ID}
	url := fmt.Sprintf("http://%s/api/v1/status", n.AdminAddr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	resp, err := s.http.Do(req)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		res.Error = fmt.Sprintf("node returned status %d", resp.StatusCode)
		return res
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.OK = true
	res.Status = json.RawMessage(body)
	return res
}

// clusterStatus fetches every node's status concurrently and returns them in configured order.
func (s *Server) clusterStatus(ctx context.Context) ClusterStatus {
	results := make([]NodeStatusResult, len(s.cfg.Nodes))
	var wg sync.WaitGroup
	for i, n := range s.cfg.Nodes {
		wg.Add(1)
		go func(i int, n Node) {
			defer wg.Done()
			results[i] = s.fetchNodeStatus(ctx, n)
		}(i, n)
	}
	wg.Wait()
	return ClusterStatus{Nodes: results}
}

func (s *Server) handleClusterStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.clusterStatus(r.Context()))
}
