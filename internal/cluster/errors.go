// Package cluster partitions the keyspace across nodes with a consistent-hash ring and
// routes each operation to the node that owns its key. In this phase the nodes run
// in-process behind a transport interface; the same Store and Transport seams are what a
// later phase implements over the network, so no caller changes when nodes move onto
// separate processes.
package cluster

import "errors"

var (
	// ErrNoNodes means the ring is empty, so no key can be routed.
	ErrNoNodes = errors.New("cluster: ring has no nodes")
	// ErrNodeUnavailable means the ring named an owner that the transport cannot reach.
	ErrNodeUnavailable = errors.New("cluster: owning node is unavailable")
)
