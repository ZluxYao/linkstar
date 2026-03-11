package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/huin/goupnp/dcps/internetgateway2"
	"github.com/libp2p/go-reuseport"
	"github.com/pion/stun"
	"golang.org/x/net/proxy"
)

// ============ 配置 ============
const (
	LocalServicePort = 3333      // 本地实际服务端口
	Socks5Addr       = "127.0.0.1:3338" // EasyTier SOCKS5 代理地址
	RouterEasyTierIP = "10.126.126.1"   // 软路由的 EasyTier IP
	LocalEasyTierIP  = "10.126.126.2"   // 本机 EasyTier IP
)

var (
	// TCP STUN 服务器列表
	stunServers = []string{
		"stun.zentauron.de:3478",
		"stun.bethesda.net:3478",
		"stun.frozenmountain.com:3478",
		"stun.telnyx.com:3478",
	}

	publicIP       string        // 真实公网IP
	routerWanIP    string        // 路由器WAN口IP
	stunMappedPort int           // 运营商NAT分配的端口
	localIP        string        // 本机内网IP
	stunLocalPort  int           // STUN使用的本地端口
	bestSTUN       string        // 最快的STUN服务器
	stunConn       net.Conn      // STUN连接
	upnpClients    []upnpGateway // UPnP客户端
	isDoubleNAT    bool          // 是否双层NAT
	natType        string        // NAT类型
	socks5Dialer   proxy.Dialer  // SOCKS5 代理 dialer
)

// upnpGateway 统一抽象四种 UPnP 客户端类型
type upnpGateway interface {
	AddPortMapping(NewRemoteHost string, NewExternalPort uint16, NewProtocol string, NewInternalPort uint16, NewInternalClient string, NewEnabled bool, NewPortMappingDescription string, NewLeaseDuration uint32) error
	DeletePortMapping(NewRemoteHost string, NewExternalPort uint16, NewProtocol string) error
	GetExternalIPAddress() (NewExternalIPAddress string, err error)
}

func main() {
	fmt.Println("╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║   双层NAT穿透 - 让公网访问本地3333端口服务               ║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝\n")

	var err error
	localIP, err = GetLocalIP()
	if err != nil {
		log.Fatalf("❌ 获取本机IP失败: %v\n", err)
	}
	fmt.Printf("✅ 本机内网IP: %s\n\n", localIP)

	// 初始化 SOCKS5 代理
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("🔌 步骤 0: 初始化 SOCKS5 代理 (EasyTier)")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	socks5Dialer, err = proxy.SOCKS5("tcp", Socks5Addr, nil, proxy.Direct)
	if err != nil {
		log.Printf("⚠️  SOCKS5 代理连接失败: %v\n", err)
		fmt.Println("   将使用直连模式")
	} else {
		fmt.Printf("✅ SOCKS5 代理已连接: %s\n", Socks5Addr)
	}

	// 步骤 1: 启动本地 HTTP 服务
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("📡 步骤 1/5: 启动本地 HTTP 服务")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	startLocalHTTPService(LocalServicePort)

	// 步骤 2: 选择最快 STUN 服务器
	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("🔍 步骤 2/5: 测试并选择最快的 STUN 服务器")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	selectBestSTUN()

	// 步骤 3: TCP STUN 获取运营商 NAT 映射并启动监听
	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("🌐 步骤 3/5: 通过 STUN 获取公网映射并启动端口复用监听")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	if !getPublicMappingAndListen() {
		log.Fatal("❌ 获取公网映射失败")
	}

	// 步骤 4: 检测 NAT 类型
	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("🔍 步骤 4/5: 检测 NAT 类型")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	detectNATType()

	// 步骤 5: UPnP 配置路由器端口映射
	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("🔧 步骤 5/5: 配置路由器 UPnP 端口映射")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	setupRouterMapping()

	// 启动保活服务
	go keepAlive()

	// 显示最终结果
	displayResult()

	// 保持运行
	select {}
}

