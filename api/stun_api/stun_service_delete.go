package stun_api

import (
	"linkstar/global"
	"linkstar/middleware"
	"linkstar/modules/stun"
	"linkstar/utils/res"

	"github.com/gin-gonic/gin"
)

type StunServiceDeleteViewRequest struct {
	DeviceID  uint `json:"deviceId"`  // 设备ID
	ServiceID uint `json:"serviceId"` // 服务ID
}

func (StunApi) StunServiceDeleteView(c *gin.Context) {
	cr := middleware.GetBindRequest[StunServiceDeleteViewRequest](c)

	// 查找目标设备
	deviceIndex := -1
	for i, device := range global.StunConfig.Devices {
		if device.DeviceID == cr.DeviceID {
			deviceIndex = i
			break
		}
	}
	if deviceIndex == -1 {
		res.FailWithMsg("设备不存在", c)
		return
	}

	// 查找目标服务
	serviceIndex := -1
	for i, svc := range global.StunConfig.Devices[deviceIndex].Services {
		if svc.ID == cr.ServiceID {
			serviceIndex = i
			break
		}
	}
	if serviceIndex == -1 {
		res.FailWithMsg("服务不存在", c)
		return
	}

	// 先停止 STUN 穿透，再从切片删除
	// 修复1：原版先删切片再 StopService，顺序颠倒（删了之后 StopService 其实无所谓，但语义上应先停再删）
	// 修复2：原版调用已删除的全局函数 stun.StopService，改为调度器实例方法
	global.StunScheduler.StopService(cr.DeviceID, cr.ServiceID)

	// 从切片中删除该服务
	services := global.StunConfig.Devices[deviceIndex].Services
	global.StunConfig.Devices[deviceIndex].Services = append(
		services[:serviceIndex],
		services[serviceIndex+1:]...,
	)

	// 持久化配置到文件
	if err := stun.UpdateStunConfig(global.StunConfig); err != nil {
		res.FailWithMsg("保存配置失败", c)
		return
	}

	res.OkWithMsg("删除成功", c)
}
