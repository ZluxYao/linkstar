package stun

import (
	"crypto/tls"
	"fmt"
	"linkstar/global"
	"linkstar/modules/stun/model"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/libp2p/go-reuseport"
	"github.com/pion/stun"
	"github.com/sirupsen/logrus"
)

// 启动STUN服务 StarStun
func StarStun(devices []model.Device) error {

	errChan := make(chan error, 1)
	activeServices := 0

	for _, device := range devices {
		for _, service := range device.Services {

			// 未启用
			if !service.Enabled {
				logrus.Infof("[%s - %s] 服务未启用，跳过", device.Name, service.Name)
				continue
			}

			activeServices++

			// 为每个服务创建单独进程
			go func(device model.Device, service *model.Service) {
				maxRetries := 5
				attempt := 0

				for {
					attempt++
					logrus.Infof("[%s - %s] 启动服务 (第 %d 次)", device.Name, service.Name, attempt)

					err := RunStunTunnel(device.IP, service)

					if err != nil {
						logrus.Errorf("❌ [%s - %s] STUN 穿透失败 (第 %d/%d 次): %v", device.Name, service.Name, attempt, maxRetries, err)

						// 如果之前启动成功过(进入过健康检查),则重置计数
						if service.StartupSuccess {
							attempt = 0
							service.StartupSuccess = false // 重置标志
						}

						// 达到最大重试次数
						if attempt >= maxRetries {
							service.Enabled = false
							logrus.Errorf("[%s - %s] 达到最大重试次数,关闭服务", device.Name, service.Name)
							select {
							case errChan <- fmt.Errorf("[%s - %s] 达到最大重试次数: %w", device.Name, service.Name, err):
							default:
							}
							return
						}

						// 等待一段时间再重试
						time.Sleep(time.Second * 1)
						continue
					}
				}
			}(device, &service)
		}
	}

	if activeServices == 0 {
		return fmt.Errorf("没有启用的服务需要穿透")
	}

	logrus.Infof("✅ 已启动 %d 个服务的 STUN 穿透", activeServices)

	// 阻塞等待第一个严重错误（如果所有服务都正常运行，会一直阻塞）
	return <-errChan
}

// 实现stun内网穿透逻辑
func RunStunTunnel(targetIP string, service *model.Service) error {
	protocol := strings.ToLower(service.Protocol) // 转为小写
	// ssh 是应用层协议，底层走 tcp
	if protocol == "ssh" {
		protocol = "tcp"
	}
	localAddr := fmt.Sprintf("%s:0", global.StunConfig.LocalIP)

	// 1.STUN拨号
	stunConn, err := reuseport.Dial(protocol, localAddr, global.StunConfig.BestSTUN) // 使用reuseport SO_REUSEPORT 可以复用端口
	if err != nil {
		return fmt.Errorf("STUN拨号失败 [%s]:%w", global.StunConfig.BestSTUN, err)
	}

	var localPort uint16
	if protocol == "tcp" {
		localPort = uint16(stunConn.LocalAddr().(*net.TCPAddr).Port)
	} else {
		localPort = uint16(stunConn.LocalAddr().(*net.UDPAddr).Port)
	}

	// 2.STUN 握手
	var publicIP string
	var publicPort int
	if protocol == "tcp" {
		publicIP, publicPort, err = doTcpStunHandshake(stunConn)
	} else {
		udpConn := stunConn.(*net.UDPConn)
		stunServerAddr, _ := net.ResolveUDPAddr("udp", global.StunConfig.BestSTUN)
		publicIP, publicPort, err = doUDPStunHandshake(udpConn, stunServerAddr)
	}
	if err != nil {
		return fmt.Errorf("与STUN服务器握手失败:%w", err)
	}

	// 3.端口复用监听
	listenAddr := fmt.Sprintf("%s:%d", global.StunConfig.LocalIP, localPort)
	listener, err := reuseport.Listen(protocol, listenAddr)
	if err != nil {
		stunConn.Close()
		return fmt.Errorf("端口监听失败：%w", err)
	}

	// 4.配置路由器UPnp
	go func() {
		description := fmt.Sprintf("LinkStar-%s", service.Name)
		err := AddPortMapping(localPort, localPort, "TCP", description)
		if err != nil {
			logrus.Warnf("[%s] UPnP 映射失败 (非致命): %v", service.Name, err)
		} else {
			logrus.Infof("[%s] UPnP 映射成功: 路由器 WAN:%d -> 本机:%d", service.Name, localPort, localPort)
		}
	}()

	defer func() {
		logrus.Infof("[%s]正在清理资源... ", service.Name)
		stunConn.Close()
		listener.Close()
		go DeletePortMapping(localPort, protocol)
	}()

	// 5. 启动健康检测
	errCh := make(chan error, 3) // 创建错误通道
	logrus.Infof("%v %v %v", localPort, publicIP, publicPort)

	var publicURL string
	if service.Tlss {
		publicURL = fmt.Sprintf("https://%s:%d", publicIP, publicPort)
	} else {
		publicURL = fmt.Sprintf("http://%s:%d", publicIP, publicPort)
	}

	if protocol == "tcp" {
		go func() {
			service.StartupSuccess = true
			err = tcpStunHealthCheck(stunConn, publicURL, publicIP, publicPort, localPort, service)
			if err != nil {
				errCh <- fmt.Errorf("TCP健康检查失败: %w", err)
			}
		}()
	} else {
		go func() {
			service.StartupSuccess = true
			udpConn := stunConn.(*net.UDPConn)
			stunServerAddr, _ := net.ResolveUDPAddr("udp", global.StunConfig.BestSTUN)
			err = udpStunHealthCheck(udpConn, stunServerAddr, publicPort, localPort)
			if err != nil {
				errCh <- fmt.Errorf("UDP健康检查失败: %w", err)
			}
		}()
	}

	// 5.数据转发
	go func() {
		targetAddr := fmt.Sprintf("%s:%d", targetIP, service.InternalPort)
		for {
			clientConn, err := listener.Accept()
			if err != nil {
				errCh <- fmt.Errorf("监听器退出: %w", err)
				return
			}
			logrus.Infof("[%s] 收到外部连接: %s", service.Name, clientConn.RemoteAddr())
			go Forward(clientConn, targetAddr, protocol)
		}
	}()

	logrus.Infof("   访问地址: %s", publicURL)

	return <-errCh
}