// ========== 步骤 1: 启动本地 HTTP 服务 ==========
func startLocalHTTPService(port int) {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

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
			max-width: 800px;
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
			line-height: 1.8;
			font-size: 0.9em;
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
		@media (max-width: 600px) {
			h1 { font-size: 1.8em; }
			.container { padding: 20px; }
		}
	</style>
</head>
<body>
	<div class="container">
		<h1>🎉 双层NAT穿透成功！</h1>
		
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
			<h3>🔄 数据传输完整链路</h3>
			<div class="flow-chart">
外网用户访问
   ↓
🌍 公网IP: %s:%d
   ↓ (运营商NAT转换)
🏢 路由器WAN: %s:%d
   ↓ (路由器UPnP转发)
🏠 本机端口: %s:%d
   ↓ (程序内部转发)
💻 HTTP服务: localhost:%d
			</div>
		</div>

		<div class="section">
			<h3>🔧 技术实现原理</h3>
			<div class="tech-item">
				<strong>1. STUN探测:</strong> 通过TCP连接STUN服务器，发现运营商NAT分配的公网端口 <span class="highlight">%d</span>
			</div>
			<div class="tech-item">
				<strong>2. 端口复用:</strong> 使用SO_REUSEPORT在同一端口 <span class="highlight">%d</span> 上同时拨号和监听
			</div>
			<div class="tech-item">
				<strong>3. UPnP自动配置:</strong> 路由器自动创建映射 %d → %s:%d
			</div>
			<div class="tech-item">
				<strong>4. 端口转发:</strong> 将 %d 端口的流量转发到实际服务端口 %d
			</div>
			<div class="tech-item">
				<strong>5. 连接保活:</strong> 每15秒发送心跳包，维持NAT映射不失效
			</div>
		</div>

		<div class="section">
			<h3>📊 关键信息</h3>
			<div class="tech-item">
				✅ 外网访问地址: <strong>http://%s:%d</strong>
			</div>
			<div class="tech-item">
				✅ 双层NAT已成功穿透
			</div>
			<div class="tech-item">
				✅ 端口映射自动维护中
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
			func() string {
				if isDoubleNAT {
					return "双层NAT (运营商+家庭路由器)"
				}
				return "单层NAT"
			}(),
			natType,
			publicIP, stunMappedPort,
			routerWanIP, stunMappedPort,
			localIP, stunLocalPort,
			LocalServicePort,
			stunMappedPort,
			stunLocalPort,
			stunMappedPort, localIP, stunLocalPort,
			stunLocalPort, LocalServicePort,
			publicIP, stunMappedPort,
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

// ========== 步骤 2: 选择最快的 STUN 服务器 ==========
func selectBestSTUN() {
	type result struct {
		server string
		delay  time.Duration
	}

	results := make(chan result, len(stunServers))
	var wg sync.WaitGroup

	fmt.Println("正在测试 STUN 服务器响应速度...")
	for _, server := range stunServers {
		wg.Add(1)
		go func(srv string) {
			defer wg.Done()

			start := time.Now()
			var conn net.Conn
			var err error
			if socks5Dialer != nil {
				// 使用 SOCKS5 代理
				conn, err = socks5Dialer.Dial("tcp", srv)
			} else {
				// 直连
				conn, err = net.DialTimeout("tcp", srv, 3*time.Second)
			}
			if err != nil {
				return
			}
			defer conn.Close()

			msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
			_, err = conn.Write(msg.Raw)
			if err != nil {
				return
			}

			conn.SetReadDeadline(time.Now().Add(3 * time.Second))
			buf := make([]byte, 1024)
			n, err := conn.Read(buf)
			if err != nil || n == 0 {
				return
			}

			delay := time.Since(start)
			fmt.Printf("  ✓ %s - %dms\n", srv, delay.Milliseconds())
			results <- result{server: srv, delay: delay}
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
			bestSTUN = res.server
		}
	}

	if bestSTUN == "" {
		log.Fatal("❌ 无法连接任何 STUN 服务器")
	}
	fmt.Printf("\n🎯 选择最快服务器: %s (%dms)\n", bestSTUN, bestDelay.Milliseconds())
}

// ========== 步骤 3: 获取公网映射并启动监听 (修复版) ==========
func getPublicMappingAndListen() bool {
	fmt.Println("📡 正在通过 STUN 获取公网映射...")

	var conn net.Conn
	var err error

	if socks5Dialer != nil {
		// 使用 SOCKS5 代理
		fmt.Printf("   使用 SOCKS5 代理: %s\n", Socks5Addr)
		conn, err = socks5Dialer.Dial("tcp", bestSTUN)
		if err != nil {
			log.Printf("❌ 通过 SOCKS5 连接 STUN 服务器失败: %v\n", err)
			return false
		}
	} else {
		// 直连模式（端口复用）
		localAddr := fmt.Sprintf("%s:0", localIP)
		conn, err = reuseport.Dial("tcp", localAddr, bestSTUN)
		if err != nil {
			log.Printf("❌ 连接 STUN 服务器失败: %v\n", err)
			return false
		}
	}

	stunConn = conn
	if tcpAddr, ok := conn.LocalAddr().(*net.TCPAddr); ok {
		stunLocalPort = tcpAddr.Port
	} else {
		stunLocalPort = 0
	}

	fmt.Printf("✅ TCP 连接建立成功\n")
	fmt.Printf("   本地端口: %d\n", stunLocalPort)

	// 发送 STUN 请求
	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	_, err = stunConn.Write(msg.Raw)
	if err != nil {
		log.Printf("❌ 发送 STUN 请求失败: %v\n", err)
		return false
	}

	// 读取响应
	stunConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1024)
	n, err := stunConn.Read(buf)
	stunConn.SetReadDeadline(time.Time{})

	if err != nil || n == 0 {
		log.Printf("❌ 读取 STUN 响应失败: %v\n", err)
		return false
	}

	// 解析响应
	response := &stun.Message{Raw: buf[:n]}
	if err := response.Decode(); err != nil {
		log.Printf("❌ 解析 STUN 响应失败: %v\n", err)
		return false
	}

	var xorAddr stun.XORMappedAddress
	if err := xorAddr.GetFrom(response); err != nil {
		log.Printf("❌ 获取映射地址失败: %v\n", err)
		return false
	}

	publicIP = xorAddr.IP.String()
	stunMappedPort = xorAddr.Port

	fmt.Printf("✅ 运营商 NAT 映射获取成功！\n")
	fmt.Printf("   🌍 公网IP: %s\n", publicIP)
	fmt.Printf("   🔑 公网端口: %d\n", stunMappedPort)

	// 第二步：创建监听器
	listenIP := localIP
	listenPort := stunLocalPort

	if socks5Dialer != nil {
		// SOCKS5 模式：需要监听在 EasyTier IP 上
		listenIP = LocalEasyTierIP
		fmt.Printf("\n📡 正在端口 %s:%d 上创建监听器（SOCKS5模式）...\n", listenIP, listenPort)
	} else {
		fmt.Printf("\n📡 正在端口 %s:%d 上创建监听器（SO_REUSEPORT）...\n", listenIP, listenPort)
	}

	listener, err := reuseport.Listen("tcp", fmt.Sprintf("%s:%d", listenIP, listenPort))
	if err != nil {
		log.Printf("⚠️  无法在端口 %d 上监听: %v\n", listenPort, err)
		log.Printf("   STUN 映射已获取，但端口复用失败\n")
		return true // STUN 映射成功，继续执行
	}

	fmt.Printf("✅ 监听器已启动，端口 %d\n", listenPort)
	fmt.Printf("   所有到达的流量将转发到本地 %d 端口\n", LocalServicePort)

	// 启动监听服务，接受连接并转发
	go func() {
		for {
			clientConn, err := listener.Accept()
			if err != nil {
				log.Printf("⚠️  接受连接失败: %v\n", err)
				continue
			}
			go handleForward(clientConn, LocalServicePort)
		}
	}()

	time.Sleep(500 * time.Millisecond)
	return true
}

