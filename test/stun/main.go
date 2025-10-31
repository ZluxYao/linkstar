package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway2"
	"github.com/pion/stun"
)

var (
	publicIP      string
	publicUDPPort int
	publicTCPPort int
	localIP       string
	udpConn       *net.UDPConn
	bestSTUN      *net.UDPAddr
	mappingActive = make(map[string]time.Time)
	tunnelStats   = make(map[string]*TunnelStats)
	mu            sync.Mutex
	upnpClient    *internetgateway2.WANIPConnection1
)

type TunnelStats struct {
	Protocol      string
	PublicAddr    string
	LocalAddr     string
	BytesSent     int64
	BytesReceived int64
	Connections   int
	CreateTime    time.Time
	LastActive    time.Time
	UPnPEnabled   bool
}

var stunServers = []string{
	"stun.freeswitch.org:3478",
	"stun.acrobits.cz:3478",
	"stun.commpeak.com:3478",
	"stun.antisip.com:3478",
	"stun.annatel.net:3478",
}

type TunnelConfig struct {
	Name         string
	Protocol     string
	LocalPort    int
	ExternalPort int
	UDPPort      int
}

func main() {
	fmt.Println("=== 内网穿透服务器 (UPnP 自动端口映射) ===\n")

	localIP = getLocalIP()

	// 1. 配置要穿透的服务
	tunnels := []TunnelConfig{
		{
			Name:         "Web服务",
			Protocol:     "TCP",
			LocalPort:    3333,
			ExternalPort: 0, // 0表示使用随机端口
			UDPPort:      33333,
		},
	}

	// 2. 为所有隧道初始化统计信息（防止空指针）
	for _, tunnel := range tunnels {
		tunnelStats[tunnel.Name] = &TunnelStats{
			Protocol:   tunnel.Protocol,
			LocalAddr:  fmt.Sprintf("%s:%d", localIP, tunnel.LocalPort),
			CreateTime: time.Now(),
			LastActive: time.Now(),
		}
	}

	// 3. 发现并连接 UPnP 网关
	fmt.Println("🔍 正在搜索 UPnP 网关设备...")
	if err := discoverUPnPGateway(); err != nil {
		log.Printf("⚠️  UPnP 发现失败: %v\n", err)
		log.Println("提示: 请确保路由器已启用 UPnP 功能")
	} else {
		fmt.Println("✓ UPnP 网关已连接")
	}

	// 4. 初始化UDP连接用于STUN
	initUDPConnection(tunnels[0].UDPPort)

	// 5. 选择最快的STUN服务器
	fmt.Println("\n正在测试 STUN 服务器...")
	selectBestSTUN()

	// 6. 获取公网地址
	getPublicAddress()

	// 7. 检查是否真的获取到了公网IP
	if !isPublicIP(publicIP) {
		fmt.Printf("\n⚠️  警告: 检测到多层NAT!\n")
		fmt.Printf("   路由器WAN口IP: %s (这是内网IP)\n", publicIP)
		fmt.Printf("   你的网络结构可能是: 光猫 → 路由器 → 你的设备\n")
		fmt.Printf("   建议:\n")
		fmt.Printf("   1. 将光猫设置为桥接模式\n")
		fmt.Printf("   2. 或在光猫上配置端口转发到路由器\n")
		fmt.Printf("   3. 使用公网IP查询服务获取真实公网IP\n\n")

		// 尝试通过HTTP服务获取真实公网IP
		if realIP := getRealPublicIP(); realIP != "" {
			fmt.Printf("✓ 通过外部服务获取真实公网IP: %s\n", realIP)
			publicIP = realIP
		}
	}

	// 8. 使用UPnP为TCP端口创建映射（支持随机端口）
	for i := range tunnels {
		if tunnels[i].Protocol == "TCP" {
			// 如果ExternalPort为0，使用随机端口
			if tunnels[i].ExternalPort == 0 {
				// 使用1024-65535之间的随机端口
				tunnels[i].ExternalPort = 10000 + (int(time.Now().Unix()) % 55535)
			}

			if err := createUPnPMapping(&tunnels[i]); err != nil {
				log.Printf("⚠️  UPnP 映射失败: %v\n", err)

				// 尝试使用其他端口
				fmt.Println("\n🔄 尝试使用其他可用端口...")
				success := false
				for port := tunnels[i].ExternalPort + 1; port < tunnels[i].ExternalPort+100; port++ {
					tunnels[i].ExternalPort = port
					if err := createUPnPMapping(&tunnels[i]); err == nil {
						fmt.Printf("✅ 成功使用替代端口: %d\n", port)
						success = true
						break
					}
				}

				if !success {
					log.Printf("提示: 需要手动在路由器配置端口转发 %d -> %s:%d\n",
						tunnels[i].ExternalPort, localIP, tunnels[i].LocalPort)
				}
			}
		}
	}

	// 9. 启动心跳保持NAT映射
	go keepNATMapping()
	go keepUPnPMappings(tunnels)

	// 10. 为每个隧道启动服务
	for _, tunnel := range tunnels {
		startTunnel(tunnel)
	}

	time.Sleep(500 * time.Millisecond)

	// 11. 显示穿透信息
	displayTunnelInfo(tunnels)

	// 保持运行
	select {}
}

