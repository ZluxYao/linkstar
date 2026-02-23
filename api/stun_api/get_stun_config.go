package stun_api

import (
	"linkstar/global"
	"linkstar/utils/res"

	"github.com/gin-gonic/gin"
)

// 获取全部的stun配置文件信息
func (StunApi) GetStunConfigView(c *gin.Context) {

	data := global.StunConfig

	res.OkWithData(data, c)
}
