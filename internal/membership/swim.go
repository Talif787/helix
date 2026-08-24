package membership

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

// SwimConfig configures the protocol engine.
type SwimConfig struct {
	Period         time.Duration // wall-clock time between protocol rounds when Start is used
	PingTimeout    time.Duration // how long to wait for a direct or indirect ack
	IndirectProbes int           // number of relays asked to probe when a direct ping fails
	SuspicionTicks uint64        // rounds a member stays suspect before being declared dead
	GossipFanout   int           // times a change is retransmitted
	MaxPiggyback   int           // max updates per message
	Rand           *rand.Rand
	Logger         *slog.Logger
}

func (c *SwimConfig) withDefaults() {
	if c.Period <= 0 {
		c.Period = 200 * time.Millisecond
	}
	if c.PingTimeout <= 0 {
		c.PingTimeout = 50 * time.Millisecond
	}
	if c.IndirectProbes <= 0 {
		c.IndirectProbes = 2
	}
	if c.Rand == nil {
		c.Rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
}

// Swim runs the membership protocol for one node: it probes peers, marks failures suspect
// then dead, refutes suspicion about itself, and gossips changes. It implements Handler so
// peers can probe it through the messenger.
type Swim struct {
	id   string
	list *MemberList
	msg  Messenger
	cfg  SwimConfig
	log  *slog.Logger

	stop chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

// NewSwim builds an engine for node id over msg, seeded with the given peer ids. It
// registers itself as a handler on an InProcessMessenger if one is provided.
func NewSwim(id string, msg Messenger, peers []string, cfg SwimConfig) *Swim {
	cfg.withDefaults()
	ml := NewMemberList(id, Config{
		SuspicionTicks: cfg.SuspicionTicks,
		GossipFanout:   cfg.GossipFanout,
		MaxPiggyback:   cfg.MaxPiggyback,
		Rand:           cfg.Rand,
	})
	for _, p := range peers {
		if p != id {
			ml.Join(p)
		}
	}
	s := &Swim{
		id:   id,
		list: ml,
		msg:  msg,
		cfg:  cfg,
		log:  cfg.Logger.With("node", id),
		stop: make(chan struct{}),
	}
	if ipm, ok := msg.(*InProcessMessenger); ok {
		ipm.Register(id, s)
	}
	return s
}

// List exposes the member list for observation.
func (s *Swim) List() *MemberList { return s.list }

// RunOnce executes exactly one protocol round: it advances the suspicion clock, picks the
// next peer, probes it directly and then indirectly, and marks it suspect if both fail. It
// is separate from Start so tests can drive the protocol deterministically.
func (s *Swim) RunOnce(ctx context.Context) {
	if dead := s.list.Tick(); len(dead) > 0 {
		for _, u := range dead {
			s.log.Info("member declared dead", "member", u.ID, "incarnation", u.Incarnation)
		}
	}

	target, ok := s.list.NextProbeTarget()
	if !ok {
		return
	}

	if s.probe(ctx, target) {
		return
	}
	if s.indirectProbe(ctx, target) {
		return
	}
	if u, changed := s.list.Suspect(target); changed {
		s.log.Info("member suspected", "member", u.ID, "incarnation", u.Incarnation)
	}
}

// probe sends a direct ping and applies any gossip in the ack. It reports success.
func (s *Swim) probe(ctx context.Context, target string) bool {
	pctx, cancel := context.WithTimeout(ctx, s.cfg.PingTimeout)
	defer cancel()
	ack, err := s.msg.Ping(pctx, s.id, target, s.outgoing(target))
	if err != nil {
		return false
	}
	s.list.Apply(ack)
	return true
}

// outgoing builds the gossip for a message to peer. It always advertises this node's own
// current membership (so peers learn its incarnation and can revive it if they had it
// recorded dead), carries the normal dissemination gossip, and appends any negative news
// this node holds about peer (so a reachable peer can refute a false suspicion or a stale
// death about itself).
func (s *Swim) outgoing(peer string) []Update {
	gossip := s.list.Gossip()
	gossip = append(gossip, s.list.Self())
	if peer != "" {
		if u, ok := s.list.NewsAbout(peer); ok {
			gossip = append(gossip, u)
		}
	}
	return gossip
}

// indirectProbe asks a few random peers to probe the target on this node's behalf.
func (s *Swim) indirectProbe(ctx context.Context, target string) bool {
	relays := s.list.RandomPeers(s.cfg.IndirectProbes, target)
	for _, relay := range relays {
		pctx, cancel := context.WithTimeout(ctx, s.cfg.PingTimeout)
		ack, err := s.msg.PingReq(pctx, s.id, relay, target, s.outgoing(relay))
		cancel()
		if err == nil {
			s.list.Apply(ack)
			return true
		}
	}
	return false
}

// HandlePing applies the caller's gossip and returns this node's gossip as the ack,
// advertising itself and any negative news about the caller so a caller others think is
// dead can refute.
func (s *Swim) HandlePing(from string, gossip []Update) []Update {
	s.list.Apply(gossip)
	return s.outgoing(from)
}

// HandlePingReq probes target on the caller's behalf and relays the outcome, telling the
// target of any negative news so a reachable target refutes.
func (s *Swim) HandlePingReq(ctx context.Context, from string, target string, gossip []Update) ([]Update, error) {
	s.list.Apply(gossip)
	pctx, cancel := context.WithTimeout(ctx, s.cfg.PingTimeout)
	defer cancel()
	ack, err := s.msg.Ping(pctx, s.id, target, s.outgoing(target))
	if err != nil {
		return nil, err
	}
	s.list.Apply(ack)
	return s.outgoing(from), nil
}

// Start runs the protocol on a ticker until Stop is called.
func (s *Swim) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(s.cfg.Period)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				s.RunOnce(context.Background())
			}
		}
	}()
}

// Stop halts the protocol loop. It is safe to call more than once.
func (s *Swim) Stop() {
	s.once.Do(func() { close(s.stop) })
	s.wg.Wait()
}
