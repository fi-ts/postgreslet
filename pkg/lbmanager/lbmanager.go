package lbmanager

import (
	"context"
	"fmt"
	"sync"

	api "github.com/fi-ts/postgreslet/api/v1"
	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type LBManger struct {
	client.Client
	LBIP           string
	PortRangeStart int32
	IsPortUsed     []bool
	NextFreePort   int32
	sync.Mutex
	logr.Logger
}

func NewLBManager(client client.Client, lbIP string, portRangeStart int32, portNum int, logger logr.Logger) *LBManger {
	return &LBManger{
		Client:         client,
		LBIP:           lbIP,
		PortRangeStart: portRangeStart,
		IsPortUsed:     make([]bool, portNum),
		NextFreePort:   portRangeStart,
		Mutex:          sync.Mutex{},
		Logger:         logger,
	}
}

func (m *LBManger) CreateLB(ctx context.Context, in *api.Postgres) error {
	m.Mutex.Lock()
	defer m.Mutex.Unlock()
	lbPort := m.NextFreePort
	if err := m.Create(ctx, in.ToSvcLB(m.LBIP, lbPort)); err != nil {
		return fmt.Errorf("failed to create Service of tpye LoadBalancer: %w", err)
	}
	m.NextFreePort = lbPort + 1
	return nil
}
