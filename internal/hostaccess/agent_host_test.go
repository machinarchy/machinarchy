package hostaccess_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/machinarchy/machinarchy/internal/agentgateway"
	"github.com/machinarchy/machinarchy/internal/domain/resource"
	"github.com/machinarchy/machinarchy/internal/hostaccess"
)

type agentProfiles struct {
	profile  *resource.AccessProfile
	revision resource.AccessProfileRevision
}

func (p agentProfiles) GetAccessProfile(context.Context, int, resource.ID) (*resource.AccessProfile, error) {
	return p.profile, nil
}
func (p agentProfiles) GetAccessProfileRevision(context.Context, int, resource.ID, uint64) (*resource.AccessProfileRevision, error) {
	return &p.revision, nil
}

type agentBinding struct {
	agent *agentgateway.Agent
	err   error
}

func (b agentBinding) HostAgent(context.Context, int, string) (*agentgateway.Agent, error) {
	return b.agent, b.err
}

// sshForbidden fails the test if an agent host is ever contacted over SSH.
type sshForbidden struct{ t *testing.T }

func (c sshForbidden) Verify(context.Context, *resource.Host, *resource.AccessProfileRevision) error {
	c.t.Error("an agent host was verified over SSH")
	return errors.New("ssh")
}
func (c sshForbidden) Execute(context.Context, *resource.Host, *resource.AccessProfileRevision, string, []string) (hostaccess.Result, error) {
	c.t.Error("an agent host was contacted over SSH")
	return hostaccess.Result{}, errors.New("ssh")
}

func TestAgentHostReadyFollowsItsAgent(t *testing.T) {
	now := time.Now()
	profile, revision, err := resource.NewAccessProfile(1, "agents", "", resource.AccessProfileSpec{Connector: resource.ConnectorAgent, Enabled: true}, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	host, err := resource.NewHost(1, 0, "agent-0", nil, resource.HostSpec{AccessProfileID: profile.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	revoked := now.Add(-time.Minute)
	cases := []struct {
		name      string
		bindings  *agentBinding
		ready     resource.ConditionStatus
		reason    string
		reachable resource.ConditionStatus
	}{
		{"gateway disabled", nil, resource.ConditionFalse, "AgentGatewayDisabled", resource.ConditionUnknown},
		{"not enrolled", &agentBinding{err: agentgateway.ErrHostNotBound}, resource.ConditionFalse, "AgentNotEnrolled", resource.ConditionUnknown},
		{"store error", &agentBinding{err: errors.New("database is locked")}, resource.ConditionFalse, "AgentBindingUnavailable", resource.ConditionFalse},
		{"lease expired", &agentBinding{agent: &agentgateway.Agent{ID: "a1", LeaseExpiresAt: now.Add(-time.Second)}}, resource.ConditionFalse, "AgentDisconnected", resource.ConditionFalse},
		{"revoked", &agentBinding{agent: &agentgateway.Agent{ID: "a1", LeaseExpiresAt: now.Add(time.Hour), RevokedAt: &revoked}}, resource.ConditionFalse, "AgentDisconnected", resource.ConditionFalse},
		{"connected", &agentBinding{agent: &agentgateway.Agent{ID: "a1", LeaseExpiresAt: now.Add(time.Hour)}}, resource.ConditionTrue, "AgentConnected", resource.ConditionTrue},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var options []hostaccess.Option
			if c.bindings != nil {
				options = append(options, hostaccess.WithAgentBindings(*c.bindings))
			}
			reconciler, err := hostaccess.NewHostReconciler(agentProfiles{profile, revision}, sshForbidden{t}, options...)
			if err != nil {
				t.Fatal(err)
			}
			observed, err := reconciler.ReconcileHost(context.Background(), host)
			if err != nil {
				t.Fatal(err)
			}
			ready, reachable := observed.Conditions[0], observed.Conditions[1]
			if ready.Type != "Ready" || ready.Status != c.ready || ready.Reason != c.reason || reachable.Status != c.reachable {
				t.Fatalf("conditions = %+v", observed.Conditions)
			}
		})
	}
}
