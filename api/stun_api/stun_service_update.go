package stun_api

import (
	"linkstar/global"
	"linkstar/middleware"
	"linkstar/modules/stun"
	"linkstar/utils/res"
	"time"

	"github.com/gin-gonic/gin"
)

type StunServiceUpdateViewRequest struct {
	DeviceID  uint   `json:"deviceId"`  // 设备ID
	ServiceID uint   `json:"serviceId"` // 服务ID
	Name      string `json:"name"`      // 服务名称
	InternalPort uint16 `json:"internalPort"` // 内网端口
	Protocol     string `json:"protocol"`     // 传输协议 "TCP"/"UDP"
	TLS          bool   `json:"tls"`          // 证书

	// UPnP 相关配置
	UseUPnP        bool   `json:"useUpnp"`
	UPnPMappedPort uint16 `json:"upnpMappedPort"`

	Enabled     bool   `json:"enabled"`
	Description string `json:"description"`
}

func (StunApi) StunServiceUpdateView(c *gin.Context) {
	cr := middleware.GetBindRequest[StunServiceUpdateViewRequest](c)

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

	// 更新服务字段
	svc := &global.StunConfig.Devices[deviceIndex].Services[serviceIndex]
	svc.Name = cr.Name
	svc.InternalPort = cr.InternalPort
	svc.Protocol = cr.Protocol
	svc.TLS = cr.TLS
	svc.UseUPnP = cr.UseUPnP
	svc.UPnPMappedPort = cr.UPnPMappedPort
	svc.Enabled = cr.Enabled
	svc.Description = cr.Description
	svc.UpdatedAt = time.Now()

	// 持久化配置到文件
	if err := stun.UpdateStunConfig(global.StunConfig); err != nil {
		res.FailWithMsg("保存配置失败", c)
		return
	}

	res.OkWithData(*svc, c)
}
