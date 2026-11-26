package nats_test

import (
	"context"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/machinarchy/machinarchy-agent/protocol/agentv1"
	"github.com/machinarchy/machinarchy/internal/agentgateway/bus/bustest"
	natsbus "github.com/machinarchy/machinarchy/internal/agentgateway/bus/nats"
)

func TestContract(t *testing.T) { runContract(t, true) }

// TestContractViaBusNext consumes through Bus.Next, as the gateway does.
func TestContractViaBusNext(t *testing.T) { runContract(t, false) }

func runContract(t *testing.T, independentConsumer bool) {
	bustest.Run(t, func(t *testing.T) *bustest.Harness {
		server, err := natsserver.NewServer(&natsserver.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
		if err != nil {
			t.Fatal(err)
		}
		go server.Start()
		if !server.ReadyForConnections(10 * time.Second) {
			t.Fatal("NATS did not start")
		}
		t.Cleanup(server.Shutdown)
		b, err := natsbus.New(natsbus.Config{URL: server.ClientURL()})
		if err != nil {
			t.Fatal(err)
		}
		nc, err := natsgo.Connect(server.ClientURL())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(nc.Close)
		js, err := jetstream.New(nc)
		if err != nil {
			t.Fatal(err)
		}
		h := &bustest.Harness{
			Bus: b,
			Next: func(agentID string, wait time.Duration) (bustest.Delivery, bool) {
				name, err := agentv1.CommandConsumer(agentID)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				consumer, err := js.Consumer(ctx, agentv1.CommandStream, name)
				if err != nil {
					t.Fatalf("consumer %s: %v", agentID, err)
				}
				batch, err := consumer.Fetch(1, jetstream.FetchMaxWait(wait))
				if err != nil {
					t.Fatal(err)
				}
				for msg := range batch.Messages() {
					return bustest.Delivery{Payload: msg.Data(), Ack: func() { _ = msg.Ack() }, Nack: func() { _ = msg.Nak() }}, true
				}
				return bustest.Delivery{}, false
			},
			SendResult: func(agentID string, payload []byte) {
				subject, err := agentv1.ResultSubject(agentID)
				if err != nil {
					t.Fatal(err)
				}
				if err := nc.Publish(subject, payload); err != nil {
					t.Fatal(err)
				}
				_ = nc.Flush()
			},
		}
		if !independentConsumer {
			h.Next = nil
		}
		return h
	})
}
