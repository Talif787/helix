package membership

import (
	"context"
	"errors"
	"sync"
)

// ErrUnreachable is returned by the transport when a node cannot be contacted, which the
// protocol treats as a failed probe (the in-process transport returns it for a node that
// has been deregistered, modeling a crash).
var ErrUnreachable = errors.New("membership: node unreachable")

// Handler is the receiving side of the protocol, implemented by the Swim engine and
// registered with the messenger so peers can reach it.
type Handler interface {
	// HandlePing applies the caller's gossip and returns this node's gossip as the ack.
	HandlePing(from string, gossip []Update) []Update
	// HandlePingReq probes target on the caller's behalf and relays the result, returning
	// gossip on success or an error if target could not be reached.
	HandlePingReq(ctx context.Context, from, target string, gossip []Update) ([]Update, error)
}

// Messenger sends SWIM messages between nodes. The in-process implementation dispatches
// to registered handlers; a network implementation would send over the wire with the same
// signatures, so the engine above it is unchanged.
type Messenger interface {
	// Ping directly probes to, carrying gossip, and returns the ack's gossip or an error.
	Ping(ctx context.Context, from, to string, gossip []Update) ([]Update, error)
	// PingReq asks via to probe target on from's behalf.
	PingReq(ctx context.Context, from, via, target string, gossip []Update) ([]Update, error)
}

// InProcessMessenger routes SWIM messages to handlers running in the same process. It is
// safe for concurrent use, and Deregister models a node crash.
type InProcessMessenger struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

// NewInProcessMessenger returns an empty messenger.
func NewInProcessMessenger() *InProcessMessenger {
	return &InProcessMessenger{handlers: make(map[string]Handler)}
}

// Register makes nodeID reachable via h.
func (m *InProcessMessenger) Register(nodeID string, h Handler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[nodeID] = h
}

// Deregister removes nodeID, modeling a crash: probes to it will fail.
func (m *InProcessMessenger) Deregister(nodeID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.handlers, nodeID)
}

func (m *InProcessMessenger) handler(nodeID string) (Handler, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	h, ok := m.handlers[nodeID]
	return h, ok
}

// Ping dispatches to the target's HandlePing, or fails if it is not registered.
func (m *InProcessMessenger) Ping(ctx context.Context, from, to string, gossip []Update) ([]Update, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h, ok := m.handler(to)
	if !ok {
		return nil, ErrUnreachable
	}
	return h.HandlePing(from, gossip), nil
}

// PingReq dispatches to the relay's HandlePingReq, or fails if the relay is not registered.
func (m *InProcessMessenger) PingReq(ctx context.Context, from, via, target string, gossip []Update) ([]Update, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h, ok := m.handler(via)
	if !ok {
		return nil, ErrUnreachable
	}
	return h.HandlePingReq(ctx, from, target, gossip)
}
