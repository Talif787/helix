package membership

import (
	"context"
	"math/rand"
	"testing"
	"time"
)

// newCluster builds n in-process swim engines sharing one messenger, all seeded with the
// full peer set. Rand is fixed so probe order is reproducible.
func newSwimSet(t *testing.T, ids []string) (*InProcessMessenger, map[string]*Swim) {
	t.Helper()
	msg := NewInProcessMessenger()
	engines := make(map[string]*Swim, len(ids))
	for i, id := range ids {
		cfg := SwimConfig{
			SuspicionTicks: 3,
			IndirectProbes: 2,
			Rand:           rand.New(rand.NewSource(int64(i) + 1)),
		}
		engines[id] = NewSwim(id, msg, ids, cfg)
	}
	return msg, engines
}

// runRounds drives every engine through r protocol rounds deterministically (no timers).
func runRounds(engines map[string]*Swim, r int) {
	ctx := context.Background()
	// A stable order keeps the test reproducible.
	order := make([]string, 0, len(engines))
	for id := range engines {
		order = append(order, id)
	}
	for i := 0; i < r; i++ {
		for _, id := range order {
			engines[id].RunOnce(ctx)
		}
	}
}

func allSee(engines map[string]*Swim, target string, want State, exclude map[string]bool) bool {
	for id, e := range engines {
		if exclude[id] {
			continue
		}
		if st, ok := e.List().StateOf(target); !ok || st != want {
			return false
		}
	}
	return true
}

func TestSwimHealthyClusterStaysAlive(t *testing.T) {
	ids := []string{"n0", "n1", "n2", "n3", "n4"}
	_, engines := newSwimSet(t, ids)
	runRounds(engines, 10)
	for _, target := range ids {
		if !allSee(engines, target, Alive, nil) {
			t.Fatalf("healthy cluster should see %s alive everywhere", target)
		}
	}
}

func TestSwimDetectsAndDisseminatesFailure(t *testing.T) {
	ids := []string{"n0", "n1", "n2", "n3", "n4"}
	msg, engines := newSwimSet(t, ids)

	dead := "n4"
	msg.Deregister(dead)  // model a crash: probes to n4 now fail
	delete(engines, dead) // the crashed node stops running rounds

	// Enough rounds for round-robin probing to hit n4, suspicion to time out, and the
	// death to gossip to everyone: one round to probe, SuspicionTicks to die, several to
	// spread.
	runRounds(engines, 25)

	if !allSee(engines, dead, Dead, nil) {
		states := map[string]string{}
		for id, e := range engines {
			st, _ := e.List().StateOf(dead)
			states[id] = st.String()
		}
		t.Fatalf("all live nodes should converge on %s dead, got %v", dead, states)
	}
}

func TestSwimRefutesFalseSuspicion(t *testing.T) {
	ids := []string{"n0", "n1", "n2"}
	_, engines := newSwimSet(t, ids)

	// Inject a false rumor into n0 that n1 is suspect at incarnation 0.
	engines["n0"].List().Apply([]Update{{ID: "n1", Incarnation: 0, State: Suspect}})
	if st, _ := engines["n0"].List().StateOf("n1"); st != Suspect {
		t.Fatal("precondition: n0 should suspect n1")
	}

	// n1 is actually alive; as gossip reaches it, it refutes with a higher incarnation and
	// that alive news spreads back, clearing the suspicion everywhere.
	runRounds(engines, 15)

	if !allSee(engines, "n1", Alive, nil) {
		states := map[string]string{}
		for id, e := range engines {
			st, _ := e.List().StateOf("n1")
			states[id] = st.String()
		}
		t.Fatalf("false suspicion of n1 should be refuted everywhere, got %v", states)
	}
}

func TestSwimNodeRejoinsAfterDeath(t *testing.T) {
	ids := []string{"n0", "n1", "n2", "n3", "n4"}
	msg, engines := newSwimSet(t, ids)

	dead := "n4"
	msg.Deregister(dead)
	delete(engines, dead)
	runRounds(engines, 25)
	if !allSee(engines, dead, Dead, nil) {
		t.Fatalf("precondition: %s should be dead everywhere", dead)
	}

	// The node restarts fresh at incarnation 0. Others still record it dead, so it must be
	// told and refute with a higher incarnation to be revived.
	rejoined := NewSwim(dead, msg, ids, SwimConfig{SuspicionTicks: 3, Rand: rand.New(rand.NewSource(99))})
	engines[dead] = rejoined
	runRounds(engines, 25)

	if !allSee(engines, dead, Alive, nil) {
		states := map[string]string{}
		for id, e := range engines {
			st, _ := e.List().StateOf(dead)
			states[id] = st.String()
		}
		t.Fatalf("rejoined %s should be alive everywhere, got %v", dead, states)
	}
}

func TestSwimStartStopIsClean(t *testing.T) {
	ids := []string{"n0", "n1", "n2"}
	_, engines := newSwimSet(t, ids)
	for _, e := range engines {
		e.cfg.Period = 5 * time.Millisecond
		e.Start()
	}
	defer func() {
		for _, e := range engines {
			e.Stop()
			e.Stop() // idempotent
		}
	}()

	// Wait for convergence rather than sleeping a fixed amount, so the test is robust to a
	// slow or contended CI runner. It cannot hang (bounded deadline) and still fails loudly
	// if convergence never happens.
	deadline := time.Now().Add(2 * time.Second)
	converged := false
	for time.Now().Before(deadline) {
		allAlive := true
		for _, target := range ids {
			if !allSee(engines, target, Alive, nil) {
				allAlive = false
				break
			}
		}
		if allAlive {
			converged = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !converged {
		t.Fatal("cluster did not converge to all-alive within the deadline")
	}
}
