package global

import (
	"linkstar/modules/stun/model"
)

var (
	StunConfig    model.StunConfig
	UpnpGateway   *model.UpnpGateway
	StunScheduler stunScheduler // 调度器接口，避免循环导入
)

// stunScheduler 定义调度器需要暴露给 global 的方法
// 具体实现是 stun.Scheduler，在 InitSTUN 时赋值
type stunScheduler interface {
	StartService(device *model.Device, service *model.Service)
	StopService(deviceID, serviceID uint)
	StartAllServices()
}
