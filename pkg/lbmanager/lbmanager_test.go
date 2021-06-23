package lbmanager

import (
	"context"
	"fmt"
	"testing"

	api "github.com/fi-ts/postgreslet/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestLBManager_nextFreePort(t *testing.T) {
	portRangeStart := int32(0)
	portRangeSize := int32(5)

	tests := []struct {
		name     string
		lbMgr    *LBManager
		portWant int32
		wantErr  bool
	}{
		{
			name: "no svc in the cluster",
			lbMgr: &LBManager{
				Client:         fake.NewClientBuilder().WithScheme(scheme()).WithLists(svcListWithPorts()).Build(),
				LBIP:           "0.0.0.0",
				PortRangeStart: portRangeStart,
				PortRangeSize:  portRangeSize,
			},
			portWant: 0,
			wantErr:  false,
		},
		{
			name: "one svc already in the cluster",
			lbMgr: &LBManager{
				Client:         fake.NewClientBuilder().WithScheme(scheme()).WithLists(svcListWithPorts(0)).Build(),
				LBIP:           "0.0.0.0",
				PortRangeStart: portRangeStart,
				PortRangeSize:  portRangeSize,
			},
			portWant: 1,
			wantErr:  false,
		},
		{
			name: "last free port left",
			lbMgr: &LBManager{
				Client:         fake.NewClientBuilder().WithScheme(scheme()).WithLists(svcListWithPorts(0, 1, 2, 3)).Build(),
				LBIP:           "0.0.0.0",
				PortRangeStart: portRangeStart,
				PortRangeSize:  portRangeSize,
			},
			portWant: 4,
			wantErr:  false,
		},
		{
			name: "no free port",
			lbMgr: &LBManager{
				Client:         fake.NewClientBuilder().WithScheme(scheme()).WithLists(svcListWithPorts(0, 1, 2, 3, 4)).Build(),
				LBIP:           "0.0.0.0",
				PortRangeStart: portRangeStart,
				PortRangeSize:  portRangeSize,
			},
			portWant: 0,
			wantErr:  true,
		},
		{
			name: "re-use releaased port",
			lbMgr: &LBManager{
				Client:         fake.NewClientBuilder().WithScheme(scheme()).WithLists(svcListWithPorts(0, 2, 3)).Build(),
				LBIP:           "0.0.0.0",
				PortRangeStart: portRangeStart,
				PortRangeSize:  portRangeSize,
			},
			portWant: 1,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			_, portGot, err := tt.lbMgr.nextFreeSocket(context.Background())
			if (err != nil) != tt.wantErr {
				t.Errorf("LBManager.nextFreePort() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if portGot != tt.portWant {
				t.Errorf("LBManager.nextFreePort() = %v, want %v", portGot, tt.portWant)
			}
		})
	}
}

func scheme() *runtime.Scheme {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	return scheme
}

// svcListWithPorts generates a `ServiceList` containing `Service`s with ports respectively
func svcListWithPorts(ports ...int32) *corev1.ServiceList {
	svcList := &corev1.ServiceList{}
	for _, port := range ports {
		svcList.Items = append(svcList.Items, *svcWithPort(port))
	}
	return svcList
}

func svcWithPort(port int32) *corev1.Service {
	svc := corev1.Service{}
	svc.Name = fmt.Sprintf("svc-with-port-%d", port)
	svc.Labels = api.SvcLoadBalancerLabel
	svc.Spec.Ports = []corev1.ServicePort{
		{
			Port: port,
		},
	}
	return &svc
}