// 与STUN服务器握手
func doTcpStunHandshake(conn net.Conn) (string, int, error) {

	// 发送STUN请求
	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	if _, err := conn.Write(msg.Raw); err != nil {
		return "", 0, fmt.Errorf("发送STUN请求失败%s", err)
	}

	// 读取响应
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return "", 0, fmt.Errorf("读取响应失败：%s", err)
	}

	// 解析响应
	var response stun.Message
	response.Raw = buf[:n]
	if err = response.Decode(); err != nil {
		return "", 0, fmt.Errorf("解码stun失败%s", err)
	}

	var xorAddr stun.XORMappedAddress
	if err = xorAddr.GetFrom(&response); err != nil {
		return "", 0, fmt.Errorf("获取映射地址失败: %s", err)

	}

	return xorAddr.IP.String(), xorAddr.Port, nil
}

// 与STUN服务器握手
func doUDPStunHandshake(conn *net.UDPConn, stunServerAddr *net.UDPAddr) (string, int, error) {

	// 发送STUN请求
	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	if _, err := conn.WriteToUDP(msg.Raw, stunServerAddr); err != nil {
		return "", 0, fmt.Errorf("发送UDP STUN请求失败: %v", err)
	}

	// 设置超时
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	//读取响应
	buf := make([]byte, 1024)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		return "", 0, fmt.Errorf("读取UDP响应失败: %v", err)
	}

	// 解析响应
	var response stun.Message
	response.Raw = buf[:n]
	if err = response.Decode(); err != nil {
		return "", 0, fmt.Errorf("解码UDP STUN失败%s", err)
	}

	var xorAddr stun.XORMappedAddress
	if err = xorAddr.GetFrom(&response); err != nil {
		return "", 0, fmt.Errorf("获取UDP映射地址失败: %v", err)

	}

	return xorAddr.IP.String(), xorAddr.Port, nil
}

