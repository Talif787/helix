package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// This file aggregates each node's Prometheus /metrics endpoint into JSON for the console's
// dashboards. It parses the text exposition format with a small dependency-free parser: the node's
// metrics are simple gauges (some with a single label), so a full Prometheus library is not needed.

// Sample is one parsed metric sample: a name, optional labels, and a float value.
type Sample struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Value  float64           `json:"value"`
}

// NodeMetricsResult is one node's parsed metrics, or an error if it could not be reached.
type NodeMetricsResult struct {
	NodeID  string   `json:"node_id"`
	OK      bool     `json:"ok"`
	Error   string   `json:"error,omitempty"`
	Samples []Sample `json:"samples,omitempty"`
}

// ClusterMetrics is the shape returned by GET /api/v1/cluster/metrics.
type ClusterMetrics struct {
	Nodes []NodeMetricsResult `json:"nodes"`
}

// parsePrometheus parses the Prometheus text exposition format into samples. It skips comment and
// blank lines, handles "name value", "name{labels} value", and a trailing timestamp, and ignores
// lines it cannot parse rather than failing the whole scrape.
func parsePrometheus(text string) []Sample {
	var out []Sample
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var name, labelPart, rest string
		if i := strings.IndexByte(line, '{'); i >= 0 {
			j := strings.IndexByte(line, '}')
			if j < i {
				continue
			}
			name = strings.TrimSpace(line[:i])
			labelPart = line[i+1 : j]
			rest = strings.TrimSpace(line[j+1:])
		} else {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			name = fields[0]
			rest = fields[1]
		}
		valTok := rest
		if fields := strings.Fields(rest); len(fields) > 0 {
			valTok = fields[0] // value, ignoring any trailing timestamp
		}
		val, err := strconv.ParseFloat(valTok, 64)
		if err != nil {
			continue
		}
		out = append(out, Sample{Name: name, Labels: parseLabels(labelPart), Value: val})
	}
	return out
}

func parseLabels(s string) map[string]string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	labels := map[string]string{}
	for _, pair := range splitLabels(s) {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.Trim(strings.TrimSpace(kv[1]), `"`)
		if k != "" {
			labels[k] = v
		}
	}
	if len(labels) == 0 {
		return nil
	}
	return labels
}

// splitLabels splits a label list on commas that are not inside a quoted value.
func splitLabels(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ',' && !inQuote:
			parts = append(parts, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

func (s *Server) fetchNodeMetrics(ctx context.Context, n Node) NodeMetricsResult {
	res := NodeMetricsResult{NodeID: n.ID}
	url := fmt.Sprintf("http://%s/metrics", n.AdminAddr)
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.OK = true
	res.Samples = parsePrometheus(string(body))
	return res
}

func (s *Server) clusterMetrics(ctx context.Context) ClusterMetrics {
	results := make([]NodeMetricsResult, len(s.cfg.Nodes))
	var wg sync.WaitGroup
	for i, n := range s.cfg.Nodes {
		wg.Add(1)
		go func(i int, n Node) {
			defer wg.Done()
			results[i] = s.fetchNodeMetrics(ctx, n)
		}(i, n)
	}
	wg.Wait()
	return ClusterMetrics{Nodes: results}
}

func (s *Server) handleClusterMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.clusterMetrics(r.Context()))
}
