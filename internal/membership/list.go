package membership

import (
	"math/rand"
	"sort"
	"sync"
)

type memberRec struct {
	Member
	suspectTick uint64 // logical tick when marked suspect, for the suspicion timeout
	gossipLeft  int    // remaining times to piggyback this member's state on outgoing messages
}

// MemberList is a node's view of the cluster. It is safe for concurrent use. It owns the
// local node's incarnation and refutes suspicion about itself, tracks a logical tick used
// for the suspicion timeout, keeps a round-robin probe order, and holds a dissemination
// buffer so recent changes ride on a bounded number of outgoing messages.
type MemberList struct {
	mu sync.Mutex

	self    string
	selfInc uint64

	members map[string]*memberRec

	tick           uint64
	suspicionTicks uint64
	gossipFanout   int // times each change is retransmitted
	maxPiggyback   int // max updates per outgoing message

	rnd        *rand.Rand
	probeOrder []string
	probeIdx   int
}

// Config parameters for a MemberList.
type Config struct {
	SuspicionTicks uint64 // ticks a member stays suspect before being declared dead
	GossipFanout   int    // times a change is retransmitted via gossip
	MaxPiggyback   int    // max updates carried on one message
	Rand           *rand.Rand
}

// NewMemberList creates a list for the local node self, seeded with self as Alive at
// incarnation 0.
func NewMemberList(self string, cfg Config) *MemberList {
	if cfg.SuspicionTicks == 0 {
		cfg.SuspicionTicks = 4
	}
	if cfg.GossipFanout <= 0 {
		cfg.GossipFanout = 3
	}
	if cfg.MaxPiggyback <= 0 {
		cfg.MaxPiggyback = 6
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.New(rand.NewSource(1))
	}
	ml := &MemberList{
		self:           self,
		members:        make(map[string]*memberRec),
		suspicionTicks: cfg.SuspicionTicks,
		gossipFanout:   cfg.GossipFanout,
		maxPiggyback:   cfg.MaxPiggyback,
		rnd:            cfg.Rand,
	}
	ml.members[self] = &memberRec{Member: Member{ID: self, Incarnation: 0, State: Alive}}
	return ml
}

// Join adds id as Alive if not already known, and schedules it for gossip.
func (ml *MemberList) Join(id string) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	if _, ok := ml.members[id]; ok {
		return
	}
	ml.members[id] = &memberRec{Member: Member{ID: id, State: Alive}, gossipLeft: ml.gossipFanout}
}

// Apply merges a batch of updates and returns those that changed local state. It handles
// self-refutation: if an update says the local node is not alive, the node bumps its own
// incarnation above the rumor and re-announces itself Alive, which overrides the suspicion
// everywhere it spreads.
func (ml *MemberList) Apply(updates []Update) []Update {
	ml.mu.Lock()
	defer ml.mu.Unlock()

	var changed []Update
	for _, u := range updates {
		if u.ID == ml.self {
			if u.State != Alive && u.Incarnation >= ml.selfInc {
				ml.selfInc = u.Incarnation + 1
				rec := ml.members[ml.self]
				rec.Incarnation = ml.selfInc
				rec.State = Alive
				rec.gossipLeft = ml.gossipFanout
				changed = append(changed, rec.update())
			}
			continue
		}
		cur, ok := ml.members[u.ID]
		if !ok {
			cur = &memberRec{Member: Member{ID: u.ID}}
			ml.members[u.ID] = cur
		}
		won, diff := merge(cur.Member, Member{ID: u.ID, Incarnation: u.Incarnation, State: u.State})
		if diff {
			cur.Member = won
			cur.gossipLeft = ml.gossipFanout
			if won.State == Suspect {
				cur.suspectTick = ml.tick
			}
			changed = append(changed, won.update())
		}
	}
	return changed
}

// Suspect marks id suspect at its currently known incarnation (used when a probe fails).
// It returns the resulting update, if any, so the caller can gossip it.
func (ml *MemberList) Suspect(id string) (Update, bool) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	cur, ok := ml.members[id]
	if !ok || id == ml.self {
		return Update{}, false
	}
	won, diff := merge(cur.Member, Member{ID: id, Incarnation: cur.Incarnation, State: Suspect})
	if !diff {
		return Update{}, false
	}
	cur.Member = won
	cur.suspectTick = ml.tick
	cur.gossipLeft = ml.gossipFanout
	return won.update(), true
}

