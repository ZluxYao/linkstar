package stun

import (
	"context"
	"fmt"
	"linkstar/modules/stun/model"
	"sync"
	"time"
)

type Backoff struct {
	steps []time.Duration
	idx   int
}

func NewBackoff() *Backoff {
	return &Backoff{
		steps: []time.Duration{
			1 * time.Second,
			2 * time.Second,
			4 * time.Second,
			5 * time.Minute,
		},
	}
}

func (b *Backoff) Next() time.Duration {
	if b.idx < len(b.steps) {
		d := b.steps[b.idx]
		b.idx++
		return d
	}
	return 5 * time.Minute
}

func (b *Backoff) Reset() {
	b.idx = 0
}

func sleepWithCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

type serviceEntry struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type Scheduler struct {
	mu          sync.Mutex
	services    map[string]*serviceEntry
	runner      TunnelRunner
	environment TunnelEnvironmentProvider
}

func NewScheduler(runner TunnelRunner, environment TunnelEnvironmentProvider) *Scheduler {
	if runner == nil {
		runner = NewSTUNTunnelRunner()
	}
	if environment == nil {
		environment = func() TunnelEnvironment { return TunnelEnvironment{} }
	}

	return &Scheduler{
		services:    make(map[string]*serviceEntry),
		runner:      runner,
		environment: environment,
	}
}

func serviceKey(deviceID, serviceID uint) string {
	return fmt.Sprintf("%d-%d", deviceID, serviceID)
}

func (s *Scheduler) StartAll(devices []model.Device) {
	for i := range devices {
		device := &devices[i]
		for j := range device.Services {
			service := &device.Services[j]
			if service.Enabled {
				s.StartService(device, service)
			}
		}
	}
}

func (s *Scheduler) StartService(device *model.Device, service *model.Service) {
	key := serviceKey(device.DeviceID, service.ID)

	old := s.detachService(key)
	waitServiceEntry(old)

	resetServiceRuntime(service)
	if !service.Enabled {
		service.LastError = ""
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry := &serviceEntry{
		cancel: cancel,
		done:   make(chan struct{}),
	}

	s.mu.Lock()
	if _, exists := s.services[key]; exists {
		s.mu.Unlock()
		cancel()
		return
	}
	s.services[key] = entry
	s.mu.Unlock()

	go s.runService(ctx, device, service, key, entry)
}

func (s *Scheduler) StopService(deviceID, serviceID uint) {
	key := serviceKey(deviceID, serviceID)
	waitServiceEntry(s.detachService(key))
}

func (s *Scheduler) StopAll() {
	s.mu.Lock()
	entries := make([]*serviceEntry, 0, len(s.services))
	for key, entry := range s.services {
		delete(s.services, key)
		entry.cancel()
		entries = append(entries, entry)
	}
	s.mu.Unlock()

	for _, entry := range entries {
		waitServiceEntry(entry)
	}
}

func (s *Scheduler) runService(
	ctx context.Context,
	device *model.Device,
	service *model.Service,
	key string,
	entry *serviceEntry,
) {
	defer close(entry.done)
	defer s.releaseService(key, entry)

	const maxProbeFailures = 5

	probeFailures := 0
	backoff := NewBackoff()

	for {
		ready := false
		err := s.runner.Run(ctx, s.buildRequest(device, service), func(result TunnelReady) {
			ready = true
			service.PunchSuccess = true
			service.ExternalPort = result.ExternalPort
			service.LastError = ""
		})

		if ctx.Err() != nil {
			resetServiceRuntime(service)
			service.LastError = ""
			return
		}

		resetServiceRuntime(service)
		if err == nil {
			service.LastError = ""
			return
		}

		service.LastError = err.Error()
		if ready {
			probeFailures = 0
			backoff.Reset()
			if !sleepWithCtx(ctx, backoff.Next()) {
				service.LastError = ""
				return
			}
			continue
		}

		probeFailures++
		if probeFailures >= maxProbeFailures {
			service.Enabled = false
			return
		}

		if !sleepWithCtx(ctx, 1*time.Second) {
			service.LastError = ""
			return
		}
	}
}

func (s *Scheduler) buildRequest(device *model.Device, service *model.Service) TunnelRequest {
	environment := TunnelEnvironment{}
	if s.environment != nil {
		environment = s.environment()
	}

	return TunnelRequest{
		ServiceName:  service.Name,
		TargetIP:     device.IP,
		InternalPort: service.InternalPort,
		Protocol:     service.Protocol,
		UseUPnP:      service.UseUPnP,
		Environment:  environment,
	}
}

func (s *Scheduler) detachService(key string) *serviceEntry {
	s.mu.Lock()
	entry := s.services[key]
	if entry != nil {
		delete(s.services, key)
	}
	s.mu.Unlock()

	if entry != nil {
		entry.cancel()
	}

	return entry
}

func (s *Scheduler) releaseService(key string, entry *serviceEntry) {
	s.mu.Lock()
	if current, ok := s.services[key]; ok && current == entry {
		delete(s.services, key)
	}
	s.mu.Unlock()
}

func waitServiceEntry(entry *serviceEntry) {
	if entry == nil {
		return
	}

	select {
	case <-entry.done:
	case <-time.After(15 * time.Second):
	}
}

func resetServiceRuntime(service *model.Service) {
	service.PunchSuccess = false
	service.ExternalPort = 0
}
