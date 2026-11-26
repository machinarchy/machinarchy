// Package bus is the controller-internal message bus behind the agent
// gateway (ADR-059). Agents never see it: they talk to the gateway only.
//
// A Bus carries commands from the controller to one agent each and delivers
// agent results to the controller. Adapters (NATS today) must keep these
// guarantees, which bustest checks:
//
//   - at-least-once delivery of a command until the consumer acknowledges it;
//   - per-agent ordering;
//   - redelivery after a negative acknowledgement or an ack timeout;
//   - isolation: an agent's queue never yields another agent's commands;
//   - idempotent publish: the same message ID within the duplicate window is
//     stored once;
//   - a durable per-agent queue that holds commands while the agent is away.
package bus

import (
	"context"
	"errors"
	"time"
)

// Handlers is what the gateway installs when it starts serving.
//
// Register and Renew carry the agent-facing control requests of an adapter
// whose agents still reach the controller through the bus itself (the NATS
// path). A nil reply means "send no answer". Adapters that do not carry
// control traffic never call them.
type Handlers struct {
	Register func(ctx context.Context, payload []byte) (reply []byte)
	Renew    func(ctx context.Context, payload []byte) (reply []byte)
	// Result receives one agent result. agentID is the identity the transport
	// authenticated (for NATS, the subject); the handler must reject a result
	// whose signed agent ID differs.
	Result func(ctx context.Context, agentID string, payload []byte)
}

// Delivery is one command handed to a consumer. Ack removes it from the
// queue; Nack (or neither, until the ack timeout) redelivers it.
type Delivery struct {
	Payload []byte
	Ack     func()
	Nack    func()
}

// Bus is implemented by each adapter.
type Bus interface {
	// Serve connects, provisions storage, starts delivering to h, and blocks
	// until ctx ends; it then shuts down cleanly. Ready is true while serving.
	Serve(ctx context.Context, h Handlers) error
	// EnsureAgent creates the agent's durable command queue. It is idempotent.
	EnsureAgent(ctx context.Context, agentID string) error
	// Publish stores payload in the agent's queue and returns once it is
	// durable. msgID makes the call idempotent.
	Publish(ctx context.Context, agentID, msgID string, payload []byte) error
	// Next returns the agent's next command, waiting up to wait; false means
	// none arrived. The gateway streams it to the authenticated agent.
	Next(ctx context.Context, agentID string, wait time.Duration) (Delivery, bool, error)
	// Ready reports whether the bus can accept commands.
	Ready() bool
}

// ErrUnavailable reports that the bus is not connected or not running.
var ErrUnavailable = errors.New("agent gateway bus is not available")
