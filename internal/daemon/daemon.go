// Package daemon assembles a running Helix node from the pieces built across Phase 7: a
// storage engine, a coordinator over a gRPC transport, a SWIM membership engine over a gRPC
// messenger, and a gRPC server that hosts both the data and membership planes on one
// listener, optionally secured with mutual TLS. It runs the background loops a live node
// needs (membership probing, anti-entropy repair, hint delivery) and shuts them down
// cleanly. The cmd/kvnode binary is a thin wrapper around this package.
package daemon

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"google.golang.org/grpc"

	"github.com/talifpathan/helix/internal/cluster"
	"github.com/talifpathan/helix/internal/membership"
	"github.com/talifpathan/helix/internal/metrics"
	"github.com/talifpathan/helix/internal/rpc"
	"github.com/talifpathan/helix/internal/storage"
	"github.com/talifpathan/helix/internal/tlsutil"
)

// Config is a fully resolved node configuration. Peers maps every node id in the cluster
// (including this node) to its dial address. TLS is enabled when the three cert paths are
// set; leaving them empty runs plaintext, which is fine for local clusters. Listener is
// optional and mainly for tests: when set, Start serves on it instead of binding BindAddr.
type Config struct {
	NodeID   string
	BindAddr string
	Peers    map[string]string
	N, R, W  int
	VNodes   int
	MaxHints int

	TLSCert string
	TLSKey  string
	TLSCA   string

	MetricsAddr string // if set, serve /metrics and /healthz over HTTP here

	Storage storage.Options

	SwimInterval        time.Duration
	AntiEntropyInterval time.Duration
	HintInterval        time.Duration
	RequestTimeout      time.Duration // per-replica RPC bound in the coordinator; 0 uses the default

	Listener net.Listener
	Logger   *slog.Logger
}

