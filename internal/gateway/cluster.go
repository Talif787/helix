package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// NodeResult is one node's contribution to an aggregated view. The node's raw JSON for the fetched
// admin path is relayed verbatim in Data, so the console gets every field without the gateway
// mirroring the node's schema. A node that cannot be reached is reported with OK false and an
// error, so the console can show a degraded cluster rather than failing the whole request.
type NodeResult struct {
	NodeID string          `json:"node_id"`
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// ClusterAggregate is the shape returned by the per-node aggregation endpoints (status, members,
// ring): one NodeResult per configured node, in configured order.
type ClusterAggregate struct {
	Nodes []NodeResult `json:"nodes"`
}

// fetchNodeJSON GETs a JSON admin path from one node and relays its body.
func (s *Server) fetchNodeJSON(ctx context.Context, n Node, path string) NodeResult {
	res := NodeResult{NodeID: n.ID}
	url := fmt.Sprintf("http://%s%s", n.AdminAddr, path)
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
	res.Data = json.RawMessage(body)
	return res
}

// aggregate fetches the given admin path from every node concurrently, in configured order.
func (s *Server) aggregate(ctx context.Context, path string) ClusterAggregate {
	results := make([]NodeResult, len(s.cfg.Nodes))
	var wg sync.WaitGroup
	for i, n := range s.cfg.Nodes {
		wg.Add(1)
		go func(i int, n Node) {
			defer wg.Done()
			results[i] = s.fetchNodeJSON(ctx, n, path)
		}(i, n)
	}
	wg.Wait()
	return ClusterAggregate{Nodes: results}
}

func (s *Server) handleClusterStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.aggregate(r.Context(), "/api/v1/status"))
}

func (s *Server) handleClusterMembers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.aggregate(r.Context(), "/api/v1/members"))
}

func (s *Server) handleClusterRing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.aggregate(r.Context(), "/api/v1/ring"))
}
