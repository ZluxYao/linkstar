package stun

import (
	"fmt"
	"linkstar/modules/stun/core"
)

func InitSTUN() {
	var stunServers []string

	stunServers = core.InitStunServers()
	fmt.Println(stunServers)

}
