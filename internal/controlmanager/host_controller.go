package controlmanager

import (
	"context"
	"errors"
	"time"

	"github.com/machinarchy/machinarchy/internal/domain/resource"
)

type HostConverger interface {
	ReconcileHost(context.Context, *resource.Host) (resource.HostPhase, []resource.Condition, error)
}

type HostController struct {
	hosts          resource.HostRepository
	converger      HostConverger
	connectorRetry time.Duration
	operations     resource.OperationRepository
	operationRefs  resource.OperationHostReferenceRepository
}

func NewHostController(hosts resource.HostRepository, converger HostConverger) (*HostController, error) {
	return NewHostControllerWithResync(hosts, converger, 5*time.Minute)
}

func NewHostControllerWithResync(hosts resource.HostRepository, converger HostConverger, resync time.Duration) (*HostController, error) {
	return NewHostControllerWithOperations(hosts, converger, resync, nil)
}

func NewHostControllerWithOperations(hosts resource.HostRepository, converger HostConverger, resync time.Duration, operations resource.OperationRepository) (*HostController, error) {
	if hosts == nil {
		return nil, errors.New("host controller requires a host repository")
	}
	if resync <= 0 {
		resync = 5 * time.Minute
	}
	controller := &HostController{hosts: hosts, converger: converger, connectorRetry: resync, operations: operations}
	if lister, ok := operations.(resource.OperationHostReferenceRepository); ok {
		controller.operationRefs = lister
	}
	return controller, nil
}

func (c *HostController) Name() string       { return "host" }
func (c *HostController) Kind() ResourceKind { return HostResourceKind }

func (c *HostController) Reconcile(ctx context.Context, key ResourceKey) (ReconcileResult, error) {
	id, err := resource.ParseResourceID(key.ID)
	if err != nil {
		return ReconcileResult{}, Permanent(err)
	}
	host, err := c.hosts.GetHost(ctx, key.WorkspaceID, id)
	if errors.Is(err, resource.ErrHostNotFound) {
		return ReconcileResult{}, nil
	}
	if err != nil {
		return ReconcileResult{}, err
	}
	if host.Spec.Presence == resource.PresenceAbsent {
		if c.operationRefs != nil && c.operations != nil {
			keys, listErr := c.operationRefs.ListActiveOperationsForHost(ctx, key.WorkspaceID, id)
			if listErr != nil {
				return ReconcileResult{}, listErr
			}
			for _, operationKey := range keys {
				operation, getErr := c.operations.GetOperation(ctx, operationKey.WorkspaceID, operationKey.ID)
				if errors.Is(getErr, resource.ErrOperationNotFound) {
					continue
				}
				if getErr != nil {
					return ReconcileResult{}, getErr
				}
				if operation.IsTerminal() || operation.Status.CancelAsked {
					continue
				}
				expected := operation.Revision
				if err := operation.RequestCancel(time.Now()); err != nil {
					return ReconcileResult{}, err
				}
				if err := c.operations.UpdateOperation(ctx, operation, expected); err != nil && !errors.Is(err, resource.ErrConflict) {
					return ReconcileResult{}, err
				}
			}
			if len(keys) > 0 {
				return ReconcileResult{RequeueAfter: time.Second}, nil
			}
		}
		return ReconcileResult{}, c.hosts.DeleteHost(ctx, key.WorkspaceID, id, host.Metadata.Revision)
	}
	if c.converger == nil {
		conditions := []resource.Condition{
			{Type: "Ready", Status: resource.ConditionFalse, Reason: "ConnectorUnavailable", Message: "No Host convergence adapter is configured"},
			{Type: "Reachable", Status: resource.ConditionUnknown, Reason: "ConnectorUnavailable"},
			{Type: "Configured", Status: resource.ConditionUnknown, Reason: "ConnectorUnavailable"},
		}
		if host.Status.ObservedGeneration == host.Metadata.Generation && sameConditions(host.Status.Conditions, conditions) {
			return ReconcileResult{RequeueAfter: c.connectorRetry}, nil
		}
		if err := c.hosts.ObserveHost(ctx, key.WorkspaceID, id, host.Metadata.Generation, host.Status.Phase, conditions, time.Now()); err != nil {
			return ReconcileResult{}, err
		}
		return ReconcileResult{RequeueAfter: c.connectorRetry}, nil
	}
	phase, conditions, err := c.converger.ReconcileHost(ctx, host)
	if err != nil {
		return ReconcileResult{}, err
	}
	if err := c.hosts.ObserveHost(ctx, key.WorkspaceID, id, host.Metadata.Generation, phase, conditions, time.Now()); err != nil {
		return ReconcileResult{}, err
	}
	for _, condition := range conditions {
		if condition.Type == "Ready" && condition.Status != resource.ConditionTrue {
			return ReconcileResult{RequeueAfter: c.connectorRetry}, nil
		}
	}
	return ReconcileResult{}, nil
}

func sameConditions(current, expected []resource.Condition) bool {
	if len(current) != len(expected) {
		return false
	}
	for index := range expected {
		if current[index].Type != expected[index].Type || current[index].Status != expected[index].Status ||
			current[index].Reason != expected[index].Reason || current[index].Message != expected[index].Message {
			return false
		}
	}
	return true
}