func (c *Config) withDefaults() {
	if c.VNodes == 0 {
		c.VNodes = 128
	}
	if c.N == 0 {
		c.N = 3
	}
	if c.R == 0 {
		c.R = 2
	}
	if c.W == 0 {
		c.W = 2
	}
	if c.MaxHints == 0 {
		c.MaxHints = c.N
	}
	if c.SwimInterval == 0 {
		c.SwimInterval = time.Second
	}
	if c.AntiEntropyInterval == 0 {
		c.AntiEntropyInterval = 30 * time.Second
	}
	if c.HintInterval == 0 {
		c.HintInterval = 10 * time.Second
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = 2 * time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

func (c Config) validate() error {
	if c.NodeID == "" {
		return fmt.Errorf("daemon: NodeID is required")
	}
	if c.BindAddr == "" && c.Listener == nil {
		return fmt.Errorf("daemon: BindAddr or Listener is required")
	}
	if len(c.Peers) == 0 {
		return fmt.Errorf("daemon: Peers must not be empty")
	}
	if _, ok := c.Peers[c.NodeID]; !ok {
		return fmt.Errorf("daemon: Peers must include this node %q", c.NodeID)
	}
	set := 0
	for _, p := range []string{c.TLSCert, c.TLSKey, c.TLSCA} {
		if p != "" {
			set++
		}
	}
	if set != 0 && set != 3 {
		return fmt.Errorf("daemon: TLS needs cert, key, and CA together (or none)")
	}
	return nil
}

func (c Config) tlsEnabled() bool { return c.TLSCert != "" }

// Daemon is a running node. Build one with New, then Start it; Stop shuts it down.
type Daemon struct {
	cfg  Config
	log  *slog.Logger
	ids  []string
	node *cluster.LocalNode

	peers     *rpc.PeerRegistry
	transport *rpc.GRPCTransport
	messenger *rpc.GRPCMessenger
	swim      *membership.Swim
	coord     *cluster.Coordinator
	ring      *cluster.Ring
	server    *grpc.Server

	lis        net.Listener
	loopCtx    context.Context
	loopCancel context.CancelFunc
	wg         sync.WaitGroup

	eng       *storage.Engine
	startedAt time.Time
	reg       *metrics.Registry
	gUp       *metrics.GaugeVec
	gUptime   *metrics.GaugeVec
	gMembers  *metrics.GaugeVec
	gMemKeys  *metrics.GaugeVec
	gMemBytes *metrics.GaugeVec
	gSSTables *metrics.GaugeVec
	gImmut    *metrics.GaugeVec
	admin     *http.Server
	adminLis  net.Listener
}

// New builds a node from cfg. It opens the storage engine and wires the coordinator, SWIM
// engine, and gRPC server, but does not begin serving until Start.
func New(cfg Config) (*Daemon, error) {
	cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	log := cfg.Logger.With("node", cfg.NodeID)

	eng, err := storage.Open(cfg.Storage)
	if err != nil {
		return nil, fmt.Errorf("daemon: open storage: %w", err)
	}
	node := cluster.NewLocalNode(cfg.NodeID, eng)

	ids := sortedKeys(cfg.Peers)
	ring := cluster.NewRing(cfg.VNodes)
	for _, id := range ids {
		ring.Add(id)
	}
	node.SetPreferenceFunc(ring.LookupN)

	peers := rpc.NewPeerRegistryFromMap(cfg.Peers)

	var dialOpts []grpc.DialOption
	var serverOpts []grpc.ServerOption
	if cfg.tlsEnabled() {
		clientCfg, err := tlsutil.ClientConfigFromFiles(cfg.TLSCert, cfg.TLSKey, cfg.TLSCA)
		if err != nil {
			_ = node.Close()
			return nil, fmt.Errorf("daemon: load client TLS: %w", err)
		}
		serverCfg, err := tlsutil.ServerConfigFromFiles(cfg.TLSCert, cfg.TLSKey, cfg.TLSCA)
		if err != nil {
			_ = node.Close()
			return nil, fmt.Errorf("daemon: load server TLS: %w", err)
		}
		dialOpts = append(dialOpts, rpc.ClientTLSOption(clientCfg))
		serverOpts = append(serverOpts, rpc.ServerTLSOption(serverCfg))
	}

	transport := rpc.NewGRPCTransport(peers, dialOpts...)
	messenger := rpc.NewGRPCMessenger(peers, dialOpts...)

	swim := membership.NewSwim(cfg.NodeID, messenger, ids, membership.SwimConfig{
		PingTimeout:    2 * time.Second,
		IndirectProbes: 2,
		SuspicionTicks: 4,
		Logger:         log,
	})
	coord := cluster.NewCoordinator(ring, transport, cfg.N, cfg.R, cfg.W, cfg.MaxHints, nil, log)
	server := rpc.NewServer(node, swim, serverOpts...)
	rpc.RegisterClientService(server, coord) // the application-facing plane, coordinated

	reg := metrics.NewRegistry()
	d := &Daemon{
		cfg: cfg, log: log, ids: ids, node: node,
		peers: peers, transport: transport, messenger: messenger,
		swim: swim, coord: coord, ring: ring, server: server,
		eng: eng, startedAt: time.Now(), reg: reg,
		gUp:       reg.NewGauge("helix_up", "1 if the node process is running"),
		gUptime:   reg.NewGauge("helix_uptime_seconds", "seconds since the node started"),
		gMembers:  reg.NewGauge("helix_cluster_members", "cluster members observed by this node, by state", "state"),
		gMemKeys:  reg.NewGauge("helix_storage_memtable_keys", "keys in the active memtable"),
		gMemBytes: reg.NewGauge("helix_storage_memtable_bytes", "approximate active memtable size in bytes"),
		gSSTables: reg.NewGauge("helix_storage_sstables", "number of on-disk sstables"),
		gImmut:    reg.NewGauge("helix_storage_immutable_memtables", "sealed memtables awaiting flush"),
	}
	d.gUp.With().Set(1)

	// Coordinator hot-path metrics, wired through the cluster.Metrics interface so the cluster
	// package stays decoupled from this registry.
	requests := reg.NewCounter("helix_requests_total", "coordinator requests by operation and result", "op", "result")
	latency := reg.NewHistogram("helix_request_duration_seconds", "coordinator request latency in seconds", metrics.DefaultLatencyBuckets, "op")
	coord.SetMetrics(&coordMetrics{requests: requests, latency: latency})
	coord.SetRequestTimeout(cfg.RequestTimeout)
	// Skip replicas the local membership view considers Dead: fast-fail them instead of dialing
	// and waiting out the per-attempt timeout. Only Dead is skipped; Suspect and unknown nodes are
	// still attempted, so a false suspicion never routes traffic away from a healthy node.
	coord.SetLiveness(func(id string) bool {
		st, known := swim.List().StateOf(id)
		if !known {
			return true
		}
		return st != membership.Dead
	})

	return d, nil
}

// coordMetrics adapts the metrics registry to the cluster.Metrics interface.
type coordMetrics struct {
	requests *metrics.CounterVec
	latency  *metrics.HistogramVec
}

var _ cluster.Metrics = (*coordMetrics)(nil)

func (m *coordMetrics) ObserveRequest(op, result string, seconds float64) {
	m.requests.With(op, result).Inc()
	m.latency.With(op).Observe(seconds)
}

// Start binds the listener (or uses cfg.Listener), serves both planes, and launches the
// background loops. It returns once serving has begun.
func (d *Daemon) Start() error {
	lis := d.cfg.Listener
	if lis == nil {
		l, err := net.Listen("tcp", d.cfg.BindAddr)
		if err != nil {
			return fmt.Errorf("daemon: listen %s: %w", d.cfg.BindAddr, err)
		}
		lis = l
	}
	d.lis = lis
	go func() { _ = d.server.Serve(lis) }()

	d.loopCtx, d.loopCancel = context.WithCancel(context.Background())
	d.wg.Add(3)
	go d.loop(d.cfg.SwimInterval, func(ctx context.Context) { d.swim.RunOnce(ctx) })
	go d.loop(d.cfg.AntiEntropyInterval, d.repairAll)
	go d.loop(d.cfg.HintInterval, func(ctx context.Context) {
		if n := d.node.DeliverHints(ctx, d.transport); n > 0 {
			d.log.Info("delivered hints", "count", n)
		}
	})

	d.log.Info("daemon serving",
		"addr", lis.Addr().String(), "tls", d.cfg.tlsEnabled(),
		"peers", len(d.cfg.Peers), "n", d.cfg.N, "r", d.cfg.R, "w", d.cfg.W)

	if err := d.startAdmin(); err != nil {
		return fmt.Errorf("daemon: start metrics server: %w", err)
	}
	return nil
}

// startAdmin serves /metrics and /healthz over HTTP when MetricsAddr is set. Metrics are
// refreshed on each scrape so they reflect current state without a background loop.
func (d *Daemon) startAdmin() error {
	if d.cfg.MetricsAddr == "" {
		return nil
	}
	lis, err := net.Listen("tcp", d.cfg.MetricsAddr)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		d.refreshMetrics()
		d.reg.Handler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	// Read-only status API (JSON) for the operator console's gateway to aggregate: config and
	// storage state, SWIM membership, the ring, and a key's preference list.
	mux.HandleFunc("GET /api/v1/status", d.handleStatus)
	mux.HandleFunc("GET /api/v1/members", d.handleMembers)
	mux.HandleFunc("GET /api/v1/ring", d.handleRing)
	mux.HandleFunc("GET /api/v1/key/{key}", d.handleKey)
	d.adminLis = lis
	d.admin = &http.Server{Handler: mux}
	go func() { _ = d.admin.Serve(lis) }()
	d.log.Info("metrics serving", "addr", lis.Addr().String())
	return nil
}

// refreshMetrics samples current membership and storage state into the gauges.
func (d *Daemon) refreshMetrics() {
	d.gUptime.With().Set(int64(time.Since(d.startedAt).Seconds()))

	st := d.eng.Stats()
	d.gMemKeys.With().Set(int64(st.Keys))
	d.gMemBytes.With().Set(st.ApproxBytes)
	d.gSSTables.With().Set(int64(st.SSTables))
	d.gImmut.With().Set(int64(st.Immutable))

	var alive, suspect, dead int
	for _, m := range d.swim.List().Members() {
		switch m.State {
		case membership.Alive:
			alive++
		case membership.Suspect:
			suspect++
		case membership.Dead:
			dead++
		}
	}
	d.gMembers.With("alive").Set(int64(alive))
	d.gMembers.With("suspect").Set(int64(suspect))
	d.gMembers.With("dead").Set(int64(dead))
}

// MetricsAddr returns the address the admin HTTP server is bound to, or "" if disabled.
func (d *Daemon) MetricsAddr() string {
	if d.adminLis != nil {
		return d.adminLis.Addr().String()
	}
	return ""
}

// Stop cancels the loops, waits for them, then gracefully stops the server and closes the
// transport, messenger, and storage engine.
func (d *Daemon) Stop() error {
	if d.admin != nil {
		_ = d.admin.Close()
	}
	if d.loopCancel != nil {
		d.loopCancel()
	}
	d.wg.Wait()
	d.server.GracefulStop()

	var firstErr error
	if err := d.transport.Close(); err != nil {
		firstErr = err
	}
	if err := d.messenger.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := d.node.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	d.log.Info("daemon stopped")
	return firstErr
}

// loop runs fn on an interval until the loop context is cancelled.
func (d *Daemon) loop(interval time.Duration, fn func(context.Context)) {
	defer d.wg.Done()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-d.loopCtx.Done():
			return
		case <-t.C:
			fn(d.loopCtx)
		}
	}
}

