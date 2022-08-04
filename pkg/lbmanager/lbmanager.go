package lbmanager

import (
	"context"
	"errors"
	"fmt"

	api "github.com/fi-ts/postgreslet/api/v1"
	corev1 "k8s.io/api/core/v1"
	apimach "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Options struct {
	LBIP                        string
	PortRangeStart              int32
	PortRangeSize               int32
	EnableStandbyLeaderSelector bool
}

// LBManager Responsible for the creation and deletion of externally accessible Services to access the Postgresql clusters managed by the Postgreslet.
type LBManager struct {
	client.Client
	options Options
}

// New Creates a new LBManager with the given configuration
func New(client client.Client, opt Options) *LBManager {
	return &LBManager{
		Client:  client,
		options: opt,
	}
}

// CreateSvcLBIfNone Creates a new Service of type LoadBalancer for the given Postgres resource if neccessary
func (m *LBManager) CreateSvcLBIfNone(ctx context.Context, in *api.Postgres) error {
	svc := &corev1.Service{}
	if err := m.Get(ctx, client.ObjectKey{
		Namespace: in.ToPeripheralResourceNamespace(),
		Name:      in.ToSvcLBName(),
	}, svc); err != nil {
		if !apimach.IsNotFound(err) {
			return fmt.Errorf("failed to fetch Service of type LoadBalancer: %w", err)
		}

		existingLBIP, nextFreePort, err := m.nextFreeSocket(ctx)
		if err != nil {
			return fmt.Errorf("failed to get a free port for creating Service of type LoadBalancer: %w", err)
		}
		var lbIPToUse string
		if m.options.LBIP != "" {
			// a specific IP was configured in the config, so use that one
			lbIPToUse = m.options.LBIP
		} else if existingLBIP != "" {
			// no ip was configured, but one is already in use, so use the existing one
			lbIPToUse = existingLBIP
		} else {
			// nothing was configured, nothing exists yet, so use an empty address so a new loadbalancer will be created and assigned
			lbIPToUse = ""
		}

		if err := m.Create(ctx, in.ToSvcLB(lbIPToUse, nextFreePort, m.options.EnableStandbyLeaderSelector)); err != nil {
			return fmt.Errorf("failed to create Service of type LoadBalancer: %w", err)
		}
		return nil
	}

	// update the selector, and only the selector (we do NOT want the change the ip or port here!!!)
	if m.options.EnableStandbyLeaderSelector && !in.IsReplicationPrimary() {
		svc.Spec.Selector[api.SpiloRoleLabelName] = api.SpiloRoleLabelValueStandbyLeader
	} else {
		svc.Spec.Selector[api.SpiloRoleLabelName] = api.SpiloRoleLabelValueMaster
	}

	if err := m.Update(ctx, svc); err != nil {
		return fmt.Errorf("failed to update Service of type LoadBalancer: %w", err)
	}

	return nil
}

// DeleteSvcLB Deletes the corresponding Service of type LoadBalancer of the given Postgres resource.
func (m *LBManager) DeleteSvcLB(ctx context.Context, in *api.Postgres) error {
	lb := &corev1.Service{}
	lb.Namespace = in.ToPeripheralResourceNamespace()
	lb.Name = in.ToSvcLBName()
	if err := m.Delete(ctx, lb); client.IgnoreNotFound(err) != nil { // todo: remove ignorenotfound
		return err
	}
	return nil
}

// nextFreeSocket finds any existing LoadBalancerIP and the next free port out of the configure port range.
func (m *LBManager) nextFreeSocket(ctx context.Context) (string, int32, error) {
	// TODO prevent concurrency issues when calculating port / ip.

	anyExistingLBIP := ""

	// Fetch all services managed by this postgreslet
	lbs := &corev1.ServiceList{}
	if err := m.List(ctx, lbs, client.MatchingLabels(api.SvcLoadBalancerLabel)); err != nil {
		return anyExistingLBIP, 0, fmt.Errorf("failed to fetch the list of services of type LoadBalancer: %w", err)
	}

	// If there are none, this will be the first (managed) service we create, so start with PortRangeStart and return
	if len(lbs.Items) == 0 {
		return anyExistingLBIP, m.options.PortRangeStart, nil
	}

	// If there are already any managed services, store all the used ports in a slice.
	// Also store the LoadBalancerIP.
	portsInUse := make([]int32, 0, len(lbs.Items))
	for i := range lbs.Items {
		svc := lbs.Items[i]
		if len(svc.Spec.Ports) > 0 {
			portsInUse = append(portsInUse, svc.Spec.Ports[0].Port)
		}
		if svc.Spec.LoadBalancerIP != "" {
			// Technically, we only store the IP of the last Service in this list.
			// As there should only be one IP per postgreslet and one postgreslet per cluster, this is good enough.
			anyExistingLBIP = svc.Spec.LoadBalancerIP
		}
	}

	// Now try all ports in the configured port range to find a free one.
	// While not as effective as other implementations, this allows us to freely change PortRangeStart and PortRangeSize
	// retroactively without breaking the implementation.
	for port := m.options.PortRangeStart; port < m.options.PortRangeStart+m.options.PortRangeSize; port++ {
		if containsElem(portsInUse, port) {
			// Port already in use, try the next one
			continue
		}
		// The postgreslet hasn't assigned this port yet, so use it.
		return anyExistingLBIP, port, nil
	}

	// If we made it this far, no free port could be found.
	return anyExistingLBIP, 0, errors.New("no free port in the configured port range found")
}

func containsElem(s []int32, v int32) bool {
	for _, elem := range s {
		if elem == v {
			return true
		}
	}
	return false
}
