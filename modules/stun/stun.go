package stun

import (
	"context"
	"fmt"
	"linkstar/global"
	"linkstar/modules/stun/model"
	"net"
	"strings"
	"time"

	"github.com/libp2p/go-reuseport"
	"github.com/pion/stun"
	"github.com/sirupsen/logrus"
)

// RunStunTunnelWithContext 实现内网穿透逻辑  支持 context 取消的穿透逻辑
func RunStunTunnelWithContext(ctx context.Context, targetIP string, service *model.Service) error {
	protocol := strings.ToLower(service.Protocol) // 转为小写

	localAddr := fmt.Sprintf("%s:0", global.StunConfig.LocalIP) //端口为0任意端口

	// STUN 拨号
	stunConn, err := reuseport.Dial(protocol, localAddr, global.StunConfig.BestSTUN)
	if err != nil {
		return fmt.Errorf("STUN拨号失败 [%s]:%w", global.StunConfig.BestSTUN, err)
	}

	var localPort uint16
	if protocol == "tcp" {
		localPort = uint16(stunConn.LocalAddr().(*net.TCPAddr).Port)
	} else {
		localPort = uint16(stunConn.LocalAddr().(*net.UDPAddr).Port)
	}

	// STUN 握手
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
		stunConn.Close()
		return fmt.Errorf("与STUN服务器握手失败:%w", err)
	}

	// 端口复用监听
	listenAddr := fmt.Sprintf("%s:%d", global.StunConfig.LocalIP, localPort)
	listener, err := reuseport.Listen(protocol, listenAddr) // 使用reuseport SO_REUSEPORT 可以复用端口
	if err != nil {
		stunConn.Close()
		return fmt.Errorf("端口监听失败：%w", err)
	}

	// 路由器upnp映射
	upnpCtx, upnpCancel := context.WithTimeout(ctx, 25*time.Second) //创建upnp的ctx
	defer upnpCancel()
	description := fmt.Sprintf("LinkStar-%s", service.Name)
	err = AddPortMappingQueue(upnpCtx, localPort, localPort, "TCP", description)
	if err != nil {
		logrus.Warnf("[%s] UPnP 映射失败 (非致命): %v", service.Name, err)
		// todo 处理失败
	} else {
		logrus.Infof("[%s] UPnP 映射成功: 路由器 WAN:%d -> 本机:%d", service.Name, localPort, localPort)
	}

	// ctx 取消时关闭连接，让所有阻塞调用立即返回
	go func() {
		<-ctx.Done()
		logrus.Infof("[%s] ctx 取消，关闭连接", service.Name)
		stunConn.Close()
	}()

	defer func() {
		logrus.Infof("[%s] 正在清理资源...", service.Name)
		stunConn.Close()
		listener.Close()
		go DeletePortMapping(localPort, protocol)
		service.PunchSuccess = false
		service.ExternalPort = 0
	}()

	errCh := make(chan error, 3)
	logrus.Infof("%v %v %v", localPort, publicIP, publicPort)

	// 存储数据
	var publicURL string
	if service.TLS {
		publicURL = fmt.Sprintf("https://%s:%d", publicIP, publicPort)
	} else {
		publicURL = fmt.Sprintf("http://%s:%d", publicIP, publicPort)
	}

	service.ExternalPort = uint16(publicPort)
	service.PunchSuccess = true

	// 开启保活
	if protocol == "tcp" {
		go func() {
			err = tcpStunHealthCheck(stunConn, publicIP, publicPort, localPort, service)
			if err != nil {
				service.PunchSuccess = false
				errCh <- fmt.Errorf("TCP健康检查失败: %w", err)
			}
		}()
	} else {
		go func() {
			udpConn := stunConn.(*net.UDPConn)
			stunServerAddr, _ := net.ResolveUDPAddr("udp", global.StunConfig.BestSTUN)
			err = udpStunHealthCheck(udpConn, stunServerAddr, publicPort, localPort)
			if err != nil {
				service.PunchSuccess = false
				errCh <- fmt.Errorf("UDP健康检查失败: %w", err)
			}
		}()
	}

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

	// 输出访问数据
	logrus.Infof("   访问地址: %s", publicURL)
	return <-errCh
}

// 与STUN服务器握手TCP
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

// 与STUN服务器握手UDP
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

// TCP首次健康检测保活
func firstTcpHealthKeep(publicIP string, expectedPublicPort int) bool {
	sleepTime := time.Second
	for i := 0; i < 3; i++ {
		time.Sleep(sleepTime)
		if tcpConnectCheck(publicIP, expectedPublicPort, 3*time.Second) {
			return true
		}
		sleepTime = sleepTime * 2 // 休息1 2 4 7秒还不通就是死了
	}
	return false
}

// TCP STUN 健康检测
func tcpStunHealthCheck(stunConn net.Conn, publicIP string, expectedPublicPort int, localPort uint16, service *model.Service) error {
	healthTicker := time.NewTicker(28 * time.Second) // 每28s 检测一次
	defer healthTicker.Stop()

	// 首次保活与检测
	if !firstTcpHealthKeep(publicIP, expectedPublicPort) {
		return fmt.Errorf("[%s] 首次保活失败，重启", service.Name)
	}
	service.PunchSuccess = true

	maxFailures := 3 // 失败阈值
	failureCount := 0
	currentStunConn := stunConn

	logrus.Infof("[%s] 启动TCP健康检查 间隔28s", service.Name)

	for range healthTicker.C {
		// 策略1: 端到端服务检测+保活
		if tcpConnectCheck(publicIP, expectedPublicPort, 3*time.Second) {
			if failureCount == 0 { // 若果是第一次失败防止网络波动，来多一次
				if tcpConnectCheck(publicIP, expectedPublicPort, 3*time.Second) {
					continue
				}
			}
			failureCount = 0 // 成功就重置
			continue         // 服务正常，跳过stun检查
		}

		failureCount++
		logrus.Warnf("[%s] 端到端检查失败 (%d/%d)", service.Name, failureCount, maxFailures)

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

		if failureCount >= maxFailures {
			return fmt.Errorf("[%s] 连续 %d 次检查失败，重新打洞", service.Name, maxFailures)
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
	healthTicker := time.NewTicker(280 * time.Second) // 每28s 健康检测一次
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
