// Package bustest is the contract every bus adapter must pass (ADR-059).
package bustest

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/agentgateway/bus"
)

// Delivery is one command seen by an agent-side consumer.
type Delivery struct {
	Payload []byte
	Ack     func()
	Nack    func()
}

// Harness adapts one adapter to the suite. The suite owns Serve.
type Harness struct {
	Bus bus.Bus
	// Next pulls the agent's next command, or reports false after wait. When
	// nil the suite uses Bus.Next, which the gateway streams to agents.
	Next func(agentID string, wait time.Duration) (Delivery, bool)
	// SendResult makes the transport deliver payload as sent by agentID.
	SendResult func(agentID string, payload []byte)
	// RedeliversOnTimeout is true when the adapter can be made to redeliver an
	// unacknowledged command within a test.
	RedeliversOnTimeout bool
}

type results struct {
	mu   sync.Mutex
	list []string
}

// Run executes the contract against a fresh harness from newHarness.
func Run(t *testing.T, newHarness func(t *testing.T) *Harness) {
	t.Helper()
	start := func(t *testing.T) (*Harness, *results) {
		t.Helper()
		h := newHarness(t)
		if h.Next == nil { // consume through the adapter's own Bus.Next
			h.Next = func(agentID string, wait time.Duration) (Delivery, bool) {
				d, ok, err := h.Bus.Next(context.Background(), agentID, wait)
				if err != nil {
					t.Fatalf("Bus.Next(%s): %v", agentID, err)
				}
				return Delivery{Payload: d.Payload, Ack: d.Ack, Nack: d.Nack}, ok
			}
		}
		got := &results{}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			done <- h.Bus.Serve(ctx, bus.Handlers{Result: func(_ context.Context, agentID string, payload []byte) {
				got.mu.Lock()
				got.list = append(got.list, agentID+":"+string(payload))
				got.mu.Unlock()
			}})
		}()
		t.Cleanup(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("Serve returned %v", err)
			}
		})
		deadline := time.Now().Add(10 * time.Second)
		for !h.Bus.Ready() {
			if time.Now().After(deadline) {
				t.Fatal("bus not ready")
			}
			time.Sleep(10 * time.Millisecond)
		}
		return h, got
	}
	ctx := context.Background()
	publish := func(t *testing.T, h *Harness, agent, id, body string) {
		t.Helper()
		if err := h.Bus.Publish(ctx, agent, id, []byte(body)); err != nil {
			t.Fatalf("publish %s: %v", id, err)
		}
	}
	expect := func(t *testing.T, h *Harness, agent, want string) Delivery {
		t.Helper()
		d, ok := h.Next(agent, 5*time.Second)
		if !ok {
			t.Fatalf("agent %s: expected %q, got nothing", agent, want)
		}
		if !bytes.Equal(d.Payload, []byte(want)) {
			t.Fatalf("agent %s: got %q, want %q", agent, d.Payload, want)
		}
		return d
	}
	none := func(t *testing.T, h *Harness, agent string) {
		t.Helper()
		if d, ok := h.Next(agent, 300*time.Millisecond); ok {
			t.Fatalf("agent %s: unexpected delivery %q", agent, d.Payload)
		}
	}

	t.Run("ready after serve", func(t *testing.T) {
		h, _ := start(t)
		if !h.Bus.Ready() {
			t.Fatal("not ready")
		}
	})
	t.Run("queue holds commands while the agent is away and keeps order", func(t *testing.T) {
		h, _ := start(t)
		if err := h.Bus.EnsureAgent(ctx, "agent-a"); err != nil {
			t.Fatal(err)
		}
		publish(t, h, "agent-a", "c1", "one")
		publish(t, h, "agent-a", "c2", "two")
		publish(t, h, "agent-a", "c3", "three")
		for _, want := range []string{"one", "two", "three"} {
			expect(t, h, "agent-a", want).Ack()
		}
		none(t, h, "agent-a")
	})
	t.Run("an agent never receives another agent's commands", func(t *testing.T) {
		h, _ := start(t)
		for _, id := range []string{"agent-a", "agent-b"} {
			if err := h.Bus.EnsureAgent(ctx, id); err != nil {
				t.Fatal(err)
			}
		}
		publish(t, h, "agent-a", "c1", "for-a")
		none(t, h, "agent-b")
		expect(t, h, "agent-a", "for-a").Ack()
	})
	t.Run("publishing the same message ID twice stores it once", func(t *testing.T) {
		h, _ := start(t)
		if err := h.Bus.EnsureAgent(ctx, "agent-a"); err != nil {
			t.Fatal(err)
		}
		publish(t, h, "agent-a", "same", "once")
		publish(t, h, "agent-a", "same", "once")
		expect(t, h, "agent-a", "once").Ack()
		none(t, h, "agent-a")
	})
	t.Run("a rejected command is redelivered, an acknowledged one is not", func(t *testing.T) {
		h, _ := start(t)
		if err := h.Bus.EnsureAgent(ctx, "agent-a"); err != nil {
			t.Fatal(err)
		}
		publish(t, h, "agent-a", "c1", "first")
		publish(t, h, "agent-a", "c2", "second")
		expect(t, h, "agent-a", "first").Nack()
		expect(t, h, "agent-a", "first").Ack() // order kept: still before "second"
		expect(t, h, "agent-a", "second").Ack()
		none(t, h, "agent-a")
	})
	t.Run("an unacknowledged command is redelivered after the ack wait", func(t *testing.T) {
		h, _ := start(t)
		if !h.RedeliversOnTimeout {
			t.Skip("adapter ack wait is fixed at 30s")
		}
		if err := h.Bus.EnsureAgent(ctx, "agent-a"); err != nil {
			t.Fatal(err)
		}
		publish(t, h, "agent-a", "c1", "lost")
		if _, ok := h.Next("agent-a", 5*time.Second); !ok {
			t.Fatal("no first delivery")
		}
		expect(t, h, "agent-a", "lost").Ack()
	})
	t.Run("results reach the handler with the authenticated agent", func(t *testing.T) {
		h, got := start(t)
		h.SendResult("agent-a", []byte("done"))
		deadline := time.Now().Add(5 * time.Second)
		for {
			got.mu.Lock()
			list := append([]string(nil), got.list...)
			got.mu.Unlock()
			if len(list) == 1 && list[0] == "agent-a:done" {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("results = %v", list)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}
