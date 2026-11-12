package service

import (
	"time"

	"github.com/machinarchy/machinarchy/internal/domain"

	"github.com/machinarchy/machinarchy/pkg/netpolicy"
)

type serviceOptions struct {
	targetPolicy            netpolicy.Policy
	targetPolicyProvider    func() netpolicy.Policy
	executionPolicyProvider func() domain.ExecutionPolicy
	commandRunner           CommandRunner
	heartbeatInterval       time.Duration
	staleAfter              time.Duration
	maxActiveRuns           int
	defaultRunTimeout       int
}

func WithTargetPolicyProvider(provider func() netpolicy.Policy) Option {
	return func(o *serviceOptions) { o.targetPolicyProvider = provider }
}
func WithExecutionPolicyProvider(provider func() domain.ExecutionPolicy) Option {
	return func(o *serviceOptions) { o.executionPolicyProvider = provider }
}

// Option customizes service construction.
type Option func(*serviceOptions)

// WithTargetPolicy sets the destination policy applied to every SSH dial. The
// default is netpolicy.Default().
func WithTargetPolicy(policy netpolicy.Policy) Option {
	return func(o *serviceOptions) { o.targetPolicy = policy }
}

// WithCommandRunner replaces the SSH command runner used by runs (tests).
func WithCommandRunner(runner CommandRunner) Option {
	return func(o *serviceOptions) { o.commandRunner = runner }
}

// WithRunTiming sets how often an executing run renews its heartbeat and how
// long a heartbeat may be missing before the run is considered abandoned.
func WithRunTiming(heartbeatInterval, staleAfter time.Duration) Option {
	return func(o *serviceOptions) { o.heartbeatInterval, o.staleAfter = heartbeatInterval, staleAfter }
}

// WithMaxActiveRuns bounds runs executing at once in this process.
func WithMaxActiveRuns(limit int) Option {
	return func(o *serviceOptions) { o.maxActiveRuns = limit }
}

func WithDefaultRunTimeout(seconds int) Option {
	return func(o *serviceOptions) { o.defaultRunTimeout = seconds }
}

func applyOptions(opts []Option) serviceOptions {
	options := serviceOptions{
		targetPolicy:      netpolicy.Default(),
		heartbeatInterval: defaultHeartbeatInterval,
		staleAfter:        defaultStaleAfter,
		maxActiveRuns:     defaultMaxActiveRuns,
		defaultRunTimeout: domain.DefaultRunTimeout,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}