// ========== 端口转发处理 ==========
func handleForward(clientConn net.Conn, targetPort int) {
	defer clientConn.Close()

	targetConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", targetPort))
	if err != nil {
		log.Printf("⚠️  连接目标端口 %d 失败: %v\n", targetPort, err)
		return
	}
	defer targetConn.Close()

	log.Printf("🔀 [转发开始] %s → localhost:%d\n", clientConn.RemoteAddr(), targetPort)

	// 双向转发
	done := make(chan struct{}, 2)

	// 客户端 → 目标服务
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, err := clientConn.Read(buf)
			if err != nil {
				return
			}
			if _, err := targetConn.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	// 目标服务 → 客户端
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, err := targetConn.Read(buf)
			if err != nil {
				return
			}
			if _, err := clientConn.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	<-done
	log.Printf("🔀 [转发完成] %s → localhost:%d\n", clientConn.RemoteAddr(), targetPort)
}

// ========== 步骤 4: 检测 NAT 类型 ==========
func detectNATType() {
	var conn net.Conn
	var err error

	if socks5Dialer != nil {
		conn, err = socks5Dialer.Dial("tcp", bestSTUN)
	} else {
		localAddr := fmt.Sprintf("%s:0", localIP)
		conn, err = reuseport.Dial("tcp", localAddr, bestSTUN)
	}
	if err != nil {
		natType = "检测失败"
		fmt.Println("⚠️  NAT 类型检测失败")
		return
	}
	defer conn.Close()

	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	conn.Write(msg.Raw)

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1024)
	n, _ := conn.Read(buf)

	if n == 0 {
		natType = "检测失败"
		return
	}

	response := &stun.Message{Raw: buf[:n]}
	response.Decode()

	var xorAddr stun.XORMappedAddress
	xorAddr.GetFrom(response)

	if xorAddr.Port == stunMappedPort {
		natType = "Endpoint-Independent (完美)"
		fmt.Println("✅ NAT类型: Endpoint-Independent Mapping")
		fmt.Println("   不同目标使用相同公网端口，最适合穿透！")
	} else {
		natType = "Address-Dependent (一般)"
		fmt.Println("⚠️  NAT类型: Address-Dependent Mapping")
		fmt.Println("   不同目标使用不同端口，需要保持连接活跃")
	}
}

