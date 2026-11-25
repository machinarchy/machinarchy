package e2e

import (
	"net/http"
	"testing"
	"time"
)

// Helpers for capability Operations (ADR-057): agent hosts live in the
// managed inventory on an Access Profile with the agent connector, and
// capability calls are Operations on those hosts.

// agentProfile creates an Access Profile whose hosts are reached through
// their enrolled agent.
func agentProfile(t *testing.T, client *api) string {
	t.Helper()
	var profile struct {
		ID string `json:"id"`
	}
	if status := client.do(t, http.MethodPost, "/api/v1/managed/access-profiles",
		map[string]any{"name": "agents", "spec": map[string]any{"connectorKind": "agent", "enabled": true}}, &profile); status != http.StatusCreated || profile.ID == "" {
		t.Fatalf("create agent Access Profile: %d %+v", status, profile)
	}
	return profile.ID
}

// agentHost registers a managed host on the agent profile.
func agentHost(t *testing.T, client *api, profileID, name string) string {
	t.Helper()
	var host struct {
		Metadata struct {
			ID string `json:"id"`
		} `json:"metadata"`
	}
	if status := client.do(t, http.MethodPost, "/api/v1/managed/hosts",
		map[string]any{"name": name, "spec": map[string]any{"accessProfileId": profileID}}, &host); status != http.StatusCreated || host.Metadata.ID == "" {
		t.Fatalf("create agent host %s: %d %+v", name, status, host)
	}
	return host.Metadata.ID
}

// hostToken issues a single-use enrolment token that binds the agent to hostID.
func hostToken(t *testing.T, client *api, hostID, hostname string) string {
	t.Helper()
	var issued struct {
		Token  string `json:"token"`
		HostID string `json:"hostId"`
	}
	if status := client.do(t, http.MethodPost, "/api/v1/agents/enrollment-tokens",
		map[string]any{"hostname": hostname, "hostId": hostID, "ttlSeconds": 600}, &issued); status != http.StatusCreated || issued.Token == "" || issued.HostID != hostID {
		t.Fatalf("issue enrolment token for host %s: %d", hostID, status)
	}
	return issued.Token
}

type operationView struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status struct {
		State   string `json:"state"`
		Targets []struct {
			Spec struct {
				HostID string `json:"hostId"`
			} `json:"spec"`
			State  string `json:"state"`
			Result *struct {
				Output     string `json:"output"`
				ErrorClass string `json:"errorClass"`
			} `json:"result"`
		} `json:"targets"`
	} `json:"status"`
}

func (o operationView) target(hostID string) (state, output, errorClass string) {
	for _, target := range o.Status.Targets {
		if target.Spec.HostID == hostID {
			if target.Result != nil {
				return target.State, target.Result.Output, target.Result.ErrorClass
			}
			return target.State, "", ""
		}
	}
	return "", "", ""
}

// submitCapability starts a capability Operation and returns its ID.
func submitCapability(t *testing.T, client *api, capability string, hostIDs ...string) string {
	t.Helper()
	var op operationView
	if status := client.do(t, http.MethodPost, "/api/v1/managed/operations", map[string]any{"hostIds": hostIDs,
		"capability": map[string]any{"name": capability, "timeoutSeconds": 20}}, &op); status != http.StatusAccepted || op.Kind != "Capability" {
		t.Fatalf("submit %s: %d %+v", capability, status, op)
	}
	return op.ID
}

// waitOperation waits until the Operation is final and returns it.
func waitOperation(t *testing.T, client *api, id string) operationView {
	t.Helper()
	var op operationView
	waitFor(t, 60*time.Second, "Operation "+id+" to finish", func() bool {
		op = operationView{}
		if client.do(t, http.MethodGet, "/api/v1/managed/operations/"+id, nil, &op) != http.StatusOK {
			return false
		}
		switch op.Status.State {
		case "Queued", "Running", "CancelRequested", "":
			return false
		}
		return true
	})
	return op
}

// runCapability runs a capability Operation to the end.
func runCapability(t *testing.T, client *api, capability string, hostIDs ...string) operationView {
	t.Helper()
	return waitOperation(t, client, submitCapability(t, client, capability, hostIDs...))
}
