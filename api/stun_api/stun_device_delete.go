package stun_api

import (
	"linkstar/global"
	"linkstar/middleware"
	"linkstar/modules/stun"
	"linkstar/utils/res"

	"github.com/gin-gonic/gin"
)

type StunDeviceDeleteViewRequest struct {
	DeviceID uint `json:"deviceId"` // 设备ID
}

func (StunApi) StunDeviceDeleteView(c *gin.Context) {
	cr := middleware.GetBindRequest[StunDeviceDeleteViewRequest](c)

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

	// 停止该设备下所有服务的 STUN 穿透
	for _, svc := range global.StunConfig.Devices[deviceIndex].Services {
		stun.StopService(cr.DeviceID, svc.ID)
	}

	// 从切片中删除该设备
	devices := global.StunConfig.Devices
	global.StunConfig.Devices = append(
		devices[:deviceIndex],
		devices[deviceIndex+1:]...,
	)

	// 持久化配置到文件
	if err := stun.UpdateStunConfig(global.StunConfig); err != nil {
		res.FailWithMsg("保存配置失败", c)
		return
	}

	res.OkWithMsg("删除成功", c)
}