// ========== 步骤 5: 配置路由器 UPnP 映射 ==========
func setupRouterMapping() {
	fmt.Println("🔍 正在发现 UPnP 网关设备...")

	upnpClients = nil

	if c, _, err := internetgateway1.NewWANIPConnection1Clients(); err == nil {
		for _, v := range c {
			upnpClients = append(upnpClients, v)
		}
	}
	if c, _, err := internetgateway1.NewWANPPPConnection1Clients(); err == nil {
		for _, v := range c {
			upnpClients = append(upnpClients, v)
		}
	}
	if c, _, err := internetgateway2.NewWANIPConnection1Clients(); err == nil {
		for _, v := range c {
			upnpClients = append(upnpClients, v)
		}
	}
	if c, _, err := internetgateway2.NewWANPPPConnection1Clients(); err == nil {
		for _, v := range c {
			upnpClients = append(upnpClients, v)
		}
	}

	if len(upnpClients) == 0 {
		log.Println("❌ UPnP 发现失败: 未找到任何网关设备")
		log.Println("💡 请手动在路由器配置端口转发:")
		log.Printf("   外部端口: %d → 内网IP: %s 内网端口: %d\n",
			stunLocalPort, localIP, stunLocalPort)
		return
	}
	fmt.Printf("✅ 发现 %d 个 UPnP 网关设备\n", len(upnpClients))

	// 获取路由器外部IP
	externalIP, err := upnpClients[0].GetExternalIPAddress()
	if err != nil {
		log.Printf("❌ 获取路由器外部IP失败: %v\n", err)
		return
	}
	routerWanIP = externalIP
	fmt.Printf("✅ 路由器WAN口IP: %s\n", routerWanIP)

	// 检测是否双层NAT
	if routerWanIP != publicIP {
		isDoubleNAT = true
		fmt.Println("\n🎯 ========== 检测到双层 NAT 环境 ==========")
		fmt.Printf("   真实公网IP:      %s (运营商分配)\n", publicIP)
		fmt.Printf("   路由器WAN口IP:   %s (运营商内网)\n", routerWanIP)
		fmt.Printf("   运营商NAT端口:   %d (外网访问)\n", stunMappedPort)
		fmt.Printf("   STUN本地端口:    %d (NAT实际映射)\n", stunLocalPort)
		fmt.Printf("   本机内网IP:      %s\n", localIP)
		fmt.Println("==========================================\n")
	} else {
		fmt.Println("✅ 单层NAT环境（路由器直接获得公网IP）")
	}

	// 配置 UPnP 端口映射
	fmt.Printf("📡 正在配置 UPnP 映射: 外部 %d → 内网 %s:%d\n",
		stunLocalPort, localIP, stunLocalPort)

	// 先删除可能存在的旧映射
	for _, client := range upnpClients {
		client.DeletePortMapping("", uint16(stunLocalPort), "TCP")
	}

	// 添加新映射
	// 确定 UPnP 映射的目标 IP
	targetIP := localIP
	if socks5Dialer != nil {
		// 使用 SOCKS5 代理时，映射到本机的 EasyTier IP
		targetIP = LocalEasyTierIP
		fmt.Printf("   📍 使用 SOCKS5 模式，UPnP 映射到: %s\n", targetIP)
	}

	success := false
	for i, client := range upnpClients {
		err := client.AddPortMapping(
			"",                    // NewRemoteHost
			uint16(stunLocalPort), // NewExternalPort
			"TCP",                 // NewProtocol
			uint16(stunLocalPort), // NewInternalPort
			targetIP,              // NewInternalClient
			true,                  // NewEnabled
			"NAT-Traversal",       // NewPortMappingDescription
			uint32(0),             // NewLeaseDuration (0=永久)
		)

		if err == nil {
			fmt.Printf("   ✓ 客户端 %d 映射成功\n", i+1)
			success = true
			break
		}

		fmt.Printf("   ✗ 客户端 %d 失败: %v\n", i+1, err)
	}

	if success {
		fmt.Println("✅ UPnP 端口映射配置成功！")
	} else {
		fmt.Println("❌ UPnP 端口映射失败")
		fmt.Println("💡 请手动配置路由器端口转发:")
		fmt.Printf("   外部端口: %d → 内网IP: %s 内网端口: %d\n",
			stunLocalPort, localIP, stunLocalPort)
	}
}

