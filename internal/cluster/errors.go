// Package cluster partitions the keyspace across nodes with a consistent-hash ring and
// replicates each key to N nodes with tunable read and write quorums. In this phase the
// nodes run in-process behind a transport interface; the same Replica and Transport seams
// are what a later phase implements over the network, so no caller changes when nodes move
// onto separate processes.
package cluster

import "errors"

var (
	// ErrNoNodes means the ring is empty, so no key can be routed.
	ErrNoNodes = errors.New("cluster: ring has no nodes")
	// ErrNodeUnavailable means the transport cannot reach a node named by the ring.
	ErrNodeUnavailable = errors.New("cluster: node is unavailable")
	// ErrWriteQuorum means fewer than W replicas acknowledged a write.
	ErrWriteQuorum = errors.New("cluster: write quorum not met")
	// ErrReadQuorum means fewer than R replicas answered a read.
	ErrReadQuorum = errors.New("cluster: read quorum not met")
)
