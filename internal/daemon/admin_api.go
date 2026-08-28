package daemon

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/talifpathan/helix/internal/membership"
	"github.com/talifpathan/helix/internal/storage"
)

// This file serves the node's read-only status API as JSON on the admin server, alongside /metrics
// and /healthz. It exposes what already exists in-process (config, storage stats, SWIM membership,
// and the consistent-hash ring) so the operator console's gateway can aggregate it across nodes.
// The JSON-building is kept in pure functions so it is testable without a running daemon.

type configJSON struct {
	N                     int   `json:"n"`
	R                     int   `json:"r"`
	W                     int   `json:"w"`
	VNodes                int   `json:"vnodes"`
	MaxHints              int   `json:"max_hints"`
	RequestTimeoutMS      int64 `json:"request_timeout_ms"`
	SwimIntervalMS        int64 `json:"swim_interval_ms"`
	AntiEntropyIntervalMS int64 `json:"anti_entropy_interval_ms"`
	HintIntervalMS        int64 `json:"hint_interval_ms"`
}

type storageJSON struct {
	MemtableKeys       int    `json:"memtable_keys"`
	ApproxBytes        int64  `json:"approx_bytes"`
	NextSeq            uint64 `json:"next_seq"`
	SSTables           int    `json:"sstables"`
	ImmutableMemtables int    `json:"immutable_memtables"`
}

type statusJSON struct {
	NodeID        string            `json:"node_id"`
	UptimeSeconds int64             `json:"uptime_seconds"`
	Config        configJSON        `json:"config"`
	Storage       storageJSON       `json:"storage"`
	Peers         map[string]string `json:"peers"`
}

type memberJSON struct {
	ID          string `json:"id"`
	State       string `json:"state"`
	Incarnation uint64 `json:"incarnation"`
}

type membersJSON struct {
	Members []memberJSON `json:"members"`
}

type ringJSON struct {
	VNodes int      `json:"vnodes"`
	Size   int      `json:"size"`
	Nodes  []string `json:"nodes"`
}

type keyJSON struct {
	Key            string   `json:"key"`
	N              int      `json:"n"`
	PreferenceList []string `json:"preference_list"`
}

func statusPayload(cfg Config, uptime time.Duration, st storage.Stats) statusJSON {
	return statusJSON{
		NodeID:        cfg.NodeID,
		UptimeSeconds: int64(uptime.Seconds()),
		Config: configJSON{
			N: cfg.N, R: cfg.R, W: cfg.W, VNodes: cfg.VNodes, MaxHints: cfg.MaxHints,
			RequestTimeoutMS:      cfg.RequestTimeout.Milliseconds(),
			SwimIntervalMS:        cfg.SwimInterval.Milliseconds(),
			AntiEntropyIntervalMS: cfg.AntiEntropyInterval.Milliseconds(),
			HintIntervalMS:        cfg.HintInterval.Milliseconds(),
		},
		Storage: storageJSON{
			MemtableKeys: st.Keys, ApproxBytes: st.ApproxBytes, NextSeq: st.NextSeq,
			SSTables: st.SSTables, ImmutableMemtables: st.Immutable,
		},
		Peers: cfg.Peers,
	}
}

func membersPayload(ms []membership.Member) membersJSON {
	out := membersJSON{Members: make([]memberJSON, 0, len(ms))}
	for _, m := range ms {
		out.Members = append(out.Members, memberJSON{
			ID: m.ID, State: m.State.String(), Incarnation: m.Incarnation,
		})
	}
	return out
}

func ringPayload(nodes []string, vnodes, size int) ringJSON {
	if nodes == nil {
		nodes = []string{}
	}
	return ringJSON{VNodes: vnodes, Size: size, Nodes: nodes}
}

func keyPayload(key string, n int, pref []string) keyJSON {
	if pref == nil {
		pref = []string{}
	}
	return keyJSON{Key: key, N: n, PreferenceList: pref}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (d *Daemon) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, statusPayload(d.cfg, time.Since(d.startedAt), d.eng.Stats()))
}

func (d *Daemon) handleMembers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, membersPayload(d.swim.List().Members()))
}

func (d *Daemon) handleRing(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, ringPayload(d.ring.Nodes(), d.cfg.VNodes, d.ring.Size()))
}

func (d *Daemon) handleKey(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if key == "" {
		http.Error(w, "missing key", http.StatusBadRequest)
		return
	}
	writeJSON(w, keyPayload(key, d.cfg.N, d.ring.LookupN([]byte(key), d.cfg.N)))
}
