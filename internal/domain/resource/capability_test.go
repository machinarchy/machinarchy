package resource

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCapabilityOperationValidation(t *testing.T) {
	hostID, err := NewResourceID()
	if err != nil {
		t.Fatal(err)
	}
	targets := []OperationTargetSpec{{HostID: hostID, HostGeneration: 1, DisplayName: "agent-1"}}
	build := func(c *CapabilitySpec, run *RunSpec) error {
		_, err := NewCapabilityOperation(1, ActorRef{UserID: 1}, OperationSpec{Targets: targets, Capability: c, Run: run}, time.Now())
		return err
	}

	op, err := NewCapabilityOperation(1, ActorRef{UserID: 1}, OperationSpec{Targets: targets,
		Capability: &CapabilitySpec{Name: "service.status", TimeoutSeconds: 30}}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if op.Kind != OperationKindCapability || string(op.Spec.Capability.Input) != "{}" || op.Status.Targets[0].State != TargetPending {
		t.Fatalf("capability Operation = %+v", op)
	}

	for _, name := range []string{"service.status", "file_stat", "a", "pkg.v2.list"} {
		if err := build(&CapabilitySpec{Name: name, TimeoutSeconds: 1}, nil); err != nil {
			t.Errorf("name %q refused: %v", name, err)
		}
	}
	bad := map[string]*CapabilitySpec{
		"no capability":     nil,
		"upper case":        {Name: "Service.Status", TimeoutSeconds: 30},
		"leading dot":       {Name: ".status", TimeoutSeconds: 30},
		"double dot":        {Name: "service..status", TimeoutSeconds: 30},
		"space":             {Name: "service status", TimeoutSeconds: 30},
		"too long":          {Name: strings.Repeat("a", maxCapabilityNameLength+1), TimeoutSeconds: 30},
		"array input":       {Name: "service.status", Input: json.RawMessage(`[1]`), TimeoutSeconds: 30},
		"invalid input":     {Name: "service.status", Input: json.RawMessage(`{bad`), TimeoutSeconds: 30},
		"oversized input":   {Name: "service.status", Input: json.RawMessage(`{"x":"` + strings.Repeat("a", MaxCapabilityInputBytes) + `"}`), TimeoutSeconds: 30},
		"zero timeout":      {Name: "service.status"},
		"timeout above max": {Name: "service.status", TimeoutSeconds: MaxCapabilityTimeoutSeconds + 1},
	}
	for name, spec := range bad {
		if err := build(spec, nil); !errors.Is(err, ErrInvalidResource) {
			t.Errorf("%s: err = %v, want ErrInvalidResource", name, err)
		}
	}
	if err := build(&CapabilitySpec{Name: "service.status", TimeoutSeconds: 30}, &RunSpec{Argv: []string{"true"}, TimeoutSeconds: 30, Concurrency: 1}); !errors.Is(err, ErrInvalidResource) {
		t.Errorf("capability with a Run spec: err = %v", err)
	}
	// A Run Operation takes no capability.
	if _, err := NewRunOperation(1, ActorRef{UserID: 1}, OperationSpec{Targets: targets, Run: &RunSpec{Argv: []string{"true"}, TimeoutSeconds: 30, Concurrency: 1},
		Capability: &CapabilitySpec{Name: "service.status", TimeoutSeconds: 30}}, time.Now()); !errors.Is(err, ErrInvalidResource) {
		t.Errorf("run with a capability: err = %v", err)
	}
}

func TestAgentAccessProfileTakesNoSSHSettings(t *testing.T) {
	now := time.Now()
	if _, _, err := NewAccessProfile(1, "agents", "", AccessProfileSpec{Connector: ConnectorAgent, Enabled: true}, 1, now); err != nil {
		t.Fatalf("plain agent profile: %v", err)
	}
	for name, spec := range map[string]AccessProfileSpec{
		"username":       {Username: "root"},
		"authentication": {Authentication: AuthenticationPassword},
		"credential":     {Credential: SecretReference{ID: "s", Version: 1}},
		"host keys":      {HostKeyPolicy: "x"},
		"CAs":            {HostCertificateAuthorities: []string{"ssh-ed25519 AAAA"}},
		"execution":      {Execution: &ExecutionSettings{}},
	} {
		spec.Connector, spec.Enabled = ConnectorAgent, true
		if _, _, err := NewAccessProfile(1, "agents", "", spec, 1, now); !errors.Is(err, ErrInvalidResource) {
			t.Errorf("agent profile with %s: err = %v", name, err)
		}
	}
	if _, _, err := NewAccessProfile(1, "x", "", AccessProfileSpec{Connector: "telnet", Enabled: true}, 1, now); !errors.Is(err, ErrInvalidResource) {
		t.Errorf("unknown connector: err = %v", err)
	}
}
