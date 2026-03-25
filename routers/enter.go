package routers

import (
	"io/fs"
	"net/http"
	_ "net/http/pprof" // 加下划线，只要副作用（自动注册路由）
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

func Run(webFS fs.FS) {

	// 单独起 pprof，只在排查问题时用
	go func() {
		logrus.Info("pprof 运行在：0.0.0.0:3334")
		http.ListenAndServe("0.0.0.0:3334", nil)
	}()

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

	srv := &http.Server{
		Addr:        "0.0.0.0:3333",
		Handler:     r,
		IdleTimeout: 60 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		logrus.Fatal("启动失败：", err)
	}
}
