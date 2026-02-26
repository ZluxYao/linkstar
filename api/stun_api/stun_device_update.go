package stun_api

import (
	"linkstar/global"
	"linkstar/middleware"
	"linkstar/modules/stun"
	"linkstar/utils/res"
	"time"

	"github.com/gin-gonic/gin"
)

type StunDeviceUpdateViewRequest struct {
	DeviceID uint   `json:"deviceId"` // 设备ID
	Name     string `json:"name"`     // 设备名称
	IP       string `json:"ip"`       // 设备内网 IP
}

func (StunApi) StunDeviceUpdateView(c *gin.Context) {
	cr := middleware.GetBindRequest[StunDeviceUpdateViewRequest](c)

	if cr.Name == "" || cr.IP == "" {
		res.FailWithMsg("设备名称和IP不能为空", c)
		return
	}

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

	// 更新设备字段
	dev := &global.StunConfig.Devices[deviceIndex]
	oldIP := dev.IP
	dev.Name = cr.Name
	dev.IP = cr.IP
	dev.UpdatedAt = time.Now()

	// 持久化配置到文件
	if err := stun.UpdateStunConfig(global.StunConfig); err != nil {
		res.FailWithMsg("保存配置失败", c)
		return
	}

	// 若 IP 发生变化，重启该设备下所有已启用服务
	if oldIP != cr.IP {
		for j := range dev.Services {
			svc := &dev.Services[j]
			stun.StartService(dev, svc)
		}
	}

	res.OkWithData(*dev, c)
}
