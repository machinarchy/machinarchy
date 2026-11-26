package rabbitmq_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/machinarchy/machinarchy/internal/agentgateway/bus"
	"github.com/machinarchy/machinarchy/internal/agentgateway/bus/bustest"
	"github.com/machinarchy/machinarchy/internal/agentgateway/bus/rabbitmq"
)

// The contract needs a broker. Start one and point the test at it:
//
//	docker run --rm -p 5672:5672 rabbitmq:4
//	MACHINARCHY_TEST_AMQP_URL=amqp://guest:guest@127.0.0.1:5672/ go test ./internal/agentgateway/bus/rabbitmq/
const urlEnv = "MACHINARCHY_TEST_AMQP_URL"

func TestContract(t *testing.T)           { runContract(t, true) }
func TestContractViaBusNext(t *testing.T) { runContract(t, false) }

func runContract(t *testing.T, independentConsumer bool) {
	url := os.Getenv(urlEnv)
	if url == "" {
		t.Skipf("%s is not set; no RabbitMQ to test against", urlEnv)
	}
	bustest.Run(t, func(t *testing.T) *bustest.Harness {
		conn, err := amqp.Dial(url)
		if err != nil {
			t.Fatalf("connect to RabbitMQ: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		ch, err := conn.Channel()
		if err != nil {
			t.Fatal(err)
		}
		reset := func() {
			for _, id := range []string{"agent-a", "agent-b"} {
				name, _ := rabbitmq.CommandQueue(id)
				_, _ = ch.QueueDelete(name, false, false, false)
			}
			_, _ = ch.QueueDelete(rabbitmq.ResultQueue, false, false, false)
		}
		reset()
		t.Cleanup(reset)

		b, err := rabbitmq.New(rabbitmq.Config{URL: url, AckWait: 500 * time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		h := &bustest.Harness{
			Bus: b,
			SendResult: func(agentID string, payload []byte) {
				key, err := rabbitmq.ResultRoutingKey(agentID)
				if err != nil {
					t.Fatal(err)
				}
				if err := ch.PublishWithContext(context.Background(), rabbitmq.ResultExchange, key, false, false, amqp.Publishing{Body: payload}); err != nil {
					t.Fatal(err)
				}
			},
		}
		if independentConsumer {
			h.Next = func(agentID string, wait time.Duration) (bustest.Delivery, bool) {
				name, err := rabbitmq.CommandQueue(agentID)
				if err != nil {
					t.Fatal(err)
				}
				c, err := conn.Channel()
				if err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(wait)
				for {
					msg, ok, err := c.Get(name, false)
					if err != nil {
						t.Fatal(err)
					}
					if ok {
						settle := func(f func() error) func() { return func() { _ = f(); _ = c.Close() } }
						return bustest.Delivery{Payload: msg.Body, Ack: settle(func() error { return msg.Ack(false) }), Nack: settle(func() error { return msg.Nack(false, true) })}, true
					}
					if time.Now().After(deadline) {
						_ = c.Close()
						return bustest.Delivery{}, false
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
		} else {
			h.RedeliversOnTimeout = true
		}
		return h
	})
}

// TestErrors covers behaviour outside the shared contract.
func TestErrors(t *testing.T) {
	if _, err := rabbitmq.New(rabbitmq.Config{}); err == nil {
		t.Fatal("New without URL must fail")
	}
	b, err := rabbitmq.New(rabbitmq.Config{URL: "amqp://127.0.0.1:1/"})
	if err != nil {
		t.Fatal(err)
	}
	if b.Ready() {
		t.Fatal("ready before Serve")
	}
	if err := b.Publish(context.Background(), "agent-a", "m", []byte("x")); !errors.Is(err, bus.ErrUnavailable) {
		t.Fatalf("Publish before Serve = %v, want ErrUnavailable", err)
	}
	if _, _, err := b.Next(context.Background(), "agent-a", time.Millisecond); !errors.Is(err, bus.ErrUnavailable) {
		t.Fatalf("Next before Serve = %v, want ErrUnavailable", err)
	}
	if err := b.Publish(context.Background(), "../bad", "m", nil); err == nil {
		t.Fatal("an invalid agent ID must be refused")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.Serve(ctx, bus.Handlers{}); err == nil {
		t.Fatal("Serve with an unreachable broker must fail")
	}
}
