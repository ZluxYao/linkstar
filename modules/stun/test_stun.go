package stun

import (
	"fmt"
	"linkstar/global"
)

func TestRunStunTunnel() {
	err := RunStunTunnel(global.StunConfig.Devices[0].IP, &global.StunConfig.Devices[0].Services[0])
	if err != nil {
		fmt.Println(err)
	}
}
