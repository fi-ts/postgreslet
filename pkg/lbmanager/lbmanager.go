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

type LBManager struct {
	client.Client         // todo: service cluster
	LBIP           string // todo: via configmap
	PortRangeStart int32  // todo: via configmap
	PortRangeSize  int32
}

func New(client client.Client, lbIP string, portRangeStart, portRangeSize int32) *LBManager {
	return &LBManager{
		Client:         client,
		LBIP:           lbIP,
		PortRangeStart: portRangeStart,
		PortRangeSize:  portRangeSize,
	}
}

func (m *LBManager) CreateSvcLBIfNone(ctx context.Context, in *api.Postgres) error {
	if err := m.Get(ctx, client.ObjectKey{
		Namespace: in.ToPeripheralResourceNamespace(),
		Name:      in.ToSvcLBName(),
	}, &corev1.Service{}); err != nil {
		if !apimach.IsNotFound(err) {
			return fmt.Errorf("failed to fetch Service of type LoadBalancer: %w", err)
		}

		existingLBIP, nextFreePort, err := m.nextFreeSocket(ctx)
		if err != nil {
			return fmt.Errorf("failed to get a free port for creating Service of type LoadBalancer: %w", err)
		}
		var lbIPToUse string
		if m.LBIP != "" {
			// a specific IP was configured in the config, so use that one
			lbIPToUse = m.LBIP
		} else if existingLBIP != "" {
			// no ip was configured, but one is already in use, so use the existing one
			lbIPToUse = existingLBIP
		} else {
			// nothing was configured, nothing exists yet, so use an empty address so a new loadbalancer will be created and assigned
			lbIPToUse = ""
		}

		if err := m.Create(ctx, in.ToSvcLB(lbIPToUse, nextFreePort)); err != nil {
			return fmt.Errorf("failed to create Service of type LoadBalancer: %w", err)
		}
		return nil
	}
	return nil
}

func (m *LBManager) DeleteSvcLB(ctx context.Context, in *api.Postgres) error {
	lb := &corev1.Service{}
	lb.Namespace = in.ToPeripheralResourceNamespace()
	lb.Name = in.ToSvcLBName()
	if err := m.Delete(ctx, lb); client.IgnoreNotFound(err) != nil { // todo: remove ignorenotfound
		return err
	}
	return nil
}

func (m *LBManager) nextFreeSocket(ctx context.Context) (string, int32, error) {
	// TODO prevent concurrency issues when calculating port / ip.

	existingLBIP := ""

	lbs := &corev1.ServiceList{}
	if err := m.List(ctx, lbs, client.MatchingLabels(api.SvcLoadBalancerLabel)); err != nil {
		return existingLBIP, 0, fmt.Errorf("failed to fetch the list of services of type LoadBalancer: %w", err)
	}

	if len(lbs.Items) == 0 {
		return existingLBIP, m.PortRangeStart, nil
	}

	// Record weather any port is occupied
	isOccupied := make([]bool, int(m.PortRangeSize))
	for i := range lbs.Items {
		svc := lbs.Items[i]
		// TODO panic: runtime error: index out of range [-23920]
		isOccupied[svc.Spec.Ports[0].Port-m.PortRangeStart] = true
		if svc.Spec.LoadBalancerIP != "" {
			existingLBIP = svc.Spec.LoadBalancerIP
		}
	}

	for i := range isOccupied {
		if !isOccupied[i] {
			return existingLBIP, m.PortRangeStart + int32(i), nil
		}
	}

	return existingLBIP, 0, errors.New("no free port")
}
