package operationworker

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/machinarchy/machinarchy/internal/telemetry"
)

type workerMetrics struct {
	inProgress prometheus.Gauge
	targets    *prometheus.CounterVec
	duration   prometheus.Histogram
	panics     prometheus.Counter
	// Detached execution (AUDIT/10b §1.3).
	launches       *prometheus.CounterVec
	runningTargets prometheus.Gauge
	polls          *prometheus.CounterVec
	lost           prometheus.Counter
	pollDuration   prometheus.Histogram
}

func newWorkerMetrics(registerer prometheus.Registerer) *workerMetrics {
	m := &workerMetrics{
		inProgress: prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "machinarchy", Subsystem: "operationworker",
			Name: "operations_in_progress", Help: "Operations being executed by this process."}),
		targets: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "machinarchy", Subsystem: "operationworker",
			Name: "targets_total", Help: "Operation targets finished, by final state."}, []string{"state"}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "machinarchy", Subsystem: "operationworker",
			Name: "target_duration_seconds", Help: "Time to run a command on one host.", Buckets: prometheus.ExponentialBuckets(0.05, 2.5, 10)}),
		panics: prometheus.NewCounter(prometheus.CounterOpts{Namespace: "machinarchy", Subsystem: "operationworker",
			Name: "panics_total", Help: "Operations stopped by a recovered panic."}),
	}
	m.launches = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "machinarchy", Subsystem: "operationworker",
		Name: "detached_launches_total", Help: "Detached launches: confirmed, unconfirmed (the reply was lost; the poller decides), failed."}, []string{"outcome"})
	m.runningTargets = prometheus.NewGauge(prometheus.GaugeOpts{Namespace: "machinarchy", Subsystem: "operationworker",
		Name: "running_targets", Help: "Detached targets running on hosts, as last listed by the poller."})
	m.polls = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "machinarchy", Subsystem: "operationworker",
		Name: "polls_total", Help: "Detached status polls, one per host session: ok or unreachable."}, []string{"outcome"})
	m.lost = prometheus.NewCounter(prometheus.CounterOpts{Namespace: "machinarchy", Subsystem: "operationworker",
		Name: "lost_targets_total", Help: "Detached targets that ended Lost (outcome unknown)."})
	m.pollDuration = prometheus.NewHistogram(prometheus.HistogramOpts{Namespace: "machinarchy", Subsystem: "operationworker",
		Name: "poll_duration_seconds", Help: "Time to poll one host's detached targets.", Buckets: prometheus.ExponentialBuckets(0.05, 2.5, 10)})
	telemetry.MustRegister(registerer, m.inProgress, m.targets, m.duration, m.panics, m.launches, m.runningTargets, m.polls, m.lost, m.pollDuration)
	return m
}
