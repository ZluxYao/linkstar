package core

import (
	"fmt"
	"linkstar/utils"
	"os"
)

func InitStunServers() []string {
	configStunServersPath := "config/stunServers.json"
	var stunServers []string

	// 检查文件是否存在
	if _, err := os.Stat(configStunServersPath); os.IsNotExist(err) {

		stunServers = []string{"stun.radiojar.com:3478",
			"stun.ringostat.com:3478",
			"stun.irishvoip.com:3478",
			"stun.voipgate.com:3478",
			"stun.tula.nu:3478",
			"stun.yesdates.com:3478",
			"stun.telnyx.com:3478",
			"stun.vavadating.com:3478",
			"stun.bau-ha.us:3478",
			"stun.bridesbay.com:3478",
			"stun.3wayint.com:3478",
			"stun.finsterwalder.com:3478",
			"stun.romaaeterna.nl:3478",
			"stun.fitauto.ru:3478",
			"stun.antisip.com:3478",
			"stun.heeds.eu:3478",
			"stun.hot-chilli.net:3478",
			"stun.eurosys.be:3478",
			"stun.vincross.com:3478",
			"stun.cibercloud.com.br:3478",
			"stun.siptrunk.com:3478",
		}
		if err := utils.WriteJsonFile(configStunServersPath, stunServers); err != nil {
			fmt.Println("写入失败：", err)
		}

	} else {
		// 文件存在就读取
		stunServers, err = utils.ReadJsonFile[[]string](configStunServersPath)
		if err != nil {
			fmt.Printf("stunService读取失败：%s\n", err)
		}
	}
	return stunServers
}
