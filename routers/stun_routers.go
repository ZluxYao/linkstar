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

	// 修改服务
	g.PUT(
		"stun/service/update",
		middleware.BindJsonMiddleware[stun_api.StunServiceUpdateViewRequest],
		app.StunServiceUpdateView,
	)

}
