package main

import (
	"embed"
	"fmt"
	"linkstar/modules/stun"
	"linkstar/routers"
	"os"
)

//go:embed web
var webFS embed.FS

func main() {
	os.Setenv("TZ", "Asia/Shanghai")

	if err := stun.InitSTUN(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	routers.Run(webFS)
}
