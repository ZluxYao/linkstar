package model

import (
	"github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/huin/goupnp/dcps/internetgateway2"
)

// upnp网关
type UpnpGateway struct {
	DefaultGateway string // 默认使用的网关类型

	DefaultV2    *internetgateway2.WANIPConnection1
	DefaultV1    *internetgateway1.WANIPConnection1
	DefaultV2ppp *internetgateway2.WANPPPConnection1
	DefaultV1ppp *internetgateway1.WANPPPConnection1

	V2    []*internetgateway2.WANIPConnection1
	V1    []*internetgateway1.WANIPConnection1
	V2ppp []*internetgateway2.WANPPPConnection1
	V1ppp []*internetgateway1.WANPPPConnection1
}
