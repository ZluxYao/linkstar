package global

import (
	"linkstar/modules/stun/model"
)

var (
	StunConfig    model.StunConfig
	UpnpGateway   *model.UpnpGateway
	StunScheduler stunScheduler // 调度器接口，避免循环导入（实现是 stun.Scheduler，InitSTUN 时赋值）
)

// stunScheduler 定义调度器需要暴露给 global 的方法
type stunScheduler interface {
	StartService(device *model.Device, service *model.Service)
	StopService(deviceID, serviceID uint)
	StartAllServices()
}
