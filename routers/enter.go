package routers

import "github.com/gin-gonic/gin"

func Run() {
	gin.SetMode("release")
	r := gin.Default()

	g := r.Group("api")
	StunRouters(g)

	r.Run("0.0.0.0")
}