// 检查是否是公网IP
func isPublicIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}

	// 检查是否是私有IP段
	privateRanges := []struct {
		start string
		end   string
	}{
		{"10.0.0.0", "10.255.255.255"},
		{"172.16.0.0", "172.31.255.255"},
		{"192.168.0.0", "192.168.255.255"},
		{"100.64.0.0", "100.127.255.255"}, // Carrier-grade NAT
	}

	for _, r := range privateRanges {
		if inRange(parsed, r.start, r.end) {
			return false
		}
	}

	return true
}

func inRange(ip net.IP, start, end string) bool {
	startIP := net.ParseIP(start)
	endIP := net.ParseIP(end)

	if ip4 := ip.To4(); ip4 != nil {
		start4 := startIP.To4()
		end4 := endIP.To4()

		for i := 0; i < 4; i++ {
			if ip4[i] < start4[i] || ip4[i] > end4[i] {
				return false
			}
			if ip4[i] > start4[i] && ip4[i] < end4[i] {
				return true
			}
		}
		return true
	}
	return false
}

// 通过外部服务获取真实公网IP（优先IPv4）
func getRealPublicIP() string {
	services := []string{
		"https://api.ipify.org",
		"https://ipv4.icanhazip.com",
		"https://api.ip.sb/ip",
		"https://ifconfig.me/ip",
	}

	client := &http.Client{Timeout: 5 * time.Second}

	for _, service := range services {
		resp, err := client.Get(service)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		buf := make([]byte, 128)
		n, _ := resp.Body.Read(buf)
		ip := strings.TrimSpace(string(buf[:n]))

		// 解析IP并检查是否是IPv4
		parsedIP := net.ParseIP(ip)
		if parsedIP != nil && parsedIP.To4() != nil {
			return ip
		}
	}

	return ""
}

func discoverUPnPGateway() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	clients, _, err := internetgateway2.NewWANIPConnection1Clients()
	if err != nil {
		return fmt.Errorf("无法发现 WANIPConnection1: %v", err)
	}

	if len(clients) == 0 {
		clients2, _, err := internetgateway2.NewWANIPConnection2Clients()
		if err != nil || len(clients2) == 0 {
			return fmt.Errorf("未发现任何 UPnP 网关设备")
		}
		return fmt.Errorf("仅支持 WANIPConnection1，请检查路由器配置")
	}

	upnpClient = clients[0]

	_, err = upnpClient.GetExternalIPAddressCtx(ctx)
	if err != nil {
		return fmt.Errorf("UPnP 连接测试失败: %v", err)
	}

	return nil
}

