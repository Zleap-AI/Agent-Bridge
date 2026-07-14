package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/service"
)

type startErrorTunnel struct {
	startCount *atomic.Int32
	err        error
}

func (t *startErrorTunnel) Start() error {
	t.startCount.Add(1)
	return t.err
}

func (*startErrorTunnel) Stop() {}

func TestTunnelManagerReportsReplacementAsDisconnected(t *testing.T) {
	manager := &tunnelManager{generation: 1}
	manager.updateConnectionStatus(1, false, service.ErrConnectionReplaced)

	status := manager.Status()
	if status.State != "disconnected" || status.Connected {
		t.Fatalf("status = %+v, want disconnected", status)
	}
}

func TestTunnelManagerReportsOrdinaryFailureAsReconnecting(t *testing.T) {
	manager := &tunnelManager{generation: 1}
	manager.updateConnectionStatus(1, false, errors.New("network unavailable"))

	status := manager.Status()
	if status.State != "reconnecting" || status.Connected {
		t.Fatalf("status = %+v, want reconnecting", status)
	}
}

func TestTunnelManagerReportsRevokedCredentialsAsDisconnected(t *testing.T) {
	manager := &tunnelManager{generation: 1}
	manager.updateConnectionStatus(1, false, service.ErrCredentialsRevoked)

	status := manager.Status()
	if status.State != "disconnected" || status.Connected || status.LastError == "" {
		t.Fatalf("status = %+v, want actionable disconnected state", status)
	}
}

func TestTunnelManagerDoesNotRetryRevokedCredentials(t *testing.T) {
	var starts atomic.Int32
	manager := &tunnelManager{
		factory: func(service.TunnelConfig) remoteTunnel {
			return &startErrorTunnel{startCount: &starts, err: service.ErrCredentialsRevoked}
		},
		retryInterval: 5 * time.Millisecond,
		status:        tunnelStatus{State: "unpaired"},
	}
	manager.Switch(service.DefaultTunnelConfig())
	defer manager.Stop()

	deadline := time.Now().Add(time.Second)
	for manager.Status().State != "disconnected" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	status := manager.Status()
	if status.State != "disconnected" || status.LastError != service.ErrCredentialsRevoked.Error() {
		t.Fatalf("status = %+v, want actionable disconnected state", status)
	}

	time.Sleep(30 * time.Millisecond)
	if got := starts.Load(); got != 1 {
		t.Fatalf("Start calls = %d, want no retry after revoked credentials", got)
	}
}
