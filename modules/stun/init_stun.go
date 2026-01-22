package stun

import (
	"fmt"
	"linkstar/global"
	"linkstar/modules/stun/model"
	"log"
	"net/http"
	"strings"
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
	global.StunConfig.StunServerList = InitStunServers()

	var g errgroup.Group

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

	// Task B: 发现 UPnP 设备
	g.Go(func() error {
		clients, _, err := internetgateway1.NewWANIPConnection1Clients()
		if err == nil && len(clients) > 0 {
			upnpClients = clients
			externalIP, _ := clients[0].GetExternalIPAddress()
			logrus.Infof("📡 发现 UPnP 网关，外部IP: %s", externalIP)
		}
		return nil
	})

	// 等待所有任务完成
	if err := g.Wait(); err != nil {
		logrus.Errorf("初始化STUN配置失败: %v", err)
		return err
	}

	// 2. 获取公网IP信息

	publicIPInfo, err := GetPublicIPInfo()
	if err != nil {
		logrus.Errorf("获取网络信息失败:%v", err)

	}
	global.StunConfig.PublicIP = publicIPInfo.PublicIP
	global.StunConfig.LocalIP = publicIPInfo.LocalIP

	// 设置时间戳
	now := time.Now()
	global.StunConfig.CreatedAt = now
	global.StunConfig.UpdatedAt = now

	fmt.Println("最快的stun服务器", global.StunConfig.BestSTUN)
	fmt.Println("本地ip:", global.StunConfig.LocalIP, "当前公网ip", global.StunConfig.PublicIP)
	fmt.Println("网络拓扑图", global.StunConfig.NatRouterList)

	// 启动本地 HTTP 服务
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("📡 步骤 1/5: 启动本地 HTTP 服务")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	startLocalHTTPService(3336)

	// // upnp 端口转发
	// if err := AddPortMapping(3335, 3335, "TCP", "Custom Port 3333 TCP"); err != nil {
	// 	logrus.Errorf("端口转发失败:%v", err)
	// }

	// if err := DeletePortMapping(3335, "TCP"); err != nil {
	// 	logrus.Errorf("端口删除失败:%v", err)
	// }

	// 2. 加载设备配置(从数据库或配置文件)
	global.StunConfig.Devices = append(global.StunConfig.Devices, model.Device{
		DeviceID: 1,
		Name:     "本机",
		IP:       "192.168.100.1",
		Services: []model.Service{
			// {
			// 	ID:           1,
			// 	Name:         "Web管理",
			// 	InternalPort: 3336,
			// 	ExternalPort: 0,
			// 	Protocol:     "TCP",
			// 	Enabled:      true,
			// 	Description:  "HTTP服务",
			// },
			{
				ID:           1,
				Name:         "Viepass",
				InternalPort: 5176,
				ExternalPort: 0,
				Protocol:     "TCP",
				Enabled:      true,
				Description:  "HTTP服务",
			},
		},
	})
	TestRunStunTunnel()
	// 3. 配置所有服务的STUN映射
	// if err := SetupDeviceServices(device); err != nil {
	// 	log.Fatalf("配置服务失败: %v", err)
	// }
	// fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	// fmt.Println("✅ 所有服务已启动,可通过以下地址访问:")
	// // 注意：由于是异步启动，立即打印可能端口还未获取到，实际以日志为准
	// time.Sleep(1 * time.Second)
	return nil
}

// ========== 启动本地 HTTP 服务 ==========
func startLocalHTTPService(port int) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		// 从 global.StunConfig 读取数据
		cfg := global.StunConfig

		// 判断 NAT 类型
		natEnv := "未知NAT环境"
		natType := "检测中..."
		natLevelCount := len(cfg.NatRouterList)

		if natLevelCount == 0 {
			natEnv = "直连公网"
			natType = "无NAT"
		} else if natLevelCount == 1 {
			natEnv = "单层NAT"
			natType = "家庭路由器NAT"
		} else if natLevelCount >= 2 {
			natEnv = fmt.Sprintf("%d层NAT", natLevelCount)
			// 检查是否有运营商 CGN (100.64.0.0/10)
			hasCGN := false
			for _, router := range cfg.NatRouterList {
				if strings.HasPrefix(router.LanIp, "100.") {
					hasCGN = true
					break
				}
			}
			if hasCGN {
				natType = "运营商CGN + 家庭路由器"
			} else {
				natType = "多级路由器NAT"
			}
		}

		// 构建网络拓扑图
		flowChart := buildFlowChart(cfg, port)

		// 获取公网访问地址
		publicAddr := "未获取"
		if cfg.PublicIP != "" {
			publicAddr = fmt.Sprintf("http://%s:%d", cfg.PublicIP, port)
		}

		html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN">
