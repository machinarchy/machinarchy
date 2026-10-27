package domain

import (
	"context"
	"time"
)

type JobStatus string

const (
	JobStatusPending   JobStatus = "PENDING"
	JobStatusRunning   JobStatus = "RUNNING"
	JobStatusCompleted JobStatus = "COMPLETED"
	JobStatusFailed    JobStatus = "FAILED"
)

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

type JobFilter struct {
	WorkspaceID int `json:"-"`
	HostID      int `json:"hostId"`
	Limit       int `json:"limit"`
	Offset      int `json:"offset"`
}

type JobStore interface {
	CreateJob(ctx context.Context, job *Job) error
	UpdateJobResult(ctx context.Context, id int, status string, resultLog string) error
	GetJobByID(ctx context.Context, workspaceID, id int) (*Job, error)
	ListJobs(ctx context.Context, filter JobFilter) ([]*Job, int, error) // Returns slice, total count, and error
}
