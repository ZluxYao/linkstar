package stun

import (
	"fmt"
	"linkstar/global"
	"time"

	"golang.org/x/sync/errgroup"
)

func InitSTUN() error {
	var err error

	global.StunConfig, err = ReadStunConfig()
	if err != nil {
		return fmt.Errorf("read stun config failed: %w", err)
	}

	global.StunScheduler = NewScheduler(
		NewSTUNTunnelRunner(),
		func() TunnelEnvironment {
			return TunnelEnvironment{
				LocalIP:  global.StunConfig.LocalIP,
				BestSTUN: global.StunConfig.BestSTUN,
			}
		},
	)

	go SetupShutdownHook(func() {
		_ = UpdateStunConfig(global.StunConfig)
	})

	var g errgroup.Group

	g.Go(func() error {
		global.StunConfig.BestSTUN = GetFastStunServer()
		return nil
	})

	g.Go(func() error {
		natRouterList, err := GetNatRouterList()
		if err != nil {
			return err
		}
		global.StunConfig.NatRouterList = natRouterList
		return nil
	})

	g.Go(func() error {
		gateway := DiscoverUPnPGateway()
		SelectDefaultGateway(gateway)
		global.UpnpGateway = gateway
		return nil
	})

	if err := g.Wait(); err != nil {
		return fmt.Errorf("prepare stun runtime failed: %w", err)
	}

	publicIPInfo, err := GetPublicIPInfo()
	if err != nil {
		return fmt.Errorf("get public ip info failed: %w", err)
	}

	global.StunConfig.PublicIP = publicIPInfo.PublicIP
	global.StunConfig.LocalIP = publicIPInfo.LocalIP
	global.StunConfig.UpdatedAt = time.Now()

	go UpdatedPublicIP()
	go global.StunScheduler.StartAll(global.StunConfig.Devices)

	return nil
}
