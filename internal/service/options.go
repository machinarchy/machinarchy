package service

import (
	"time"

	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

type serviceOptions struct {
	targetPolicy      netpolicy.Policy
	commandRunner     CommandRunner
	heartbeatInterval time.Duration
	staleAfter        time.Duration
	maxActiveRuns     int
}

// ServiceOption customizes service construction.
type ServiceOption func(*serviceOptions)

// WithTargetPolicy sets the destination policy applied to every SSH dial. The
// default is netpolicy.Default().
func WithTargetPolicy(policy netpolicy.Policy) ServiceOption {
	return func(o *serviceOptions) { o.targetPolicy = policy }
}

// WithCommandRunner replaces the SSH command runner used by runs (tests).
func WithCommandRunner(runner CommandRunner) ServiceOption {
	return func(o *serviceOptions) { o.commandRunner = runner }
}

// WithRunTiming sets how often an executing run renews its heartbeat and how
// long a heartbeat may be missing before the run is considered abandoned.
func WithRunTiming(heartbeatInterval, staleAfter time.Duration) ServiceOption {
	return func(o *serviceOptions) { o.heartbeatInterval, o.staleAfter = heartbeatInterval, staleAfter }
}

// WithMaxActiveRuns bounds runs executing at once in this process.
func WithMaxActiveRuns(limit int) ServiceOption {
	return func(o *serviceOptions) { o.maxActiveRuns = limit }
}

func applyOptions(opts []ServiceOption) serviceOptions {
	options := serviceOptions{
		targetPolicy:      netpolicy.Default(),
		heartbeatInterval: defaultHeartbeatInterval,
		staleAfter:        defaultStaleAfter,
		maxActiveRuns:     defaultMaxActiveRuns,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}
