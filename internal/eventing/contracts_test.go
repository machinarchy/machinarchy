package eventing

import (
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

func TestEventEnvelopeValidationAndSchemaVersion(t *testing.T) {
	id, err := resource.NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	event, err := NewHostEvent(HostSpecChanged, 2, Subject{Kind: "host", ID: id, Generation: 3, Revision: 5}, "spec-changed", time.Now(), "request-1", "command-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := event.Validate(); err != nil {
		t.Fatal(err)
	}
	event.SchemaVersion = 2
	if err := event.Validate(); err == nil {
		t.Fatal("unsupported event version was accepted")
	}
}