<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<title>🎉 NAT穿透成功！</title>
	<style>
		* { margin: 0; padding: 0; box-sizing: border-box; }
		body {
			font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
			background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
			min-height: 100vh;
			display: flex;
			align-items: center;
			justify-content: center;
			padding: 20px;
		}
		.container {
			background: white;
			border-radius: 20px;
			box-shadow: 0 20px 60px rgba(0,0,0,0.3);
			max-width: 900px;
			width: 100%%;
			padding: 40px;
		}
		h1 {
			color: #667eea;
			font-size: 2.5em;
			margin-bottom: 20px;
			text-align: center;
		}
		.success-badge {
			background: linear-gradient(135deg, #10b981, #059669);
			color: white;
			padding: 20px;
			border-radius: 12px;
			margin: 20px 0;
		}
		.success-badge h2 {
			margin-bottom: 10px;
			font-size: 1.3em;
		}
		.info-item {
			display: flex;
			justify-content: space-between;
			padding: 8px 0;
			border-bottom: 1px solid rgba(255,255,255,0.2);
		}
		.info-item:last-child { border-bottom: none; }
		.label { font-weight: bold; }
		.flow-chart {
			background: #1e293b;
			color: #10b981;
			padding: 25px;
			border-radius: 12px;
			font-family: 'Monaco', 'Courier New', monospace;
			margin: 20px 0;
			line-height: 2;
			font-size: 0.9em;
			white-space: pre;
		}
		.section {
			background: #f8fafc;
			padding: 20px;
			border-radius: 12px;
			margin: 20px 0;
			border-left: 4px solid #667eea;
		}
		.section h3 {
			color: #334155;
			margin-bottom: 15px;
		}
		.tech-item {
			padding: 8px 0;
			color: #475569;
		}
		.highlight {
			background: #fef3c7;
			padding: 2px 6px;
			border-radius: 4px;
			font-weight: bold;
		}
		.nat-level {
			background: #e0e7ff;
			padding: 15px;
			border-radius: 8px;
			margin: 10px 0;
			font-family: monospace;
		}
		@media (max-width: 600px) {
			h1 { font-size: 1.8em; }
			.container { padding: 20px; }
		}
	</style>
</head>
<body>
	<div class="container">
		<h1>🎉 NAT穿透服务运行中</h1>
		
		<div class="success-badge">
			<h2>✅ 连接信息</h2>
			<div class="info-item">
				<span class="label">客户端IP:</span>
				<span>%s</span>
			</div>
			<div class="info-item">
				<span class="label">访问时间:</span>
				<span>%s</span>
			</div>
			<div class="info-item">
				<span class="label">请求路径:</span>
				<span>%s</span>
			</div>
			<div class="info-item">
				<span class="label">NAT环境:</span>
				<span>%s</span>
			</div>
			<div class="info-item">
				<span class="label">NAT类型:</span>
				<span>%s</span>
			</div>
		</div>

		<div class="section">
			<h3>🌐 网络拓扑结构 (共%d层NAT)</h3>
			%s
		</div>

		<div class="section">
			<h3>🔄 数据传输完整链路</h3>
			<div class="flow-chart">%s</div>
		</div>

		<div class="section">
			<h3>🔧 技术实现原理</h3>
			<div class="tech-item">
				<strong>1. STUN探测:</strong> 使用 <span class="highlight">%s</span> 进行网络环境检测
			</div>
			<div class="tech-item">
				<strong>2. 多层NAT识别:</strong> 通过TTL递增探测，发现了 <span class="highlight">%d层</span> NAT设备
			</div>
			<div class="tech-item">
				<strong>3. 端口映射:</strong> 自动配置UPnP端口转发规则
			</div>
			<div class="tech-item">
				<strong>4. 连接保活:</strong> 定期发送心跳包维持NAT映射
			</div>
		</div>

		<div class="section">
			<h3>📊 关键信息</h3>
			<div class="tech-item">
				✅ 本机内网IP: <strong>%s</strong>
			</div>
			<div class="tech-item">
				✅ 公网访问地址: <strong>%s</strong>
			</div>
			<div class="tech-item">
				✅ HTTP服务端口: <strong>%d</strong>
			</div>
			<div class="tech-item">
				✅ NAT穿透状态: <strong>运行中</strong>
			</div>
			<div class="tech-item">
				⚠️  保持程序运行以维持穿透状态
			</div>
		</div>
	</div>
</body>
</html>`,
			r.RemoteAddr,
			time.Now().Format("2006-01-02 15:04:05"),
			r.URL.Path,
			natEnv,
			natType,
			natLevelCount,
			buildNATLevelHTML(cfg.NatRouterList),
			flowChart,
			cfg.BestSTUN,
			natLevelCount,
			cfg.LocalIP,
			publicAddr,
			port,
		)

		w.Write([]byte(html))
		log.Printf("✅ [HTTP请求] %s %s from %s\n", r.Method, r.URL.Path, r.RemoteAddr)
	})

	go func() {
		addr := fmt.Sprintf("0.0.0.0:%d", port)
		fmt.Printf("✅ HTTP 服务已启动: %s\n", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Fatalf("❌ HTTP 服务启动失败: %v\n", err)
		}
	}()

	time.Sleep(500 * time.Millisecond)
}

// 构建NAT层级HTML展示
func buildNATLevelHTML(natRouters []model.NatRouterInfo) string {
	if len(natRouters) == 0 {
		return `<div class="nat-level">📍 直连公网 (无NAT)</div>`
	}

	var html strings.Builder
	for _, router := range natRouters {
		icon := "🏠"
		deviceType := "家庭路由器"

		// 根据IP段判断设备类型
		if strings.HasPrefix(router.LanIp, "100.") {
			icon = "🌐"
			deviceType = "运营商CGN网关"
		} else if router.NatLevel > 1 {
			icon = "📡"
			deviceType = fmt.Sprintf("二级路由器 (Level %d)", router.NatLevel)
		}

		html.WriteString(fmt.Sprintf(
			`<div class="nat-level">%s <strong>NAT层级 %d:</strong> %s - LAN口IP: %s</div>`,
			icon, router.NatLevel, deviceType, router.LanIp,
		))
	}
	return html.String()
}

// 构建数据流转链路图
func buildFlowChart(cfg model.StunConfig, localPort int) string {
	var chart strings.Builder

	chart.WriteString("外网用户访问\n")
	chart.WriteString("   ↓\n")

	// 公网入口
	if cfg.PublicIP != "" {
		chart.WriteString(fmt.Sprintf("🌍 公网IP: %s\n", cfg.PublicIP))
	} else {
		chart.WriteString("🌍 公网IP: (检测中...)\n")
	}

	// NAT层级
	if len(cfg.NatRouterList) > 0 {
		for i := len(cfg.NatRouterList) - 1; i >= 0; i-- {
			router := cfg.NatRouterList[i]
			chart.WriteString("   ↓ (NAT转换)\n")

			if strings.HasPrefix(router.LanIp, "100.") {
				chart.WriteString(fmt.Sprintf("🌐 运营商CGN: %s\n", router.LanIp))
			} else {
				chart.WriteString(fmt.Sprintf("📡 路由器%d: %s\n", router.NatLevel, router.LanIp))
			}
		}
	}

	chart.WriteString("   ↓ (端口转发)\n")
	chart.WriteString(fmt.Sprintf("💻 本机服务: %s:%d\n", cfg.LocalIP, localPort))

	return chart.String()
}