func createUPnPMapping(config *TunnelConfig) error {
	if upnpClient == nil {
		return fmt.Errorf("UPnP 客户端未初始化")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	protocol := "TCP"
	externalPort := uint16(config.ExternalPort)
	internalPort := uint16(config.LocalPort)
	description := fmt.Sprintf("LinkStart_%s", config.Name)
	leaseDuration := uint32(0)

	fmt.Printf("\n📡 正在创建 UPnP 端口映射...\n")
	fmt.Printf("   外部端口: %d\n", externalPort)
	fmt.Printf("   内部地址: %s:%d\n", localIP, internalPort)
	fmt.Printf("   协议: %s\n", protocol)

	// 先尝试删除已存在的映射
	upnpClient.DeletePortMappingCtx(ctx, "", externalPort, protocol)

	// 创建新映射
	err := upnpClient.AddPortMappingCtx(
		ctx,
		"",
		externalPort,
		protocol,
		internalPort,
		localIP,
		true,
		description,
		leaseDuration,
	)

	if err != nil {
		return fmt.Errorf("添加端口映射失败: %v", err)
	}

	// 验证映射
	verifiedInternalPort, internalClient, _, _, _, err := upnpClient.GetSpecificPortMappingEntryCtx(
		ctx, "", externalPort, protocol)

	if err != nil {
		return fmt.Errorf("验证端口映射失败: %v", err)
	}

	if internalClient != localIP {
		return fmt.Errorf("端口映射验证失败: 预期 %s，实际 %s", localIP, internalClient)
	}

	fmt.Printf("   验证成功: %s:%d\n", internalClient, verifiedInternalPort)
	fmt.Printf("✅ UPnP 端口映射创建成功!\n")

	// 更新全局变量
	publicTCPPort = int(externalPort)

	// 更新统计信息
	mu.Lock()
	if stats, ok := tunnelStats[config.Name]; ok {
		stats.UPnPEnabled = true
		stats.PublicAddr = fmt.Sprintf("%s:%d", publicIP, externalPort)
	}
	mu.Unlock()

	return nil
}

func keepUPnPMappings(tunnels []TunnelConfig) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		if upnpClient == nil {
			continue
		}

		for _, tunnel := range tunnels {
			if tunnel.Protocol == "TCP" {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

				upnpClient.AddPortMappingCtx(
					ctx,
					"",
					uint16(tunnel.ExternalPort),
					"TCP",
					uint16(tunnel.LocalPort),
					localIP,
					true,
					fmt.Sprintf("LinkStart_%s", tunnel.Name),
					0,
				)

				cancel()
			}
		}

		fmt.Printf("[%s] 🔄 UPnP 映射已刷新\n", time.Now().Format("15:04:05"))
	}
}

func initUDPConnection(port int) {
	addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		log.Fatal("解析UDP地址失败:", err)
	}

	udpConn, err = net.ListenUDP("udp4", addr)
	if err != nil {
		log.Fatal("创建UDP连接失败:", err)
	}

	fmt.Printf("本地地址: %s:%d\n", localIP, port)
}

