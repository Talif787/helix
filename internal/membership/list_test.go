package membership

import (
	"math/rand"
	"testing"
)

func TestMergePrecedence(t *testing.T) {
	m := func(inc uint64, s State) Member { return Member{ID: "x", Incarnation: inc, State: s} }
	cases := []struct {
		name     string
		cur, in  Member
		want     Member
		wantDiff bool
	}{
		{"higher incarnation wins", m(1, Alive), m(2, Alive), m(2, Alive), true},
		{"lower incarnation loses even if dead", m(2, Alive), m(1, Dead), m(2, Alive), false},
		{"suspect beats alive same inc", m(1, Alive), m(1, Suspect), m(1, Suspect), true},
		{"alive cannot revive suspect same inc", m(1, Suspect), m(1, Alive), m(1, Suspect), false},
		{"dead beats suspect same inc", m(1, Suspect), m(1, Dead), m(1, Dead), true},
		{"dead terminal at same inc", m(1, Dead), m(1, Alive), m(1, Dead), false},
		{"rejoin with higher inc revives", m(1, Dead), m(2, Alive), m(2, Alive), true},
	}
	for _, tc := range cases {
		got, diff := merge(tc.cur, tc.in)
		if got != tc.want || diff != tc.wantDiff {
			t.Errorf("%s: merge=%v diff=%v, want %v diff=%v", tc.name, got, diff, tc.want, tc.wantDiff)
		}
	}
}

func TestApplySelfRefutation(t *testing.T) {
	ml := NewMemberList("self", Config{})
	// A rumor that self is suspect at incarnation 5 must be refuted with a higher one.
	changed := ml.Apply([]Update{{ID: "self", Incarnation: 5, State: Suspect}})
	if len(changed) != 1 {
		t.Fatalf("expected a refutation update, got %v", changed)
	}
	if changed[0].State != Alive || changed[0].Incarnation <= 5 {
		t.Fatalf("refutation should be Alive at incarnation > 5, got %+v", changed[0])
	}
	if st, _ := ml.StateOf("self"); st != Alive {
		t.Fatalf("self should be alive after refutation, got %v", st)
	}
}

func TestSuspectThenTickDeclaresDead(t *testing.T) {
	ml := NewMemberList("self", Config{SuspicionTicks: 3})
	ml.Join("b")

	if _, ok := ml.Suspect("b"); !ok {
		t.Fatal("expected b to become suspect")
	}
	if st, _ := ml.StateOf("b"); st != Suspect {
		t.Fatalf("b should be suspect, got %v", st)
	}
	// Not dead before the timeout.
	for i := 0; i < 2; i++ {
		if dead := ml.Tick(); len(dead) != 0 {
			t.Fatalf("declared dead too early at tick %d: %v", i, dead)
		}
	}
	// Dead at the timeout.
	dead := ml.Tick()
	if len(dead) != 1 || dead[0].ID != "b" || dead[0].State != Dead {
		t.Fatalf("expected b declared dead, got %v", dead)
	}
}

func TestGossipRetransmitBudget(t *testing.T) {
	ml := NewMemberList("self", Config{GossipFanout: 2, MaxPiggyback: 10})
	ml.Apply([]Update{{ID: "b", Incarnation: 1, State: Suspect}})

	// The change about b should be handed out exactly GossipFanout times, then stop.
	count := 0
	for i := 0; i < 5; i++ {
		for _, u := range ml.Gossip() {
			if u.ID == "b" {
				count++
			}
		}
	}
	if count != 2 {
		t.Fatalf("expected b gossiped 2 times, got %d", count)
	}
}

func TestNextProbeTargetCoversAllInOneRound(t *testing.T) {
	ml := NewMemberList("self", Config{Rand: rand.New(rand.NewSource(42))})
	for _, id := range []string{"a", "b", "c", "d"} {
		ml.Join(id)
	}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		id, ok := ml.NextProbeTarget()
		if !ok {
			t.Fatalf("expected a target at step %d", i)
		}
		if seen[id] {
			t.Fatalf("member %s probed twice within one round", id)
		}
		seen[id] = true
	}
	if len(seen) != 4 {
		t.Fatalf("expected all 4 members probed in one round, saw %v", seen)
	}
}

func TestDeadMembersNotProbed(t *testing.T) {
	ml := NewMemberList("self", Config{})
	ml.Join("a")
	ml.Apply([]Update{{ID: "a", Incarnation: 0, State: Dead}})
	if id, ok := ml.NextProbeTarget(); ok {
		t.Fatalf("dead member should not be probed, got %q", id)
	}
}
