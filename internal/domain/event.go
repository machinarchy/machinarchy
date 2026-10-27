package domain

import (
	"time"
)

// Event defines the core contract for all domain events.
type Event interface {
	EventName() string
	OccurredAt() time.Time
}

// HostReconnectedEvent is triggered when a managed host re-establishes connectivity.
type HostReconnectedEvent struct {
	HostID    int
	Hostname  string
	IP        string
	Timestamp time.Time
}

func (e HostReconnectedEvent) EventName() string { return "host.reconnected" }
func (e HostReconnectedEvent) OccurredAt() time.Time {
	if e.Timestamp.IsZero() {
		return time.Now()
	}
	return e.Timestamp
}

// HostCreatedEvent is triggered when a new host target is registered.
type HostCreatedEvent struct {
	HostID        int
	Hostname      string
	EnvironmentID int
	Timestamp     time.Time
}

func (e HostCreatedEvent) EventName() string { return "host.created" }
func (e HostCreatedEvent) OccurredAt() time.Time {
	if e.Timestamp.IsZero() {
		return time.Now()
	}
	return e.Timestamp
}

// JobCompletedEvent is triggered when an asynchronous host job finishes.
type JobCompletedEvent struct {
	JobID     int
	HostID    int
	Status    string
	Timestamp time.Time
}

func (e JobCompletedEvent) EventName() string { return "job.completed" }
func (e JobCompletedEvent) OccurredAt() time.Time {
	if e.Timestamp.IsZero() {
		return time.Now()
	}
	return e.Timestamp
}