func selectBestSTUN() {
	type result struct {
		addr   *net.UDPAddr
		delay  time.Duration
		server string
	}

	results := make(chan result, len(stunServers))
	var wg sync.WaitGroup

	for _, server := range stunServers {
		wg.Add(1)
		go func(srv string) {
			defer wg.Done()

			addr, err := net.ResolveUDPAddr("udp4", srv)
			if err != nil {
				return
			}

			start := time.Now()
			testConn, err := net.DialUDP("udp4", nil, addr)
			if err != nil {
				return
			}
			defer testConn.Close()

			message := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
			testConn.Write(message.Raw)
			testConn.SetReadDeadline(time.Now().Add(3 * time.Second))

			buf := make([]byte, 1024)
			n, err := testConn.Read(buf)
			if err != nil {
				return
			}

			response := &stun.Message{Raw: buf[:n]}
			if err := response.Decode(); err != nil {
				return
			}

			var xorAddr stun.XORMappedAddress
			if xorAddr.GetFrom(response) != nil {
				return
			}

			delay := time.Since(start)
			fmt.Printf("  ✓ %s - %dms\n", srv, delay.Milliseconds())

			results <- result{addr: addr, delay: delay, server: srv}
		}(server)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var bestDelay time.Duration = time.Hour
	for res := range results {
		if res.delay < bestDelay {
			bestDelay = res.delay
			bestSTUN = res.addr
		}
	}

	if bestSTUN == nil {
		log.Fatal("❌ 无法连接任何 STUN 服务器")
	}
	fmt.Printf("🎯 选择最快的: %s (%dms)\n", bestSTUN.String(), bestDelay.Milliseconds())
}

func getPublicAddress() {
	fmt.Println("\n正在获取公网地址...")

	// 优先使用 UPnP 获取IP（可能是内网IP）
	if upnpClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		ip, err := upnpClient.GetExternalIPAddressCtx(ctx)
		if err == nil && ip != "" {
			publicIP = ip
			fmt.Printf("✓ 通过 UPnP 获取路由器WAN口IP: %s\n", publicIP)
		}
	}

	// 使用 STUN 获取端口映射
	message := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	udpConn.WriteToUDP(message.Raw, bestSTUN)
	udpConn.SetReadDeadline(time.Now().Add(3 * time.Second))

	buf := make([]byte, 1024)
	n, _, err := udpConn.ReadFromUDP(buf)

	if err == nil && n > 0 {
		response := &stun.Message{Raw: buf[:n]}
		if response.Decode() == nil {
			var xorAddr stun.XORMappedAddress
			if xorAddr.GetFrom(response) == nil {
				if publicIP == "" {
					publicIP = xorAddr.IP.String()
				}
				publicUDPPort = xorAddr.Port
				fmt.Printf("✓ UDP 公网端口: %d\n", publicUDPPort)
			}
		}
	}

	if publicUDPPort == 0 {
		fmt.Printf("⚠️  STUN 查询失败，UDP端口未知\n")
	}

	udpConn.SetReadDeadline(time.Time{})
}

func keepNATMapping() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		message := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
		udpConn.WriteToUDP(message.Raw, bestSTUN)
	}
}

func startTunnel(config TunnelConfig) {
	mu.Lock()
	stats, ok := tunnelStats[config.Name]
	if !ok {
		// 如果统计信息不存在，创建一个
		stats = &TunnelStats{
			Protocol:   config.Protocol,
			LocalAddr:  fmt.Sprintf("%s:%d", localIP, config.LocalPort),
			CreateTime: time.Now(),
			LastActive: time.Now(),
		}
		tunnelStats[config.Name] = stats
	}

	// 更新公网地址（可能在UPnP映射时已经更新）
	if stats.PublicAddr == "" {
		stats.PublicAddr = fmt.Sprintf("%s:%d", publicIP, config.ExternalPort)
	}
	mu.Unlock()

	if config.Protocol == "TCP" {
		startTCPTunnel(config, stats)
	}
}

func startTCPTunnel(config TunnelConfig, stats *TunnelStats) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		handleWebRequest(w, r, stats)
	})

	addr := fmt.Sprintf("0.0.0.0:%d", config.LocalPort)
	fmt.Printf("[TCP隧道] %s 已启动: %s\n", config.Name, addr)

	go func() {
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Printf("❌ HTTP服务器启动失败: %v\n", err)
		}
	}()
}