// sshConnectCheck 通过读取 SSH 横幅来验证 SSH 服务可达性
func sshConnectCheck(host string, port int, timeout time.Duration) bool {
	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		logrus.Debugf("SSH连接检查失败 %s: %v", addr, err)
		return false
	}
	defer conn.Close()

	// 读取 SSH 横幅，例如 "SSH-2.0-OpenSSH_8.9"
	buf := make([]byte, 64)
	conn.SetReadDeadline(time.Now().Add(timeout))
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		logrus.Debugf("SSH横幅读取失败 %s: %v", addr, err)
		return false
	}

	banner := string(buf[:n])
	if strings.HasPrefix(banner, "SSH-") {
		logrus.Debugf("SSH检查 OK %s, 横幅: %s", addr, strings.TrimSpace(banner))
		return true
	}

	logrus.Debugf("SSH检查 NOT OK %s, 收到: %s", addr, strings.TrimSpace(banner))
	return false
}

// serviceHealthCheck 根据 Protocol 字段选择合适的健康检查方式
// Protocol: "ssh" -> SSH横幅检测, "http"/"https" -> HTTP检测, "tcp"/"udp" -> TCP连通性检测
func serviceHealthCheck(service *model.Service, publicURL string, publicIP string, publicPort int) bool {
	proto := strings.ToLower(service.Protocol)

	switch proto {
	case "ssh":
		return sshConnectCheck(publicIP, publicPort, 3*time.Second)
	case "http", "https":
		return httpCheckWithRetry(publicURL, 1, 3*time.Second)
	default:
		// tcp/udp 等其他协议使用 TCP 连通性检查
		return tcpConnectCheck(publicIP, publicPort, 3*time.Second)
	}
}

// tcpConnectCheck 通用 TCP 连通性检查
func tcpConnectCheck(host string, port int, timeout time.Duration) bool {
	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		logrus.Debugf("TCP连接检查失败 %s: %v", addr, err)
		return false
	}
	conn.Close()
	logrus.Debugf("TCP连接检查 OK %s", addr)
	return true
}

// TCP STUN 健康检测
func tcpStunHealthCheck(stunConn net.Conn, publicURL string, publicIP string, expectedPublicPort int, localPort uint16, service *model.Service) error {
	healthTicker := time.NewTicker(28 * time.Second) // 每28s 检测一次
	defer healthTicker.Stop()

	// 首次保活：等待 NAT 映射稳定后再检查
	time.Sleep(2 * time.Second)
	if !serviceHealthCheck(service, publicURL, publicIP, expectedPublicPort) {
		return fmt.Errorf("首次保活失败，重启")
	}

	maxFailures := 3 // 失败阈值
	currentStunConn := stunConn

	logrus.Info("启动TCP健康检查 间隔28s")

	for range healthTicker.C {
		// 策略1: 端到端服务检测+保活
		if serviceHealthCheck(service, publicURL, publicIP, expectedPublicPort) {
			continue // 服务正常，跳过stun检查
		}

		// 策略2: STUN 检测NAT映射
		_, port, err := doTcpStunHandshake(currentStunConn)
		if err != nil {
			logrus.Infof("STUN连接断开，尝试重连...")

			// 关闭旧连接
			currentStunConn.Close()
			// 重连STUN
			localAddr := fmt.Sprintf("%s:%d", global.StunConfig.LocalIP, localPort)
			newConn, err := reuseport.Dial("tcp", localAddr, global.StunConfig.BestSTUN)
			if err != nil {
				return fmt.Errorf("STUN重连失败: %w", err)
			}

			// 验证新连接的端口
			_, newPort, err := doTcpStunHandshake(newConn)
			if err != nil {
				newConn.Close()
				return fmt.Errorf("重连后STUN验证失败: %w", err)
			}

			// 检查端口是否变化
			if newPort != expectedPublicPort {
				newConn.Close()
				return fmt.Errorf("公网端口漂移 %d -> %d", expectedPublicPort, newPort)
			}

			logrus.Infof("✅ STUN重连成功，端口保持 %d", newPort)
			currentStunConn = newConn
			continue
		}

		// STUN正常但端口变化，触发重启
		if port != expectedPublicPort {
			return fmt.Errorf("❌ 公网端口漂移 %d -> %d，需要重新打洞", expectedPublicPort, port)
		}

		// STUN正常但服务持续失败，可能是上游服务问题
		logrus.Warnf("STUN端口正常但服务检查持续失败（已检查 %d 次），可能需要检查上游服务", maxFailures)
	}

	return nil
}

