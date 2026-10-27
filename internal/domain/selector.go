package domain

// HostSelector defines criteria for filtering managed hosts across the fleet.
type HostSelector struct {
	EnvironmentID *int              `json:"environment_id,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Hostnames     []string          `json:"hostnames,omitempty"`
}

// Matches checks if a given host satisfies the selector criteria.
func (s *HostSelector) Matches(h *Host) bool {
	if h == nil {
		return false
	}

	// Filter by Environment ID if specified
	if s.EnvironmentID != nil && h.EnvironmentID != *s.EnvironmentID {
		return false
	}

	// Filter by specific hostnames if provided
	if len(s.Hostnames) > 0 {
		matched := false
		for _, name := range s.Hostnames {
			if h.Hostname == name {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Filter by labels (all specified selector labels must match host labels)
	for k, requiredVal := range s.Labels {
		hostVal, exists := h.Labels[k]
		if !exists || hostVal != requiredVal {
			return false
		}
	}

	return true
}

// HostJobResult captures the per-host outcome of a fleet execution job.
type HostJobResult struct {
	Hostname string `json:"hostname"`
	Address  string `json:"address"`
	Status   string `json:"status"` // "success" or "failed"
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
}

// FleetExecutionResult captures aggregated and per-host outcomes for fleet jobs.
type FleetExecutionResult struct {
	TotalTargeted int             `json:"total_targeted"`
	SuccessCount  int             `json:"success_count"`
	FailureCount  int             `json:"failure_count"`
	Results       []HostJobResult `json:"results"`
}
