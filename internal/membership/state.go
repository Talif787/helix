// Package membership implements SWIM-style cluster membership and failure detection: each
// node probes peers (directly, then indirectly through others), marks unresponsive peers
// suspect and eventually dead, and gossips these changes so every node converges on the
// same eventually-consistent view of who is up. A suspected node refutes by raising its
// incarnation number, which keeps a transient hiccup from permanently evicting a healthy
// node. In this phase the nodes run in-process behind a Messenger interface; the same seam
// is what a network transport implements later.
package membership

import "fmt"

// State is a member's liveness as seen by the cluster.
type State uint8

const (
	// Alive means the member is responding to probes.
	Alive State = iota
	// Suspect means a probe failed and the member is on a timeout before being declared dead.
	Suspect
	// Dead means the member is considered failed and is routed around.
	Dead
)

func (s State) String() string {
	switch s {
	case Alive:
		return "alive"
	case Suspect:
		return "suspect"
	case Dead:
		return "dead"
	default:
		return fmt.Sprintf("state(%d)", uint8(s))
	}
}

// severity orders states so that a "worse" state wins at an equal incarnation.
func severity(s State) int {
	switch s {
	case Alive:
		return 0
	case Suspect:
		return 1
	case Dead:
		return 2
	default:
		return -1
	}
}

// Update is a piece of membership news about one node: its id, the incarnation the news is
// about, and the state. Updates are what gossip carries and what MemberList.Apply consumes.
type Update struct {
	ID          string
	Incarnation uint64
	State       State
}

// Member is the current view of one node.
type Member struct {
	ID          string
	Incarnation uint64
	State       State
}

func (m Member) update() Update {
	return Update{ID: m.ID, Incarnation: m.Incarnation, State: m.State}
}

// merge decides which of two views of the same member wins. A higher incarnation always
// wins; at an equal incarnation the worse state wins (alive < suspect < dead). This makes
// state monotonic at a given incarnation, so a node clears suspicion only by refuting with
// a higher incarnation. It returns the winner and whether it differs from cur.
func merge(cur, incoming Member) (Member, bool) {
	switch {
	case incoming.Incarnation > cur.Incarnation:
		return incoming, incoming != cur
	case incoming.Incarnation < cur.Incarnation:
		return cur, false
	default:
		if severity(incoming.State) > severity(cur.State) {
			return incoming, true
		}
		return cur, false
	}
}