// UDP健康检测
func udpStunHealthCheck(udpConn *net.UDPConn, stunServer *net.UDPAddr, expectedPublicPort int, localPort uint16) error {
	healthTicker := time.NewTicker(28 * time.Second) // 每28s 健康检测一次
	defer healthTicker.Stop()

	consecutiveFailures := 0 // 连续失败计数器
	maxFailures := 3         // 失败阈值
	currentConn := udpConn

	logrus.Info("启动UDP健康检查 间隔28s")

	for range healthTicker.C {
		// STUN 检测NAT映射
		_, port, err := doUDPStunHandshake(currentConn, stunServer)
		if err != nil {
			consecutiveFailures++
			logrus.Warnf("UDP STUN检查失败 (%d/%d): %v", consecutiveFailures, maxFailures, err)

			// 达到失败阈值，尝试重建
			if consecutiveFailures >= maxFailures {
				logrus.Infof("UDP连接异常，尝试重建...")
				// 重建
				currentConn.Close()
				localAddr := &net.UDPAddr{
					IP:   net.ParseIP(global.StunConfig.LocalIP),
					Port: int(localPort),
				}
				newConn, err := net.ListenUDP("udp", localAddr)
				if err != nil {
					return fmt.Errorf("UDP重建失败: %v", err)
				}

				// 验证新的连接，确保外部端口没变
				_, newPort, err := doUDPStunHandshake(newConn, stunServer)
				if err != nil {
					newConn.Close()
					return fmt.Errorf("重建后STUN验证失败: %v", err)
				}

				// 判断端口变没，如果变了必须退出，重新打洞
				if newPort != expectedPublicPort {
					newConn.Close()
					return fmt.Errorf("公网端口漂移 %d -> %d", expectedPublicPort, newPort)
				}

				logrus.Infof("✅ UDP重建成功，端口保持 %d", newPort)
				currentConn = newConn
				consecutiveFailures = 0 // 重建成功，重置计数
			}
			continue
		}

		// STUN 正常 但是端口变了，得重启
		if port != expectedPublicPort {
			return fmt.Errorf("❌ 公网端口变化 %d -> %d", expectedPublicPort, port)
		}

		// STUN正常
		consecutiveFailures = 0
	}
	return nil

}

func httpCheckWithRetry(url string, maxRetries int, interval time.Duration) bool {
	// 创建可复用的 Transport（移到循环外）
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		MaxIdleConns:      10,
		IdleConnTimeout:   30 * time.Second,
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections() // 函数结束时清理空闲连接

	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	for i := 0; i < maxRetries; i++ {
		resp, err := client.Get(url)

		//无论成功失败都要关闭 Body
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}

		if err != nil {
			logrus.Debug("HTTP端到端检查 NOT OK", url)
			time.Sleep(interval)
			continue
		}

		logrus.Debug("HTTP端到端检查 OK", url)
		return resp.StatusCode > 0
	}

	return false
}

// HTTP端到端检查
func httpCheckOK(url string, timeout time.Duration) bool {

	// 创建跳过证书验证的Transport
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}

	client := http.Client{
		Timeout:   timeout,
		Transport: tr,
		// 不跟随重定向 301 302说明服务通过
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(url)
	if err != nil {
		logrus.Debug("HTTP端到端检查 NOT OK", url)
		return false
	}
	defer resp.Body.Close()

	logrus.Debug("HTTP端到端检查 OK", url)

	return resp.StatusCode > 0 // 即使返回404也是穿透成功的
}

// 发送TCP STUN 心跳包
func SendTCPHeartbeat(conn net.Conn) error {
	// 使用 STUN Binding Request（不等待响应，仅用于保活）
	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})

	_, err := conn.Write(msg.Raw)
	if err != nil {
		return fmt.Errorf("发送心跳包失败")
	}

	logrus.Debug("TCP心跳包已发送")
	return nil
}

// 发送UDP STUN 心跳包
func SendUdpHeartbeat(conn *net.UDPConn, stunServer *net.UDPAddr) error {
	// 使用 STUN Binding Request（不等待响应，仅用于保活）
	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)

	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})

	_, err := conn.WriteToUDP(msg.Raw, stunServer)
	if err != nil {
		return fmt.Errorf("发送UDP心跳失败: %v", err)
	}

	logrus.Debug("UDP心跳包已发送")
	return nil
}