// ========== 连接保活 (修复版) ==========
func keepAlive() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	maxRetries := 3
	retryCount := 0

	for range ticker.C {
		if stunConn == nil {
			// 重新连接 STUN 服务器
			for retryCount < maxRetries {
				var conn net.Conn
				var err error

				if socks5Dialer != nil {
					conn, err = socks5Dialer.Dial("tcp", bestSTUN)
				} else {
					localAddr := fmt.Sprintf("%s:%d", localIP, stunLocalPort)
					conn, err = reuseport.Dial("tcp", localAddr, bestSTUN)
				}

				if err != nil {
					retryCount++
					log.Printf("⚠️  重连失败 (%d/%d): %v\n", retryCount, maxRetries, err)
					time.Sleep(2 * time.Second)
					continue
				}

				stunConn = conn
				retryCount = 0
				log.Printf("🔄 TCP STUN 连接已重建\n")
				break
			}

			if retryCount >= maxRetries {
				log.Println("❌ 多次重连失败，尝试切换 STUN 服务器...")
				selectBestSTUN()
				retryCount = 0
				continue
			}
		}

		// 发送心跳包
		msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
		_, err := stunConn.Write(msg.Raw)
		if err != nil {
			log.Printf("⚠️  心跳发送失败: %v\n", err)
			stunConn.Close()
			stunConn = nil
			continue
		}

		// 读取响应
		stunConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 1024)
		n, err := stunConn.Read(buf)
		stunConn.SetReadDeadline(time.Time{})

		if err != nil || n == 0 {
			log.Printf("⚠️  心跳响应失败: %v\n", err)
			stunConn.Close()
			stunConn = nil
			continue
		}

		// 验证端口是否变化
		response := &stun.Message{Raw: buf[:n]}
		if response.Decode() == nil {
			var xorAddr stun.XORMappedAddress
			if xorAddr.GetFrom(response) == nil {
				currentPort := xorAddr.Port
				if currentPort != stunMappedPort {
					log.Printf("⚠️  警告: 运营商NAT端口已变化 %d → %d\n", stunMappedPort, currentPort)
					log.Println("   需要重新配置穿透...")
					stunMappedPort = currentPort

					// 重新配置UPnP
					if len(upnpClients) > 0 {
						setupRouterMapping()
					}
				}
			}
		}

		log.Printf("🔄 保活成功 (公网:%s:%d | 路由器:%s:%d | 本机:%s:%d→%d)\n",
			publicIP, stunMappedPort,
			routerWanIP, stunMappedPort,
			localIP, stunLocalPort, LocalServicePort)
	}
}

