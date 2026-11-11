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

func TestHostSpecCommandRequiresOptimisticPreconditions(t *testing.T) {
	id, err := resource.NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	command := HostSpecUpdateCommand{Command: CommandMetadata{ID: "command-1"}, WorkspaceID: 1, HostID: id, ExpectedRevision: 1, ExpectedGeneration: 2,
		Spec: resource.HostSpec{Presence: resource.PresenceManaged}}
	if err := command.Validate(); err != nil {
		t.Fatal(err)
	}
	command.ExpectedGeneration = 0
	if err := command.Validate(); err == nil {
		t.Fatal("command without generation precondition was accepted")
	}
}
