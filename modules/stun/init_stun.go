package stun

import (
	"fmt"
	"linkstar/global"
	"linkstar/modules/stun/core"
)

func InitSTUN() {

	global.StunConfig.StunServerList = core.InitStunServers()

	global.StunConfig.BestSTUN = GetFastStunServer()

	fmt.Println(global.StunConfig.BestSTUN)

	GetPublicIPInfo()

}