// Tick advances the logical clock and declares dead any member that has been suspect for at
// least SuspicionTicks. It returns the members newly declared dead.
func (ml *MemberList) Tick() []Update {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.tick++
	var dead []Update
	for _, rec := range ml.members {
		if rec.State == Suspect && ml.tick-rec.suspectTick >= ml.suspicionTicks {
			rec.State = Dead
			rec.gossipLeft = ml.gossipFanout
			dead = append(dead, rec.update())
		}
	}
	return dead
}

// Gossip returns up to MaxPiggyback updates to attach to an outgoing message, preferring
// the freshest changes, and decrements their remaining retransmit budget.
func (ml *MemberList) Gossip() []Update {
	ml.mu.Lock()
	defer ml.mu.Unlock()

	pending := make([]*memberRec, 0, len(ml.members))
	for _, rec := range ml.members {
		if rec.gossipLeft > 0 {
			pending = append(pending, rec)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].gossipLeft != pending[j].gossipLeft {
			return pending[i].gossipLeft > pending[j].gossipLeft
		}
		return pending[i].ID < pending[j].ID
	})
	if len(pending) > ml.maxPiggyback {
		pending = pending[:ml.maxPiggyback]
	}
	out := make([]Update, 0, len(pending))
	for _, rec := range pending {
		out = append(out, rec.update())
		rec.gossipLeft--
	}
	return out
}

// NextProbeTarget returns the next non-self member to probe, cycling through a reshuffled
// order each round so every member is probed once per round. Dead members are skipped.
func (ml *MemberList) NextProbeTarget() (string, bool) {
	ml.mu.Lock()
	defer ml.mu.Unlock()

	for attempts := 0; attempts < 2; attempts++ {
		for ml.probeIdx < len(ml.probeOrder) {
			id := ml.probeOrder[ml.probeIdx]
			ml.probeIdx++
			if rec, ok := ml.members[id]; ok && id != ml.self && rec.State != Dead {
				return id, true
			}
		}
		ml.reshuffleLocked()
		if len(ml.probeOrder) == 0 {
			return "", false
		}
	}
	return "", false
}

func (ml *MemberList) reshuffleLocked() {
	ml.probeOrder = ml.probeOrder[:0]
	for id, rec := range ml.members {
		if id != ml.self && rec.State != Dead {
			ml.probeOrder = append(ml.probeOrder, id)
		}
	}
	sort.Strings(ml.probeOrder) // stable base before shuffle, for reproducibility
	ml.rnd.Shuffle(len(ml.probeOrder), func(i, j int) {
		ml.probeOrder[i], ml.probeOrder[j] = ml.probeOrder[j], ml.probeOrder[i]
	})
	ml.probeIdx = 0
}

// RandomPeers returns up to k random members that are not self, not dead, and not excluded.
func (ml *MemberList) RandomPeers(k int, exclude string) []string {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	candidates := make([]string, 0, len(ml.members))
	for id, rec := range ml.members {
		if id != ml.self && id != exclude && rec.State != Dead {
			candidates = append(candidates, id)
		}
	}
	sort.Strings(candidates)
	ml.rnd.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	if len(candidates) > k {
		candidates = candidates[:k]
	}
	return candidates
}

// Members returns a snapshot of all members sorted by id.
func (ml *MemberList) Members() []Member {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	out := make([]Member, 0, len(ml.members))
	for _, rec := range ml.members {
		out = append(out, rec.Member)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Self returns the local node's own current membership as an update. A node advertises
// this on every outgoing message so peers always learn its current incarnation; that is
// what lets a node others had recorded as dead be revived as soon as it exchanges a
// message with them, without relying on the bounded gossip retransmit budget.
func (ml *MemberList) Self() Update {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	return ml.members[ml.self].update()
}

// NewsAbout returns the current update for id when this node holds negative news about it
// (Suspect or Dead). Attaching it to a message to that peer lets the peer refute if it is
// actually alive, which is how a falsely suspected node clears itself and how a restarted
// node (which others still record as dead) is revived.
func (ml *MemberList) NewsAbout(id string) (Update, bool) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	rec, ok := ml.members[id]
	if !ok || rec.State == Alive {
		return Update{}, false
	}
	return rec.update(), true
}

// StateOf returns the known state of id and whether it is known.
func (ml *MemberList) StateOf(id string) (State, bool) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	rec, ok := ml.members[id]
	if !ok {
		return Alive, false
	}
	return rec.State, true
}

// AliveCount returns how many members are currently Alive (including self).
func (ml *MemberList) AliveCount() int {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	n := 0
	for _, rec := range ml.members {
		if rec.State == Alive {
			n++
		}
	}
	return n
}
