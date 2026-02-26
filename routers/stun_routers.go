package routers

import (
	"linkstar/api"
	"linkstar/api/stun_api"
	"linkstar/middleware"

	"github.com/gin-gonic/gin"
)

func StunRouters(g *gin.RouterGroup) {
	var app = api.App.StunApi

	// 输出当前stun配置文件
	g.GET(
		"stun/config",
		app.GetStunConfigView,
	)

	// 新增服务
	g.POST(
		"stun/service/add",
		middleware.BindJsonMiddleware[stun_api.StunServiceAddViewRequest],
		app.StunServiceAddView,
	)

	// 新增设备
	g.POST(
		"stun/device/add",
		middleware.BindJsonMiddleware[stun_api.StunDeviceAddViewRequest],
		app.StunDeviceAddView,
	)

	// 修改服务
	g.PUT(
		"stun/service/update",
		middleware.BindJsonMiddleware[stun_api.StunServiceUpdateViewRequest],
		app.StunServiceUpdateView,
	)

	// 删除服务
	g.DELETE(
		"stun/service/delete",
		middleware.BindJsonMiddleware[stun_api.StunServiceDeleteViewRequest],
		app.StunServiceDeleteView,
	)

	// 删除设备
	g.DELETE(
		"stun/device/delete",
		middleware.BindJsonMiddleware[stun_api.StunDeviceDeleteViewRequest],
		app.StunDeviceDeleteView,
	)

	// 修改设备
	g.PUT(
		"stun/device/update",
		middleware.BindJsonMiddleware[stun_api.StunDeviceUpdateViewRequest],
		app.StunDeviceUpdateView,
	)

}
