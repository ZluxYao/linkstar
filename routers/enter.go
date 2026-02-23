package routers

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

func Run(webFS fs.FS) {
	gin.SetMode("release")
	r := gin.Default()
	r.RedirectTrailingSlash = false

	// API 路由
	g := r.Group("api")
	StunRouters(g)

	// 剥掉 web/dist 前缀
	webFS, _ = fs.Sub(webFS, "web/dist")
	// 所有非 API 请求：先找静态文件，找不到就返回 index.html（Vue Router 兜底）
	r.NoRoute(func(c *gin.Context) {
		path := c.Request.URL.Path

		if strings.HasPrefix(path, "/api") {
			c.JSON(404, gin.H{"error": "not found"})
			return
		}

		filePath := strings.TrimPrefix(path, "/")
		if _, err := fs.Stat(webFS, filePath); err == nil {
			c.FileFromFS(filePath, http.FS(webFS))
			return
		}

		// 兜底：返回 index.html
		data, _ := fs.ReadFile(webFS, "index.html")
		c.Data(200, "text/html; charset=utf-8", data)
	})

	logrus.Info("后端运行在：0.0.0.0:3333")
	if err := r.Run("0.0.0.0:3333"); err != nil {
		logrus.Fatal("启动失败：", err)
	}
}
