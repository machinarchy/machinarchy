package domain

import (
	"context"
	"time"
)

// JobStatus represents the current state of a job in the system.
type JobStatus string

// Const defines the possible statuses for a job.
const (
	JobStatusPending   JobStatus = "PENDING"
	JobStatusRunning   JobStatus = "RUNNING"
	JobStatusCompleted JobStatus = "COMPLETED"
	JobStatusFailed    JobStatus = "FAILED"
)

// Job represents an asynchronous task executed on a managed host, including its metadata and execution results.
type Job struct {
	ID          int       `json:"id"`
	WorkspaceID int       `json:"-"`
	HostID      int       `json:"hostId"`
	Command     string    `json:"command"`
	Status      JobStatus `json:"status"`
	ResultLog   string    `json:"resultLog,omitempty"`
	TriggeredBy string    `json:"triggeredBy"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	FinishedAt  time.Time `json:"finishedAt,omitempty"`
}

// JobFilter defines the criteria for querying jobs, including workspace, host, and pagination parameters.
type JobFilter struct {
	WorkspaceID int `json:"-"`
	HostID      int `json:"hostId"`
	Limit       int `json:"limit"`
	Offset      int `json:"offset"`
}

// JobStore defines the interface for managing jobs in the data store, including creation, updates, and retrieval.
type JobStore interface {
	CreateJob(ctx context.Context, job *Job) error
	UpdateJobResult(ctx context.Context, id int, status string, resultLog string) error
	GetJobByID(ctx context.Context, workspaceID, id int) (*Job, error)
	ListJobs(ctx context.Context, filter JobFilter) ([]*Job, int, error) // Returns slice, total count, and error
}
