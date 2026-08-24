// Command swimdemo runs a small in-process cluster of SWIM membership engines and verifies
// the protocol end to end: the cluster converges to all-alive, a killed node is detected
// and gossiped as dead to every survivor, and a restarted node is revived everywhere. It
// prints a short report and exits non-zero on any failed check, so it doubles as a smoke
// test.
package main

import (
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/talifpathan/helix/internal/membership"
	"github.com/talifpathan/helix/internal/observability"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "swimdemo: FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("swimdemo: PASS")
}

func run() error {
	logger := observability.NewLogger("warn", "text") // quiet; the demo prints its own report
	ids := []string{"node-a", "node-b", "node-c", "node-d", "node-e", "node-f"}

	msg := membership.NewInProcessMessenger()
	engines := map[string]*membership.Swim{}
	cfg := func() membership.SwimConfig {
		return membership.SwimConfig{
			Period:         15 * time.Millisecond,
			PingTimeout:    10 * time.Millisecond,
			SuspicionTicks: 4,
			IndirectProbes: 2,
			Logger:         logger,
		}
	}
	for _, id := range ids {
		engines[id] = membership.NewSwim(id, msg, ids, cfg())
	}
	for _, e := range engines {
		e.Start()
	}
	defer func() {
		for _, e := range engines {
			e.Stop()
		}
	}()

	fmt.Printf("started %d membership nodes\n", len(ids))

	if !waitUntil(3*time.Second, func() bool { return allAgree(engines, ids, membership.Alive, nil) }) {
		return fmt.Errorf("cluster did not converge to all-alive: %s", viewOf(engines, ids))
	}
	fmt.Println("cluster converged: every node sees every node alive")

	// Kill one node: stop its engine and make it unreachable.
	victim := "node-e"
	engines[victim].Stop()
	msg.Deregister(victim)
	delete(engines, victim)
	fmt.Printf("killed %s\n", victim)

	if !waitUntil(5*time.Second, func() bool {
		return allAgree(engines, ids, membership.Dead, map[string]bool{victim: true}) || allSeeTarget(engines, victim, membership.Dead)
	}) {
		return fmt.Errorf("survivors did not converge on %s dead: %s", victim, viewOf(engines, ids))
	}
	fmt.Printf("failure detected: every survivor gossiped %s to dead\n", victim)

	// Restart the node fresh; it must be told it is presumed dead and refute to rejoin.
	engines[victim] = membership.NewSwim(victim, msg, ids, cfg())
	engines[victim].Start()
	fmt.Printf("restarted %s\n", victim)

	if !waitUntil(5*time.Second, func() bool { return allSeeTarget(engines, victim, membership.Alive) }) {
		return fmt.Errorf("restarted %s was not revived everywhere: %s", victim, viewOf(engines, ids))
	}
	fmt.Printf("recovery detected: %s refuted and is alive everywhere again\n", victim)

	return nil
}

func waitUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// allSeeTarget reports whether every running engine sees target in the wanted state.
func allSeeTarget(engines map[string]*membership.Swim, target string, want membership.State) bool {
	for _, e := range engines {
		if st, ok := e.List().StateOf(target); !ok || st != want {
			return false
		}
	}
	return true
}

// allAgree reports whether every running engine (minus excluded) sees each id in want.
func allAgree(engines map[string]*membership.Swim, ids []string, want membership.State, exclude map[string]bool) bool {
	for _, target := range ids {
		if exclude[target] {
			continue
		}
		for id, e := range engines {
			if exclude[id] {
				continue
			}
			if st, ok := e.List().StateOf(target); !ok || st != want {
				return false
			}
		}
	}
	return true
}

func viewOf(engines map[string]*membership.Swim, ids []string) string {
	names := make([]string, 0, len(engines))
	for id := range engines {
		names = append(names, id)
	}
	sort.Strings(names)
	out := ""
	for _, id := range names {
		out += id + "["
		for _, m := range engines[id].List().Members() {
			out += m.ID + ":" + m.State.String() + " "
		}
		out += "] "
	}
	return out
}
