package controlmanager

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/machinarchy/machinarchy/internal/telemetry"
)

type engineMetrics struct {
	reconciles *prometheus.CounterVec
	duration   *prometheus.HistogramVec
	deliveries *prometheus.CounterVec
}

func newEngineMetrics(registerer prometheus.Registerer, queueDepth func() float64) *engineMetrics {
	m := &engineMetrics{
		reconciles: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "machinarchy", Subsystem: "controlmanager",
			Name: "reconciles_total", Help: "Reconciliations by controller and outcome (success, retry, permanent_error)."}, []string{"controller", "outcome"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{Namespace: "machinarchy", Subsystem: "controlmanager",
			Name: "reconcile_duration_seconds", Help: "Reconciliation duration by controller.", Buckets: prometheus.ExponentialBuckets(0.005, 2.5, 10)},
			[]string{"controller"}),
		deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "machinarchy", Subsystem: "controlmanager",
			Name: "outbox_deliveries_total", Help: "Outbox deliveries handled by the dispatcher, by outcome."}, []string{"outcome"}),
	}
	telemetry.MustRegister(registerer, m.reconciles, m.duration, m.deliveries,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{Namespace: "machinarchy", Subsystem: "controlmanager",
			Name: "queue_depth", Help: "Resource keys waiting in the work queue."}, queueDepth))
	return m
}
