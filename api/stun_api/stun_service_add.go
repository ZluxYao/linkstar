package stun_api

import (
	"linkstar/global"
	"linkstar/middleware"
	"linkstar/modules/stun"
	"linkstar/modules/stun/model"
	"linkstar/utils/res"
	"time"

	"github.com/gin-gonic/gin"
)

type StunServiceAddViewRequest struct {
	DeviceID     uint   `json:"deviceId"`     // 设备ID
	Name         string `json:"name"`         // 服务名称,如 "SSH" / "Web管理" / "照片库"
	InternalPort uint16 `json:"internalPort"` // 内网端口,如 22
	Protocol     string `json:"protocol"`     // 传输协议 "TCP"/"UDP" (默认 TCP)
	TLS          bool   `json:"tls"`          // 证书

	// UPnP 相关配置
	UseUPnP        bool   `json:"useUpnp"`        // 是否启用 UPnP 自动端口映射 (默认 true)
	UPnPMappedPort uint16 `json:"upnpMappedPort"` // UPnP 实际映射成功的端口号

	Enabled     bool   `json:"enabled"`     // 服务是否启用 (默认 true)
	Description string `json:"description"` // 服务描述信息 (可选)
}

func (StunApi) StunServiceAddView(c *gin.Context) {
	cr := middleware.GetBindRequest[StunServiceAddViewRequest](c)

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

	// 生成新服务ID（取当前最大ID+1）
	var maxID uint = 0
	for _, svc := range global.StunConfig.Devices[deviceIndex].Services {
		if svc.ID > maxID {
			maxID = svc.ID
		}
	}

	// 构建新服务
	newService := model.Service{
		ID:             maxID + 1,
		Name:           cr.Name,
		InternalPort:   cr.InternalPort,
		Protocol:       cr.Protocol,
		TLS:            cr.TLS,
		UseUPnP:        cr.UseUPnP,
		UPnPMappedPort: cr.UPnPMappedPort,
		Enabled:        cr.Enabled,
		Description:    cr.Description,
		UpdatedAt:      time.Now(),
	}

	// 添加服务到设备
	global.StunConfig.Devices[deviceIndex].Services = append(
		global.StunConfig.Devices[deviceIndex].Services,
		newService,
	)

	// 持久化配置到文件
	if err := stun.UpdateStunConfig(global.StunConfig); err != nil {
		res.FailWithMsg("保存配置失败", c)
		return
	}

	res.OkWithData(newService, c)
}
