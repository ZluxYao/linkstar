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

type StunDeviceAddViewRequest struct {
	Name string `json:"name"` // 设备名称，如 "群晖NAS" / "树莓派"
	IP   string `json:"ip"`   // 设备内网 IP
}

func (StunApi) StunDeviceAddView(c *gin.Context) {
	cr := middleware.GetBindRequest[StunDeviceAddViewRequest](c)

	if cr.Name == "" || cr.IP == "" {
		res.FailWithMsg("设备名称和IP不能为空", c)
		return
	}

	// 生成新设备ID（取当前最大ID+1）
	var maxID uint = 0
	for _, d := range global.StunConfig.Devices {
		if d.DeviceID > maxID {
			maxID = d.DeviceID
		}
	}

	newDevice := model.Device{
		DeviceID:  maxID + 1,
		Name:      cr.Name,
		IP:        cr.IP,
		Services:  []model.Service{},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	global.StunConfig.Devices = append(global.StunConfig.Devices, newDevice)

	if err := stun.UpdateStunConfig(global.StunConfig); err != nil {
		res.FailWithMsg("保存配置失败", c)
		return
	}

	res.OkWithData(newDevice, c)
}