func handleWebRequest(w http.ResponseWriter, r *http.Request, stats *TunnelStats) {
	clientIP := r.RemoteAddr

	mu.Lock()
	stats.Connections++
	stats.LastActive = time.Now()
	mu.Unlock()

	fmt.Printf("[%s] 🌐 访问: %s %s\n",
		time.Now().Format("15:04:05"), clientIP, r.URL.Path)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	isPublic := !strings.HasPrefix(clientIP, "10.") &&
		!strings.HasPrefix(clientIP, "192.168.") &&
		!strings.HasPrefix(clientIP, "172.")

	html := generateWebPage(clientIP, isPublic, stats)
	fmt.Fprint(w, html)
}

func generateWebPage(clientIP string, isPublic bool, stats *TunnelStats) string {
	accessType := "🏠 本地网络访问"
	accessColor := "#ffc107"
	statusEmoji := "⚠️"

	if isPublic {
		accessType = "🌍 公网访问成功！"
		accessColor = "#38ef7d"
		statusEmoji = "✅"
	}

	uptime := time.Since(stats.CreateTime)
	upnpStatus := "❌ 未启用"
	if stats.UPnPEnabled {
		upnpStatus = "✅ 已启用"
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>内网穿透服务器 - UPnP</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
            background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%);
            min-height: 100vh;
            padding: 20px;
        }
        .container { max-width: 1200px; margin: 0 auto; }
        .card {
            background: white;
            border-radius: 20px;
            padding: 40px;
            box-shadow: 0 20px 60px rgba(0,0,0,0.4);
            margin-bottom: 20px;
            animation: slideUp 0.5s ease;
        }
        @keyframes slideUp {
            from { opacity: 0; transform: translateY(30px); }
            to { opacity: 1; transform: translateY(0); }
        }
        .header {
            background: linear-gradient(135deg, #11998e 0%%, #38ef7d 100%%);
            color: white;
            padding: 40px;
            border-radius: 15px;
            text-align: center;
            margin-bottom: 30px;
        }
        .header h1 { font-size: 36px; margin-bottom: 10px; }
        .status-badge {
            display: inline-block;
            padding: 12px 24px;
            background: %s;
            border-radius: 25px;
            margin-top: 15px;
            font-weight: bold;
            font-size: 18px;
        }
        .info-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
            gap: 20px;
            margin: 20px 0;
        }
        .info-box {
            background: linear-gradient(135deg, #f8f9fa 0%%, #e9ecef 100%%);
            padding: 25px;
            border-radius: 12px;
            border-left: 5px solid #667eea;
            transition: transform 0.3s ease;
        }
        .info-box:hover { transform: translateY(-5px); }
        .info-label {
            font-size: 12px;
            color: #666;
            text-transform: uppercase;
            margin-bottom: 10px;
            font-weight: 600;
        }
        .info-value {
            font-size: 20px;
            font-weight: bold;
            color: #333;
            font-family: 'Monaco', monospace;
            word-break: break-all;
        }
        .copy-btn {
            background: #667eea;
            color: white;
            border: none;
            padding: 10px 20px;
            border-radius: 8px;
            cursor: pointer;
            margin-top: 10px;
            font-size: 14px;
            transition: background 0.3s;
        }
        .copy-btn:hover { background: #5568d3; }
        .tech-box {
            background: linear-gradient(135deg, #e3f2fd 0%%, #bbdefb 100%%);
            border-left: 5px solid #2196f3;
            padding: 25px;
            border-radius: 12px;
            margin: 20px 0;
        }
        .tech-box h3 {
            color: #1565c0;
            margin-bottom: 15px;
            font-size: 20px;
        }
        .tech-box ul {
            color: #1565c0;
            padding-left: 25px;
            line-height: 1.8;
        }
    </style>
</head>
<body>
    <div class="container">
        <div class="card">
            <div class="header">
                <h1>%s 内网穿透成功</h1>
                <p>通过 UPnP 自动端口映射 + STUN 协议</p>
                <div class="status-badge">%s</div>
            </div>

            <h2 style="margin-bottom: 20px;">📊 连接信息</h2>
            <div class="info-grid">
                <div class="info-box">
                    <div class="info-label">🌍 公网访问地址</div>
                    <div class="info-value" style="color: #667eea;">%s</div>
                    <button class="copy-btn" onclick="navigator.clipboard.writeText('http://%s')">复制链接</button>
                </div>
                
                <div class="info-box">
                    <div class="info-label">🏠 内网地址</div>
                    <div class="info-value">%s</div>
                </div>
                
                <div class="info-box">
                    <div class="info-label">🔧 UPnP 状态</div>
                    <div class="info-value">%s</div>
                </div>
                
                <div class="info-box">
                    <div class="info-label">👥 总连接数</div>
                    <div class="info-value">%d</div>
                </div>
                
                <div class="info-box">
                    <div class="info-label">⏱️ 运行时间</div>
                    <div class="info-value">%s</div>
                </div>
                
                <div class="info-box">
                    <div class="info-label">🌐 你的 IP</div>
                    <div class="info-value">%s</div>
                </div>
            </div>

            <div class="tech-box">
                <h3>🎉 技术实现</h3>
                <ul>
                    <li><strong>UPnP 自动端口映射</strong>：无需手动配置路由器，自动创建端口转发规则</li>
                    <li><strong>STUN 协议</strong>：获取公网 IP 地址和 NAT 类型</li>
                    <li><strong>自动保活</strong>：定期刷新 UPnP 映射，保持端口开放</li>
                    <li><strong>智能端口选择</strong>：冲突时自动尝试其他可用端口</li>
                </ul>
            </div>
        </div>
    </div>

    <script>
        setTimeout(() => location.reload(), 30000);
    </script>
</body>
</html>`,
		accessColor,
		statusEmoji,
		accessType,
		stats.PublicAddr, stats.PublicAddr,
		stats.LocalAddr,
		upnpStatus,
		stats.Connections,
		formatDuration(uptime),
		clientIP)
}

func formatDuration(d time.Duration) string {
	hours := int(d.Hours())
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60
	return fmt.Sprintf("%d小时%d分%d秒", hours, minutes, seconds)
}

func displayTunnelInfo(tunnels []TunnelConfig) {
	fmt.Println("\n✅ 服务已启动!")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("📊 隧道列表:")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	for _, tunnel := range tunnels {
		mu.Lock()
		stats, ok := tunnelStats[tunnel.Name]
		mu.Unlock()

		if !ok || stats == nil {
			fmt.Printf("\n规则名称: %s\n", tunnel.Name)
			fmt.Printf("状态: 初始化中...\n")
			continue
		}

		upnpStatus := "❌"
		if stats.UPnPEnabled {
			upnpStatus = "✅"
		}

		fmt.Printf("\n规则名称: %s\n", tunnel.Name)
		fmt.Printf("协议: %s\n", tunnel.Protocol)
		fmt.Printf("公网地址: http://%s\n", stats.PublicAddr)
		fmt.Printf("内网转发: %s\n", stats.LocalAddr)
		fmt.Printf("UPnP 状态: %s\n", upnpStatus)
		fmt.Printf("状态: 运行中 ✓\n")
	}

	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("💡 访问方式:")
	fmt.Printf("   本地: http://%s:%d\n", localIP, tunnels[0].LocalPort)
	if publicTCPPort > 0 {
		fmt.Printf("   公网: http://%s:%d ✅ UPnP已启用\n", publicIP, publicTCPPort)
	} else {
		fmt.Printf("   公网: 需要手动配置端口转发\n")
	}

	if !isPublicIP(publicIP) {
		fmt.Println("\n⚠️  注意: 检测到多层NAT，可能无法从外网访问")
		fmt.Printf("   路由器WAN口IP: %s (内网IP)\n", publicIP)
	}

	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
}

func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "unknown"
	}
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				return ipNet.IP.String()
			}
		}
	}
	return "unknown"
}