// ========== 显示最终结果 ==========
func displayResult() {
	fmt.Println("\n╔══════════════════════════════════════════════════════════╗")
	fmt.Println("║                    🎉 穿透配置完成！                      ║")
	fmt.Println("╚══════════════════════════════════════════════════════════╝\n")

	if isDoubleNAT {
		fmt.Println("📊 双层 NAT 环境配置")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Printf("🌍 真实公网IP:        %s\n", publicIP)
		fmt.Printf("🔑 公网端口:          %d ⬅ 运营商NAT分配\n", stunMappedPort)
		fmt.Printf("🏢 路由器WAN口IP:     %s\n", routerWanIP)
		fmt.Printf("🔧 路由器映射端口:    %d ⬅ UPnP自动配置\n", stunLocalPort)
		fmt.Printf("🏠 本机内网IP:        %s\n", localIP)
		fmt.Printf("📡 端口转发:          %d → %d\n", stunLocalPort, LocalServicePort)

		fmt.Println("\n🔄 完整数据流路径:")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Printf("   互联网用户\n")
		fmt.Printf("        ↓\n")
		fmt.Printf("   %s:%d (运营商NAT)\n", publicIP, stunMappedPort)
		fmt.Printf("        ↓ [第一层NAT转换]\n")
		fmt.Printf("   %s:%d (路由器WAN)\n", routerWanIP, stunMappedPort)
		fmt.Printf("        ↓ [第二层NAT转换 - UPnP]\n")
		fmt.Printf("   %s:%d (端口复用监听)\n", localIP, stunLocalPort)
		fmt.Printf("        ↓ [程序内部转发]\n")
		fmt.Printf("   localhost:%d (HTTP服务)\n", LocalServicePort)

		fmt.Println("\n✅ 穿透状态: 成功")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Printf("🌐 外网访问地址: http://%s:%d\n", publicIP, stunMappedPort)
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	} else {
		fmt.Println("📊 单层 NAT 环境配置")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Printf("🌍 公网IP:            %s\n", publicIP)
		fmt.Printf("🔑 映射端口:          %d\n", stunMappedPort)
		fmt.Printf("🏠 本机内网IP:        %s\n", localIP)
		fmt.Printf("📡 端口转发:          %d → %d\n", stunLocalPort, LocalServicePort)

		fmt.Println("\n✅ 穿透状态: 成功")
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
		fmt.Printf("🌐 外网访问地址: http://%s:%d\n", publicIP, stunMappedPort)
		fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	}

	fmt.Println("\n💡 技术说明")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("• TCP STUN 探测: 发现运营商NAT端口 %d\n", stunMappedPort)
	fmt.Printf("• 端口复用: 使用SO_REUSEPORT在端口 %d 上同时拨号和监听\n", stunLocalPort)
	fmt.Printf("• UPnP 自动配置: 路由器映射 %d → %s:%d\n",
		stunLocalPort, localIP, stunLocalPort)
	fmt.Printf("• 端口转发: %d 的流量转发到服务端口 %d\n", stunLocalPort, LocalServicePort)
	fmt.Println("• 连接保活: 每15秒发送心跳维持NAT映射")
	fmt.Println("• NAT类型:", natType)

	fmt.Println("\n⚠️  注意事项")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("✓ 保持程序运行以维持穿透状态")
	fmt.Println("✓ 公网IP变化后需要重新运行")
	fmt.Println("✓ 路由器重启后需要重新运行")
	fmt.Println("✓ 确保路由器已开启UPnP功能")
	fmt.Println("✓ 本地3333端口服务必须保持运行")

	fmt.Println("\n🔧 维护命令")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("• 测试本地服务: curl http://localhost:3333")
	fmt.Printf("• 测试外网访问: curl http://%s:%d\n", publicIP, stunMappedPort)
	fmt.Println("• 查看路由器配置: 登录路由器管理页面查看端口转发")

	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("程序正在运行中，按 Ctrl+C 退出...")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━\n")
}

// ========== 辅助函数 ==========

// 获取本机内网IP
func GetLocalIP() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}

	for _, iface := range interfaces {
		// 过滤掉未启动的网卡
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		// 过滤虚拟网卡
		name := iface.Name
		if strings.HasPrefix(name, "docker") ||
			strings.HasPrefix(name, "br-") ||
			strings.HasPrefix(name, "veth") ||
			strings.HasPrefix(name, "lo") ||
			strings.HasPrefix(name, "virbr") {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() { //转换为IPNet类型，同时去除回环地址
				if ipnet.IP.To4() != nil {
					return ipnet.IP.String(), nil
				}
			}
		}
	}
	return "", fmt.Errorf("未找到本机IP地址")
}