// repairAll runs one anti-entropy round between this node and every other node, scoped to
// the keys each pair co-replicates.
func (d *Daemon) repairAll(ctx context.Context) {
	for _, peer := range d.ids {
		if peer == d.cfg.NodeID {
			continue
		}
		rb, ok := d.transport.Replica(peer)
		if !ok {
			continue
		}
		scope := cluster.RepairScope{NodeA: d.cfg.NodeID, NodeB: peer, N: d.cfg.N}
		n, err := cluster.Repair(ctx, d.node, rb, scope)
		if err != nil {
			d.log.Debug("anti-entropy repair failed", "peer", peer, "error", err)
			continue
		}
		if n > 0 {
			d.log.Info("anti-entropy reconciled keys", "peer", peer, "keys", n)
		}
	}
}

// Put, Get, and Delete drive the coordinator, going through the quorum across the cluster.
func (d *Daemon) Put(ctx context.Context, key, value []byte) error {
	return d.coord.Put(ctx, key, value)
}

func (d *Daemon) Get(ctx context.Context, key []byte) ([]byte, error) {
	return d.coord.Get(ctx, key)
}

func (d *Daemon) Delete(ctx context.Context, key []byte) error {
	return d.coord.Delete(ctx, key)
}

// Members returns this node's view of cluster membership.
func (d *Daemon) Members() *membership.MemberList { return d.swim.List() }

// Addr returns the address the node is actually serving on.
func (d *Daemon) Addr() string {
	if d.lis != nil {
		return d.lis.Addr().String()
	}
	return d.cfg.BindAddr
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
