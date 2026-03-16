package stun

import (
	"fmt"
	"linkstar/global"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

var (
	// 管理运行中的服务
	upnpClients []*internetgateway1.WANIPConnection1
)

func InitSTUN() error {
	var err error

	logrus.Info("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	logrus.Info("🚀 LinkStar 启动流程开始")
	logrus.Info("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	// 读取stun配置文件
	global.StunConfig, err = ReadStunConfig()
	if err != nil {
		logrus.Fatal("读取配置文件失败", err)
	}

	// 监听退出保持配置文件
	go SetupShutdownHook(func() {
		err := UpdateStunConfig(global.StunConfig)
		if err != nil {
			logrus.Error("保存配置失败：", err)
		}
	})

	// ========================================
	// 阶段1: 准备阶段 - 获取网络信息
	// ========================================
	logrus.Info("📋 阶段1: 准备网络环境...")

	var g errgroup.Group

	// 1.1 获取最快的 STUN 服务器
	g.Go(func() error {
		logrus.Info("  ⏳ 正在测试STUN服务器速度...")
		bestSTUN := GetFastStunServer()
		global.StunConfig.BestSTUN = bestSTUN
		logrus.Infof("  ✅ 最快STUN服务器: %s", bestSTUN)
		return nil
	})

	// 1.2 获取 NAT 路由列表
	g.Go(func() error {
		logrus.Info("  ⏳ 正在探测NAT网络拓扑...")
		natRouterList, err := GetNatRouterList()
		if err != nil {
			logrus.Errorf("  ❌ 获取NAT拓扑失败: %v", err)
			return err
		}
		global.StunConfig.NatRouterList = natRouterList
		logrus.Infof("  ✅ NAT层级: %d层", len(natRouterList))
		return nil
	})

	// 1.3 发现 UPnP 设备
	g.Go(func() error {
		logrus.Info("  ⏳ 正在发现UPnP网关...")
		clients, _, err := internetgateway1.NewWANIPConnection1Clients()
		if err == nil && len(clients) > 0 {
			upnpClients = clients
			externalIP, _ := clients[0].GetExternalIPAddress()
			logrus.Infof("  ✅ 发现UPnP网关，外部IP: %s", externalIP)
		} else {
			logrus.Warn("  ⚠️  未发现UPnP网关")
		}
		return nil
	})

	// 等待所有准备任务完成
	if err := g.Wait(); err != nil {
		logrus.Errorf("❌ 准备阶段失败: %v", err)
		return err
	}

	// 1.4 获取公网IP信息（依赖最快的STUN服务器）
	logrus.Info("  ⏳ 正在获取公网IP...")
	publicIPInfo, err := GetPublicIPInfo()
	if err != nil {
		logrus.Errorf("  ❌ 获取公网IP失败: %v", err)
		return err
	}
	global.StunConfig.PublicIP = publicIPInfo.PublicIP
	global.StunConfig.LocalIP = publicIPInfo.LocalIP
	logrus.Infof("  ✅ 本地IP: %s, 公网IP: %s", global.StunConfig.LocalIP, global.StunConfig.PublicIP)

	// 设置时间戳
	global.StunConfig.UpdatedAt = time.Now()

	logrus.Info("✅ 阶段1完成: 网络环境准备就绪")
	logrus.Info("")

	// ========================================
	// 阶段2: UPnP阶段 - 串行处理端口映射
	// ========================================
	logrus.Info("📋 阶段2: 配置UPnP端口映射...")

	// 启动UPnP队列处理器
	upnpQueue := GetUpnpQueue()
	upnpQueue.Start()

	// 统计需要UPnP的服务
	upnpCount := 0
	for i := range global.StunConfig.Devices {
		for j := range global.StunConfig.Devices[i].Services {
			service := &global.StunConfig.Devices[i].Services[j]
			if service.Enabled && service.UseUPnP {
				upnpCount++
			}
		}
	}

	if upnpCount > 0 {
		logrus.Infof("  ⏳ 需要配置 %d 个UPnP端口映射（串行处理，避免冲突）...", upnpCount)

		successCount := 0
		for i := range global.StunConfig.Devices {
			device := &global.StunConfig.Devices[i]
			for j := range device.Services {
				service := &device.Services[j]

				if !service.Enabled || !service.UseUPnP {
					continue
				}

				// 使用队列串行处理UPnP
				err := upnpQueue.AddPortMappingSync(
					service.ExternalPort,
					service.InternalPort,
					service.Protocol,
					fmt.Sprintf("%s-%s", device.Name, service.Name),
				)

				if err != nil {
					logrus.Warnf("  ⚠️  [%s-%s] UPnP映射失败: %v", device.Name, service.Name, err)
					service.UseUPnP = false // 失败则禁用UPnP
				} else {
					service.UPnPMappedPort = service.ExternalPort
					successCount++
					logrus.Infof("  ✅ [%s-%s] 端口 %d -> %d 映射成功",
						device.Name, service.Name, service.ExternalPort, service.InternalPort)
				}
			}
		}

		logrus.Infof("✅ 阶段2完成: UPnP配置完成 (%d/%d 成功)", successCount, upnpCount)
	} else {
		logrus.Info("  ℹ️  无需配置UPnP端口映射")
		logrus.Info("✅ 阶段2完成")
	}
	logrus.Info("")

	// ========================================
	// 阶段3: 启动阶段 - 分批启动STUN服务
	// ========================================
	logrus.Info("📋 阶段3: 启动STUN穿透服务...")

	go StartAllServicesBatched()

	logrus.Info("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	logrus.Info("✅ LinkStar 启动完成，服务正在后台运行")
	logrus.Info("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	return nil
}

// // upnp 端口转发
// if err := AddPortMapping(3335, 3335, "TCP", "Custom Port 3333 TCP"); err != nil {
// 	logrus.Errorf("端口转发失败:%v", err)
// }

// if err := DeletePortMapping(3335, "TCP"); err != nil {
// 	logrus.Errorf("端口删除失败:%v", err)
// }

// 2. 加载设备配置(从数据库或配置文件)
// global.StunConfig.Devices = append(global.StunConfig.Devices, model.Device{
// 	DeviceID: 1,
// 	Name:     "istore",
// 	IP:       "192.168.100.1",
// 	Services: []model.Service{
// 		{
// 			ID:           1,
// 			Name:         "Viepass",
// 			InternalPort: 5176,
// 			ExternalPort: 0,
// 			Protocol:     "TCP",
// 			Tlss:         true,
// 			Enabled:      true,
// 			Description:  "HTTP服务",
// 		},
// 	},
// })

// global.StunConfig.Devices = append(global.StunConfig.Devices, model.Device{
// 	DeviceID: 2,
// 	Name:     "本机",
// 	IP:       global.StunConfig.LocalIP,
// 	Services: []model.Service{
// 		{
// 			ID:           1,
// 			Name:         "STUN panel",
// 			InternalPort: 3336,
// 			ExternalPort: 0,
// 			Protocol:     "TCP",
// 			TLS:          false,
// 			Enabled:      true,
// 			Description:  "HTTP服务",
// 		},
// 		// {
// 		// 	ID:           1,
// 		// 	Name:         "7070",
// 		// 	InternalPort: 7070,
// 		// 	ExternalPort: 0,
// 		// 	Protocol:     "TCP",
// 		// 	Tlss:         false,
// 		// 	Enabled:      true,
// 		// 	Description:  "HTTP服务",
// 		// },
// 	},
// })
