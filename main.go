package main

import (
	"linkstar/modules/stun"
	"linkstar/routers"
	"os"
)

func main() {
	// 设置时区
	os.Setenv("TZ", "Asia/Shanghai")

	stun.InitSTUN()

	routers.Run()
}
