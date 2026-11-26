package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/agentgateway/bus/bustest"
	"github.com/machinarchy/machinarchy/internal/agentgateway/bus/memory"
)

func TestContractViaBusNext(t *testing.T) {
	bustest.Run(t, func(*testing.T) *bustest.Harness {
		b := memory.New()
		b.AckWait = 200 * time.Millisecond
		return &bustest.Harness{Bus: b, RedeliversOnTimeout: true,
			SendResult: func(agentID string, payload []byte) { b.DeliverResult(context.Background(), agentID, payload) }}
	})
}

func TestContract(t *testing.T) {
	bustest.Run(t, func(*testing.T) *bustest.Harness {
		b := memory.New()
		b.AckWait = 200 * time.Millisecond
		return &bustest.Harness{
			Bus: b, RedeliversOnTimeout: true,
			Next: func(agentID string, wait time.Duration) (bustest.Delivery, bool) {
				d, ok, _ := b.Next(context.Background(), agentID, wait)
				return bustest.Delivery{Payload: d.Payload, Ack: d.Ack, Nack: d.Nack}, ok
			},
			SendResult: func(agentID string, payload []byte) { b.DeliverResult(context.Background(), agentID, payload) },
		}
	})
}
