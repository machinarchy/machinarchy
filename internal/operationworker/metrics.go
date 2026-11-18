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
	telemetry.MustRegister(registerer, m.inProgress, m.targets, m.duration, m.panics)
	return m
}
