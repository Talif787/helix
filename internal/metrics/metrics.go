// Package metrics is a small, dependency-free metrics library that renders the Prometheus
// text exposition format. It supports labelled counters and gauges, which cover a node's
// needs (request tallies, quorum outcomes, membership and storage gauges) while keeping the
// module standard-library only. A later phase can adopt a full client if native histograms
// or exemplars are needed; the exposition here is standard, so Prometheus scrapes it as is.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Registry holds metric families and renders them, preserving registration order for the
// family blocks and sorting series within a family for deterministic output.
type Registry struct {
	mu       sync.Mutex
	families []collector
	names    map[string]bool
}

type collector interface{ writeTo(w io.Writer) }

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{names: make(map[string]bool)} }

func (r *Registry) register(name string, c collector) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.names[name] {
		panic("metrics: duplicate registration of " + name)
	}
	r.names[name] = true
	r.families = append(r.families, c)
}

// NewCounter registers and returns a counter family with the given label names (zero labels
// is allowed for a single-series metric).
func (r *Registry) NewCounter(name, help string, labelNames ...string) *CounterVec {
	cv := &CounterVec{name: name, help: help, labelNames: labelNames, series: make(map[string]*Counter)}
	r.register(name, cv)
	return cv
}

// NewGauge registers and returns a gauge family.
func (r *Registry) NewGauge(name, help string, labelNames ...string) *GaugeVec {
	gv := &GaugeVec{name: name, help: help, labelNames: labelNames, series: make(map[string]*Gauge)}
	r.register(name, gv)
	return gv
}

// WriteTo renders the full exposition to w.
func (r *Registry) WriteExposition(w io.Writer) {
	r.mu.Lock()
	fams := make([]collector, len(r.families))
	copy(fams, r.families)
	r.mu.Unlock()
	for _, f := range fams {
		f.writeTo(w)
	}
}

// Handler serves the exposition over HTTP in the Prometheus text format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.WriteExposition(w)
	})
}

// CounterVec is a family of monotonically increasing counters sharing a name and label set.
type CounterVec struct {
	name, help string
	labelNames []string
	mu         sync.Mutex
	series     map[string]*Counter
}

// Counter is a single counter series.
type Counter struct {
	labels []string
	v      uint64
}

// With returns the counter for the given label values, creating it on first use. It panics
// if the number of values does not match the family's label names.
func (cv *CounterVec) With(values ...string) *Counter {
	if len(values) != len(cv.labelNames) {
		panic(fmt.Sprintf("metrics: %s expects %d label values, got %d", cv.name, len(cv.labelNames), len(values)))
	}
	key := strings.Join(values, "\x00")
	cv.mu.Lock()
	defer cv.mu.Unlock()
	c := cv.series[key]
	if c == nil {
		c = &Counter{labels: append([]string(nil), values...)}
		cv.series[key] = c
	}
	return c
}

// Inc adds one. Add adds n. Both are safe for concurrent use.
func (c *Counter) Inc()          { atomic.AddUint64(&c.v, 1) }
func (c *Counter) Add(n uint64)  { atomic.AddUint64(&c.v, n) }
func (c *Counter) value() uint64 { return atomic.LoadUint64(&c.v) }

func (cv *CounterVec) writeTo(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n", cv.name, escapeHelp(cv.help))
	fmt.Fprintf(w, "# TYPE %s counter\n", cv.name)
	cv.mu.Lock()
	keys := make([]string, 0, len(cv.series))
	for k := range cv.series {
		keys = append(keys, k)
	}
	series := cv.series
	cv.mu.Unlock()
	sort.Strings(keys)
	for _, k := range keys {
		c := series[k]
		writeSample(w, cv.name, cv.labelNames, c.labels, strconv.FormatUint(c.value(), 10))
	}
}

// GaugeVec is a family of gauges that can go up or down.
type GaugeVec struct {
	name, help string
	labelNames []string
	mu         sync.Mutex
	series     map[string]*Gauge
}

// Gauge is a single gauge series.
type Gauge struct {
	labels []string
	v      int64
}

// With returns the gauge for the given label values, creating it on first use.
func (gv *GaugeVec) With(values ...string) *Gauge {
	if len(values) != len(gv.labelNames) {
		panic(fmt.Sprintf("metrics: %s expects %d label values, got %d", gv.name, len(gv.labelNames), len(values)))
	}
	key := strings.Join(values, "\x00")
	gv.mu.Lock()
	defer gv.mu.Unlock()
	g := gv.series[key]
	if g == nil {
		g = &Gauge{labels: append([]string(nil), values...)}
		gv.series[key] = g
	}
	return g
}

// Set, Add, Inc, and Dec update the gauge; all are safe for concurrent use.
func (g *Gauge) Set(n int64)  { atomic.StoreInt64(&g.v, n) }
func (g *Gauge) Add(n int64)  { atomic.AddInt64(&g.v, n) }
func (g *Gauge) Inc()         { atomic.AddInt64(&g.v, 1) }
func (g *Gauge) Dec()         { atomic.AddInt64(&g.v, -1) }
func (g *Gauge) value() int64 { return atomic.LoadInt64(&g.v) }

func (gv *GaugeVec) writeTo(w io.Writer) {
	fmt.Fprintf(w, "# HELP %s %s\n", gv.name, escapeHelp(gv.help))
	fmt.Fprintf(w, "# TYPE %s gauge\n", gv.name)
	gv.mu.Lock()
	keys := make([]string, 0, len(gv.series))
	for k := range gv.series {
		keys = append(keys, k)
	}
	series := gv.series
	gv.mu.Unlock()
	sort.Strings(keys)
	for _, k := range keys {
		g := series[k]
		writeSample(w, gv.name, gv.labelNames, g.labels, strconv.FormatInt(g.value(), 10))
	}
}

func writeSample(w io.Writer, name string, labelNames, labelValues []string, valueStr string) {
	if len(labelNames) == 0 {
		fmt.Fprintf(w, "%s %s\n", name, valueStr)
		return
	}
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('{')
	for i, ln := range labelNames {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(ln)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(labelValues[i]))
		b.WriteByte('"')
	}
	b.WriteString("} ")
	b.WriteString(valueStr)
	b.WriteByte('\n')
	io.WriteString(w, b.String())
}

// escapeLabelValue escapes backslash, double-quote, and newline per the exposition format.
func escapeLabelValue(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
	return r.Replace(s)
}

// escapeHelp escapes backslash and newline in HELP text.
func escapeHelp(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	return r.Replace(s)
}
