package stun

import "context"

type TunnelEnvironment struct {
	LocalIP  string
	BestSTUN string
}

type TunnelRequest struct {
	ServiceName  string
	TargetIP     string
	InternalPort uint16
	Protocol     string
	UseUPnP      bool
	Environment  TunnelEnvironment
}

type TunnelReady struct {
	ExternalPort uint16
}

type TunnelRunner interface {
	Run(ctx context.Context, req TunnelRequest, onReady func(TunnelReady)) error
}

type TunnelEnvironmentProvider func() TunnelEnvironment

type STUNTunnelRunner struct{}

func NewSTUNTunnelRunner() TunnelRunner {
	return STUNTunnelRunner{}
}
