package telemetry

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// MustRegister registers collectors on registerer, which may be nil (the
// collectors then still work, unexported). A collector already registered
// with an identical description is accepted, so a component can be built
// twice in one process; any other conflict is a programming error and panics.
func MustRegister(registerer prometheus.Registerer, collectors ...prometheus.Collector) {
	if registerer == nil {
		return
	}
	for _, collector := range collectors {
		if err := registerer.Register(collector); err != nil {
			var already prometheus.AlreadyRegisteredError
			if !errors.As(err, &already) {
				panic(err)
			}
		}
	}
}
