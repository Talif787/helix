package cluster

// Metrics receives one observation per coordinator request. The implementation lives outside
// this package (the daemon wires one backed by the metrics registry), so the cluster stays
// decoupled from any metrics backend. op is "get", "put", or "delete"; result is "ok",
// "not_found", or "error"; seconds is the wall-clock duration of the request.
type Metrics interface {
	ObserveRequest(op, result string, seconds float64)
}

// nopMetrics is the default, used when no metrics sink is set, so the coordinator never has to
// nil-check.
type nopMetrics struct{}

func (nopMetrics) ObserveRequest(string, string, float64) {}
