package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/infra"
	"github.com/Zleap-AI/Agent-Bridge/internal/service"
)

type configState struct {
	mu   sync.RWMutex
	cfg  *infra.Config
	save func(*infra.Config) error
}

func newConfigState(cfg *infra.Config, save func(*infra.Config) error) *configState {
	if cfg == nil {
		cfg = infra.DefaultConfig()
	}
	if save == nil {
		save = infra.SaveConfig
	}
	return &configState{cfg: cfg.Clone(), save: save}
}

func (s *configState) Snapshot() *infra.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Clone()
}

func (s *configState) Update(update func(*infra.Config)) (*infra.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg.Clone()
	update(next)
	if err := s.save(next.Clone()); err != nil {
		return nil, err
	}
	s.cfg = next
	return next.Clone(), nil
}

type tunnelStatus struct {
	State     string
	Connected bool
	LastError string
}

type tunnelController interface {
	Switch(config service.TunnelConfig)
	Stop()
	Status() tunnelStatus
}

type remoteTunnel interface {
	Start() error
	Stop()
}

type tunnelManager struct {
	mu            sync.RWMutex
	factory       func(service.TunnelConfig) remoteTunnel
	retryInterval time.Duration
	generation    uint64
	active        remoteTunnel
	cancel        context.CancelFunc
	status        tunnelStatus
}

func newTunnelManager(registry *agent.AgentRegistry, sessions *service.SessionManager) *tunnelManager {
	return &tunnelManager{
		factory: func(config service.TunnelConfig) remoteTunnel {
			return service.NewTunnelServiceWithSessionManager(registry, config, sessions)
		},
		retryInterval: 5 * time.Second,
		status:        tunnelStatus{State: "unpaired"},
	}
}

func (m *tunnelManager) Switch(config service.TunnelConfig) {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
	}
	m.generation++
	generation := m.generation
	old := m.active
	m.active = nil
	connectCtx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.status = tunnelStatus{State: "connecting"}
	m.mu.Unlock()
	if old != nil {
		old.Stop()
	}
	go m.connect(connectCtx, generation, config)
}

func (m *tunnelManager) connect(ctx context.Context, generation uint64, config service.TunnelConfig) {
	config.OnConnectionChange = func(connected bool, err error) {
		m.updateConnectionStatus(generation, connected, err)
	}
	for {
		tunnel := m.factory(config)
		m.mu.Lock()
		if generation != m.generation {
			m.mu.Unlock()
			tunnel.Stop()
			return
		}
		m.active = tunnel
		m.mu.Unlock()
		err := tunnel.Start()

		m.mu.Lock()
		if generation != m.generation {
			m.mu.Unlock()
			tunnel.Stop()
			return
		}
		if err == nil {
			// The concrete tunnel normally reports its precise state through
			// OnConnectionChange. Keep compatibility with simple implementations
			// that only implement Start/Stop.
			if m.status.State == "connecting" {
				m.status = tunnelStatus{State: "connected", Connected: true}
			}
			m.mu.Unlock()
			return
		}
		m.active = nil
		if isTerminalTunnelError(err) {
			lastError := err.Error()
			if m.status.LastError != "" {
				lastError = m.status.LastError
			}
			m.status = tunnelStatus{State: "disconnected", LastError: lastError}
			m.mu.Unlock()
			tunnel.Stop()
			return
		}
		m.status = tunnelStatus{State: "reconnecting", LastError: err.Error()}
		m.mu.Unlock()
		tunnel.Stop()

		timer := time.NewTimer(m.retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (m *tunnelManager) Stop() {
	m.mu.Lock()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.generation++
	active := m.active
	m.active = nil
	m.status = tunnelStatus{State: "unpaired"}
	m.mu.Unlock()
	if active != nil {
		active.Stop()
	}
}

func (m *tunnelManager) updateConnectionStatus(generation uint64, connected bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation != m.generation {
		return
	}
	if connected {
		m.status = tunnelStatus{State: "connected", Connected: true}
		return
	}
	state := "reconnecting"
	if isTerminalTunnelError(err) {
		state = "disconnected"
	}
	status := tunnelStatus{State: state}
	if err != nil {
		status.LastError = err.Error()
	}
	m.status = status
}

func isTerminalTunnelError(err error) bool {
	return errors.Is(err, service.ErrConnectionReplaced) || errors.Is(err, service.ErrCredentialsRevoked)
}

func (m *tunnelManager) Status() tunnelStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.status
}

func tunnelConfigFrom(cfg *infra.Config) service.TunnelConfig {
	config := service.DefaultTunnelConfig()
	config.ServerURL = cfg.ServerURL
	config.BridgeID = cfg.BridgeID
	config.Token = cfg.Token
	return config
}
