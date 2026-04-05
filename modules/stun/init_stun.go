package stun

import (
	"fmt"
	"linkstar/global"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

func InitSTUN() error {
	var err error

	// 读取stun配置文件
	global.StunConfig, err = ReadStunConfig()
	if err != nil {
		logrus.Fatal("读取配置文件失败", err)
	}

	// 初始化调度器，挂载到 global 供 API 层调用
	// 必须在 StartAllServices 之前完成，否则 API 层拿到的是 nil
	global.StunScheduler = NewScheduler()

	// 监听退出保持配置文件
	go SetupShutdownHook(func() {
		err := UpdateStunConfig(global.StunConfig)
		if err != nil {
			logrus.Error("保存配置失败：", err)
		}
	})

	// 监听协程数量
	// go func() {
	// 	for {
	// 		numGoroutines := runtime.NumGoroutine()
	// 		time.Sleep(1 * time.Second)
	// 		logrus.Infof("当前 goroutine 数量: %d", numGoroutines)
	// 	}
	// }()

	var g errgroup.Group //并发启动，减少时间

	// 1. 获取最快的 STUN 服务器
	g.Go(func() error {
		bestSTUN := GetFastStunServer()
		global.StunConfig.BestSTUN = bestSTUN
		return nil
	})

	// 3. 获取 NAT 路由列表
	g.Go(func() error {
		natRouterList, err := GetNatRouterList()
		if err != nil {
			logrus.Errorf("获取NatRouterList失败:%v", err)
			return err
		}
		global.StunConfig.NatRouterList = natRouterList
		return nil
	})

	// 发现 UPnP 设备
	g.Go(func() error {
		// 发现网关
		wg := DiscoverUPnPGateway()

		// 智能选择网关
		SelectDefaultGateway(wg)

		global.UpnpGateway = wg

		return nil
	})

	// 等待所有任务完成
	if err := g.Wait(); err != nil {
		logrus.Errorf("初始化STUN配置失败: %v", err)
		return err
	}

	// 2. 获取公网IP信息  得先获取最快的stun服务器
	publicIPInfo, err := GetPublicIPInfo()
	if err != nil {
		logrus.Errorf("获取网络信息失败:%v", err)
		return err
	}
	global.StunConfig.PublicIP = publicIPInfo.PublicIP
	global.StunConfig.LocalIP = publicIPInfo.LocalIP

	// 启动公网ip更新
	go UpdatedPublicIP()

	// 设置时间戳
	now := time.Now()
	global.StunConfig.UpdatedAt = now

	fmt.Println("最快的stun服务器", global.StunConfig.BestSTUN)
	fmt.Println("本地ip:", global.StunConfig.LocalIP, "当前公网ip", global.StunConfig.PublicIP)
	fmt.Println("网络拓扑图", global.StunConfig.NatRouterList)

	// 3. 启动所有服务的STUN映射（协程启动）
	// StartAllServices 已改为 Scheduler 实例方法，通过 global.StunScheduler 调用
	go global.StunScheduler.StartAllServices()
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("✅ 所有服务已启动,可通过以下地址访问:")
	return nil
}
