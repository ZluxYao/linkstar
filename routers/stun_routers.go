package routers

import (
	"linkstar/api"

	"github.com/gin-gonic/gin"
)

func StunRouters(g *gin.RouterGroup) {
	var app = api.App.StunApi

	// 输出当前stun配置文件
	g.GET(
		"stun/config",
		app.GetStunConfigView,
	)

}
