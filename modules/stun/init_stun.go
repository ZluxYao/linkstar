package stun

import (
	"fmt"
	"linkstar/global"
	"sync"

	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

func InitSTUN() error {
	global.StunConfig.StunServerList = InitStunServers()

	var g errgroup.Group
	var mu sync.Mutex

	// 1. 获取最快的 STUN 服务器
	g.Go(func() error {
		bestSTUN := GetFastStunServer()
		mu.Lock()
		global.StunConfig.BestSTUN = bestSTUN
		mu.Unlock()
		return nil
	})

	// 2. 获取公网IP信息
	g.Go(func() error {
		publicIPInfo, err := GetPublicIPInfo()
		if err != nil {
			logrus.Errorf("获取网络信息失败:%v", err)
			return err
		}
		mu.Lock()
		global.StunConfig.PublicIP = publicIPInfo.PublicIP
		global.StunConfig.LocalIP = publicIPInfo.LocalIP
		mu.Unlock()
		return nil
	})

	// 3. 获取 NAT 路由列表
	g.Go(func() error {
		natRouterList, err := GetNatRouterList()
		if err != nil {
			logrus.Errorf("获取NatRouterList失败:%v", err)
			return err
		}
		mu.Lock()
		global.StunConfig.NatRouterList = natRouterList
		mu.Unlock()
		return nil
	})

	// 等待所有任务完成，如果有错误会返回第一个错误
	if err := g.Wait(); err != nil {
		logrus.Errorf("初始化STUN配置失败: %v", err)
		return err
	}

	fmt.Println("最快的stun服务器", global.StunConfig.BestSTUN)
	fmt.Println("本地ip:", global.StunConfig.LocalIP, "当前公网ip", global.StunConfig.PublicIP)
	fmt.Println("网络拓扑图", global.StunConfig.NatRouterList)

	return nil
}
