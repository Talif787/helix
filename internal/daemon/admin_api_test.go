package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/talifpathan/helix/internal/cluster"
	"github.com/talifpathan/helix/internal/membership"
	"github.com/talifpathan/helix/internal/storage"
)

func TestStatusPayload(t *testing.T) {
	cfg := Config{
		NodeID: "n0", N: 3, R: 2, W: 2, VNodes: 128, MaxHints: 3,
		RequestTimeout: 2 * time.Second, SwimInterval: time.Second,
		AntiEntropyInterval: 30 * time.Second, HintInterval: 10 * time.Second,
		Peers: map[string]string{"n0": "h0:7070"},
	}
	st := storage.Stats{Keys: 5, ApproxBytes: 1024, NextSeq: 6, SSTables: 1, Immutable: 0}

	got := statusPayload(cfg, 90*time.Second, st)
	if got.NodeID != "n0" || got.UptimeSeconds != 90 {
		t.Fatalf("status basics wrong: %+v", got)
	}
	if got.Config.N != 3 || got.Config.RequestTimeoutMS != 2000 || got.Config.VNodes != 128 {
		t.Fatalf("config wrong: %+v", got.Config)
	}
	if got.Storage.MemtableKeys != 5 || got.Storage.NextSeq != 6 || got.Storage.SSTables != 1 {
		t.Fatalf("storage wrong: %+v", got.Storage)
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatalf("status must marshal to JSON: %v", err)
	}
}

func TestMembersPayload(t *testing.T) {
	ms := []membership.Member{
		{ID: "n0", State: membership.Alive, Incarnation: 1},
		{ID: "n1", State: membership.Suspect, Incarnation: 2},
		{ID: "n2", State: membership.Dead, Incarnation: 3},
	}
	got := membersPayload(ms)
	if len(got.Members) != 3 {
		t.Fatalf("want 3 members, got %d", len(got.Members))
	}
	if got.Members[0].State != "alive" || got.Members[1].State != "suspect" || got.Members[2].State != "dead" {
		t.Fatalf("state strings wrong: %+v", got.Members)
	}
}

func TestMembersPayloadEmptyIsNonNilSlice(t *testing.T) {
	got := membersPayload(nil)
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// An empty member list must serialize as [] (a non-null array), so the console can iterate it.
	if string(b) != `{"members":[]}` {
		t.Fatalf("empty members should be []: got %s", b)
	}
}

func TestRingHandler(t *testing.T) {
	ring := cluster.NewRing(64)
	for _, id := range []string{"n0", "n1", "n2"} {
		ring.Add(id)
	}
	d := &Daemon{cfg: Config{VNodes: 64, N: 3}, ring: ring}

	rr := httptest.NewRecorder()
	d.handleRing(rr, httptest.NewRequest(http.MethodGet, "/api/v1/ring", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("ring status = %d", rr.Code)
	}
	var rj ringJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &rj); err != nil {
		t.Fatalf("ring json: %v", err)
	}
	if rj.VNodes != 64 || len(rj.Nodes) != 3 {
		t.Fatalf("ring payload wrong: %+v", rj)
	}
}

func TestKeyHandlerRoutedThroughMux(t *testing.T) {
	ring := cluster.NewRing(64)
	for _, id := range []string{"n0", "n1", "n2"} {
		ring.Add(id)
	}
	d := &Daemon{cfg: Config{VNodes: 64, N: 3}, ring: ring}

	// Route through a real mux so the {key} path value is populated (Go 1.22 pattern routing).
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/key/{key}", d.handleKey)

	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/key/order:5005", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("key status = %d", rr.Code)
	}
	var kj keyJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &kj); err != nil {
		t.Fatalf("key json: %v", err)
	}
	if kj.Key != "order:5005" || kj.N != 3 || len(kj.PreferenceList) != 3 {
		t.Fatalf("key payload wrong: %+v", kj)
	}
	valid := map[string]bool{"n0": true, "n1": true, "n2": true}
	for _, id := range kj.PreferenceList {
		if !valid[id] {
			t.Fatalf("preference list has unknown node %q", id)
		}
	}
}
