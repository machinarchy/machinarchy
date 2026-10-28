package service

import "github.com/machinarchy/machinarchy/pkg/netpolicy"

type serviceOptions struct {
	targetPolicy netpolicy.Policy
}

// ServiceOption customizes service construction.
type ServiceOption func(*serviceOptions)

// WithTargetPolicy sets the destination policy applied to every SSH dial. The
// default is netpolicy.Default().
func WithTargetPolicy(policy netpolicy.Policy) ServiceOption {
	return func(o *serviceOptions) { o.targetPolicy = policy }
}

func applyOptions(opts []ServiceOption) serviceOptions {
	options := serviceOptions{targetPolicy: netpolicy.Default()}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	return options
}
