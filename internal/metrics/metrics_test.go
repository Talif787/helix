package metrics

import (
	"strings"
	"sync"
	"testing"
)

func render(r *Registry) string {
	var b strings.Builder
	r.WriteExposition(&b)
	return b.String()
}

func TestCounterExposition(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("helix_requests_total", "total requests", "op", "result")
	c.With("get", "ok").Inc()
	c.With("get", "ok").Inc()
	c.With("put", "error").Add(3)

	out := render(r)
	if !strings.Contains(out, "# TYPE helix_requests_total counter") {
		t.Fatalf("missing TYPE line:\n%s", out)
	}
	if !strings.Contains(out, `helix_requests_total{op="get",result="ok"} 2`) {
		t.Fatalf("missing or wrong get/ok sample:\n%s", out)
	}
	if !strings.Contains(out, `helix_requests_total{op="put",result="error"} 3`) {
		t.Fatalf("missing or wrong put/error sample:\n%s", out)
	}
}

func TestGaugeExposition(t *testing.T) {
	r := NewRegistry()
	g := r.NewGauge("helix_uptime_seconds", "uptime")
	g.With().Set(42)
	mv := r.NewGauge("helix_cluster_members", "members by state", "state")
	mv.With("alive").Set(3)
	mv.With("dead").Set(1)

	out := render(r)
	if !strings.Contains(out, "# TYPE helix_uptime_seconds gauge") {
		t.Fatalf("missing gauge TYPE:\n%s", out)
	}
	if !strings.Contains(out, "helix_uptime_seconds 42") {
		t.Fatalf("missing label-less gauge sample:\n%s", out)
	}
	if !strings.Contains(out, `helix_cluster_members{state="alive"} 3`) ||
		!strings.Contains(out, `helix_cluster_members{state="dead"} 1`) {
		t.Fatalf("missing labelled gauge samples:\n%s", out)
	}
}

func TestDeterministicOrdering(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("m", "h", "k")
	c.With("z").Inc()
	c.With("a").Inc()
	c.With("m").Inc()
	out := render(r)
	ia := strings.Index(out, `k="a"`)
	im := strings.Index(out, `k="m"`)
	iz := strings.Index(out, `k="z"`)
	if !(ia < im && im < iz) {
		t.Fatalf("series should be sorted by label value:\n%s", out)
	}
}

func TestLabelValueEscaping(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("m", "h", "path")
	c.With(`a"b\c` + "\n" + "d").Inc()
	out := render(r)
	if !strings.Contains(out, `path="a\"b\\c\nd"`) {
		t.Fatalf("label value not escaped correctly:\n%s", out)
	}
}

func TestConcurrentInc(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("hits", "h")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.With().Inc()
			}
		}()
	}
	wg.Wait()
	out := render(r)
	if !strings.Contains(out, "hits 10000") {
		t.Fatalf("concurrent increments lost updates:\n%s", out)
	}
}

func TestDuplicateRegistrationPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic on duplicate metric name")
		}
	}()
	r := NewRegistry()
	r.NewCounter("dup", "h")
	r.NewGauge("dup", "h")
}
