package main

import (
	"linkstar/core"
	"linkstar/modules/stun"
	"linkstar/routers"
	"os"

	"github.com/sirupsen/logrus"
)

func main() {
	// 设置时区
	os.Setenv("TZ", "Asia/Shanghai")
	core.InitLogger()
	logrus.Info("LinkStar Run")

	stun.InitSTUN()

	routers.Run()
	select {}
}
