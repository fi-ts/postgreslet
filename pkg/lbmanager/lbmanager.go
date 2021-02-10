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

func (m *LBManager) CreateLBIfNone(ctx context.Context, in *api.Postgres) error {
	if err := m.Get(ctx, client.ObjectKey{
		Namespace: in.Spec.ProjectID,
		Name:      in.ToSvcLBName(),
	}, &corev1.Service{}); err != nil {
		if !apimach.IsNotFound(err) {
			return fmt.Errorf("failed to fetch Service of type LoadBalancer: %w", err)
		}

		nextFreePort, err := m.nextFreePort(ctx)
		if err != nil {
			return fmt.Errorf("failed to get a free port for creating Service of type LoadBalancer: %w", err)
		}
		if err := m.Create(ctx, in.ToSvcLB(m.LBIP, nextFreePort)); err != nil {
			return fmt.Errorf("failed to create Service of type LoadBalancer: %w", err)
		}
		return nil
	}
	return nil
}

func (m *LBManager) DeleteLB(ctx context.Context, in *api.Postgres) error {
	lb := &corev1.Service{}
	lb.Namespace = in.Spec.ProjectID
	lb.Name = in.ToSvcLBName()
	if err := m.Delete(ctx, lb); client.IgnoreNotFound(err) != nil { // todo: remove ignorenotfound
		return err
	}
	return nil
}

const SvcTypeIndex = "spec.type"

func (m *LBManager) nextFreePort(ctx context.Context) (int32, error) {
	lbs := &corev1.ServiceList{}
	if err := m.List(ctx, lbs,
		client.MatchingFields{SvcTypeIndex: string(corev1.ServiceTypeLoadBalancer)},
		client.MatchingLabels{
			"application": "spilo",
			"spilo-role":  "master",
		},
	); err != nil {
		return 0, fmt.Errorf("failed to fetch the list of services of type LoadBalancer: %w", err)
	}

	if len(lbs.Items) == 0 {
		return m.PortRangeStart, nil
	}

	// Record weather any port is occupied
	isOccupied := make([]bool, int(m.PortRangeSize))
	for i := range lbs.Items {
		isOccupied[lbs.Items[i].Spec.Ports[0].Port-m.PortRangeStart] = true
	}

	for i := range isOccupied {
		if !isOccupied[i] {
			return m.PortRangeStart + int32(i), nil
		}
	}

	return 0, errors.New("no free port")
}
