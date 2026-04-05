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

// RunStunTunnelWithContext 实现内网穿透逻辑，支持 context 取消
func RunStunTunnelWithContext(ctx context.Context, targetIP string, service *model.Service) error {
	protocol := strings.ToLower(service.Protocol)
	localAddr := fmt.Sprintf("%s:0", global.StunConfig.LocalIP)

	// STUN 拨号
	stunConn, err := reuseport.Dial(protocol, localAddr, global.StunConfig.BestSTUN)
	if err != nil {
		return fmt.Errorf("STUN拨号失败 [%s]: %w", global.StunConfig.BestSTUN, err)
	}

	var localPort uint16
	if protocol == "tcp" {
		localPort = uint16(stunConn.LocalAddr().(*net.TCPAddr).Port)
	} else {
		localPort = uint16(stunConn.LocalAddr().(*net.UDPAddr).Port)
	}

	// STUN 握手，获取公网 IP:Port
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
		return fmt.Errorf("与STUN服务器握手失败: %w", err)
	}

	// 端口复用监听
	listenAddr := fmt.Sprintf("%s:%d", global.StunConfig.LocalIP, localPort)
	listener, err := reuseport.Listen(protocol, listenAddr)
	if err != nil {
		stunConn.Close()
		return fmt.Errorf("端口监听失败: %w", err)
	}

	// UPnP 映射（非致命，失败继续运行）
	upnpCtx, upnpCancel := context.WithTimeout(ctx, 25*time.Second)
	defer upnpCancel()
	description := fmt.Sprintf("LinkStar-%s", service.Name)
	if err = AddPortMappingQueue(upnpCtx, localPort, localPort, "TCP", description); err != nil {
		logrus.Warnf("[%s] UPnP 映射失败 (非致命): %v", service.Name, err)
	} else {
		logrus.Infof("[%s] UPnP 映射成功: WAN:%d -> 本机:%d", service.Name, localPort, localPort)
	}

	// innerCtx：统一控制健康检查和 Accept 循环的退出
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	defer func() {
		logrus.Infof("[%s] 正在清理资源...", service.Name)
		stunConn.Close()
		listener.Close()
		go DeletePortMapping(localPort, protocol)
		service.PunchSuccess = false
		service.ExternalPort = 0
	}()

	// 更新服务状态
	var publicURL string
	if service.TLS {
		publicURL = fmt.Sprintf("https://%s:%d", publicIP, publicPort)
	} else {
		publicURL = fmt.Sprintf("http://%s:%d", publicIP, publicPort)
	}
	service.ExternalPort = uint16(publicPort)
	service.PunchSuccess = true
	logrus.Infof("[%s] 穿透成功 本地端口:%d 公网:%s", service.Name, localPort, publicURL)

	// errCh buffer=2，健康检查和 Accept 各一个写入位置，不会阻塞
	errCh := make(chan error, 2)

	// 健康检查 goroutine
	if protocol == "tcp" {
		go func() {
			if err := tcpStunHealthCheck(innerCtx, stunConn, publicIP, publicPort, localPort, service.Name); err != nil {
				service.PunchSuccess = false
				errCh <- fmt.Errorf("TCP健康检查失败: %w", err)
			}
		}()
	} else {
		go func() {
			udpConn := stunConn.(*net.UDPConn)
			stunServerAddr, _ := net.ResolveUDPAddr("udp", global.StunConfig.BestSTUN)
			if err := udpStunHealthCheck(innerCtx, udpConn, stunServerAddr, publicPort, localPort); err != nil {
				service.PunchSuccess = false
				errCh <- fmt.Errorf("UDP健康检查失败: %w", err)
			}
		}()
	}

	// Accept 循环 goroutine
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

	return <-errCh
}

// ═══════════════════════════════════════════════════
// STUN 握手
// ═══════════════════════════════════════════════════

func doTcpStunHandshake(conn net.Conn) (string, int, error) {
	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	if _, err := conn.Write(msg.Raw); err != nil {
		return "", 0, fmt.Errorf("发送STUN请求失败: %w", err)
	}

	conn.SetDeadline(time.Now().Add(3 * time.Second))
	defer conn.SetDeadline(time.Time{})

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return "", 0, fmt.Errorf("读取响应失败: %w", err)
	}

	var response stun.Message
	response.Raw = buf[:n]
	if err = response.Decode(); err != nil {
		return "", 0, fmt.Errorf("解码STUN失败: %w", err)
	}

	var xorAddr stun.XORMappedAddress
	if err = xorAddr.GetFrom(&response); err != nil {
		return "", 0, fmt.Errorf("获取映射地址失败: %w", err)
	}
	return xorAddr.IP.String(), xorAddr.Port, nil
}

func doUDPStunHandshake(conn *net.UDPConn, stunServerAddr *net.UDPAddr) (string, int, error) {
	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	if _, err := conn.WriteToUDP(msg.Raw, stunServerAddr); err != nil {
		return "", 0, fmt.Errorf("发送UDP STUN请求失败: %w", err)
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	buf := make([]byte, 1024)
	n, _, err := conn.ReadFromUDP(buf)
	if err != nil {
		return "", 0, fmt.Errorf("读取UDP响应失败: %w", err)
	}

	var response stun.Message
	response.Raw = buf[:n]
	if err = response.Decode(); err != nil {
		return "", 0, fmt.Errorf("解码UDP STUN失败: %w", err)
	}

	var xorAddr stun.XORMappedAddress
	if err = xorAddr.GetFrom(&response); err != nil {
		return "", 0, fmt.Errorf("获取UDP映射地址失败: %w", err)
	}
	return xorAddr.IP.String(), xorAddr.Port, nil
}

// ═══════════════════════════════════════════════════
// TCP 健康检查
// ═══════════════════════════════════════════════════

// firstTcpHealthKeep 首次保活检测，等待 NAT 打洞稳定
// 修复：原版 time.Sleep 不感知 ctx，改为 sleepWithCtx
// 返回 false 表示保活失败或 ctx 已取消
func firstTcpHealthKeep(ctx context.Context, publicIP string, expectedPublicPort int) bool {
	sleepTime := 2 * time.Second
	for i := 0; i < 3; i++ {
		// ctx 取消时立即返回，不再阻塞 StopService 等待
		if !sleepWithCtx(ctx, sleepTime) {
			return false
		}
		if tcpConnectCheck(publicIP, expectedPublicPort, 3*time.Second) {
			return true
		}
		sleepTime *= 2 // 2s → 4s → 8s
	}
	return false
}

// tcpStunHealthCheck TCP 保活与健康检测
// 修复：去掉 service 参数，只接收 serviceName 字符串，状态管理由调度器负责
func tcpStunHealthCheck(ctx context.Context, stunConn net.Conn, publicIP string, expectedPublicPort int, localPort uint16, serviceName string) error {
	// 首次保活
	if !firstTcpHealthKeep(ctx, publicIP, expectedPublicPort) {
		if ctx.Err() != nil {
			return nil // ctx 取消，正常退出，不算失败
		}
		return fmt.Errorf("[%s] 首次保活失败，触发重启", serviceName)
	}

	healthTicker := time.NewTicker(28 * time.Second)
	defer healthTicker.Stop()

	maxFailures := 3
	failureCount := 0
	currentStunConn := stunConn

	logrus.Infof("[%s] TCP健康检查启动，间隔28s", serviceName)

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-healthTicker.C:
			// 策略1：端到端连通性检测
			if tcpConnectCheck(publicIP, expectedPublicPort, 3*time.Second) {
				failureCount = 0
				continue
			}
			failureCount++

			// 第一次失败可能是抖动，立即补测一次
			if failureCount == 1 {
				if tcpConnectCheck(publicIP, expectedPublicPort, 3*time.Second) {
					failureCount = 0
					continue
				}
			}
			logrus.Warnf("[%s] 端到端检查失败 (%d/%d)", serviceName, failureCount, maxFailures)

			// 策略2：STUN 检测 NAT 映射是否存活
			_, port, err := doTcpStunHandshake(currentStunConn)
			if err != nil {
				// STUN 连接断开，尝试重连
				logrus.Infof("[%s] STUN连接断开，尝试重连...", serviceName)
				currentStunConn.Close()

				localAddr := fmt.Sprintf("%s:%d", global.StunConfig.LocalIP, localPort)
				newConn, err := reuseport.Dial("tcp", localAddr, global.StunConfig.BestSTUN)
				if err != nil {
					return fmt.Errorf("STUN重连失败: %w", err)
				}

				_, newPort, err := doTcpStunHandshake(newConn)
				if err != nil {
					newConn.Close()
					return fmt.Errorf("重连后STUN验证失败: %w", err)
				}

				if newPort != expectedPublicPort {
					newConn.Close()
					return fmt.Errorf("公网端口漂移 %d -> %d，需要重新打洞", expectedPublicPort, newPort)
				}

				logrus.Infof("[%s] STUN重连成功，端口保持 %d", serviceName, newPort)
				currentStunConn = newConn
				continue
			}

			// STUN 正常但端口漂移
			if port != expectedPublicPort {
				return fmt.Errorf("公网端口漂移 %d -> %d，需要重新打洞", expectedPublicPort, port)
			}

			// STUN 正常但端到端持续失败，达到阈值才重启
			if failureCount >= maxFailures {
				return fmt.Errorf("[%s] 端到端连续失败 %d 次，触发重启", serviceName, maxFailures)
			}

			logrus.Warnf("[%s] STUN端口正常但服务检查持续失败，可能是上游服务问题", serviceName)
		}
	}
}

// ═══════════════════════════════════════════════════
// UDP 健康检查
// ═══════════════════════════════════════════════════

// udpStunHealthCheck UDP 保活与健康检测
// 修复1：原版 ticker 是 280s，注释却写28s，改回 28s
// 修复2：for range ticker.C 不感知 ctx，改为 select
func udpStunHealthCheck(ctx context.Context, udpConn *net.UDPConn, stunServer *net.UDPAddr, expectedPublicPort int, localPort uint16) error {
	healthTicker := time.NewTicker(28 * time.Second) // 修复：原版误写成 280s
	defer healthTicker.Stop()

	consecutiveFailures := 0
	maxFailures := 3
	currentConn := udpConn

	logrus.Info("UDP健康检查启动，间隔28s")

	for {
		select {
		case <-ctx.Done(): // 修复：原版 for range 无法感知 ctx 取消
			return nil

		case <-healthTicker.C:
			_, port, err := doUDPStunHandshake(currentConn, stunServer)
			if err != nil {
				consecutiveFailures++
				logrus.Warnf("UDP STUN检查失败 (%d/%d): %v", consecutiveFailures, maxFailures, err)

				if consecutiveFailures < maxFailures {
					continue
				}

				// 达到失败阈值，尝试重建 UDP 连接
				logrus.Infof("UDP连接异常，尝试重建...")
				currentConn.Close()

				localAddr := &net.UDPAddr{
					IP:   net.ParseIP(global.StunConfig.LocalIP),
					Port: int(localPort),
				}
				newConn, err := net.ListenUDP("udp", localAddr)
				if err != nil {
					return fmt.Errorf("UDP重建失败: %w", err)
				}

				_, newPort, err := doUDPStunHandshake(newConn, stunServer)
				if err != nil {
					newConn.Close()
					return fmt.Errorf("重建后STUN验证失败: %w", err)
				}

				if newPort != expectedPublicPort {
					newConn.Close()
					return fmt.Errorf("公网端口漂移 %d -> %d", expectedPublicPort, newPort)
				}

				logrus.Infof("UDP重建成功，端口保持 %d", newPort)
				currentConn = newConn
				consecutiveFailures = 0
				continue
			}

			// STUN 正常但端口漂移
			if port != expectedPublicPort {
				return fmt.Errorf("公网端口变化 %d -> %d", expectedPublicPort, port)
			}

			consecutiveFailures = 0
		}
	}
}

// ═══════════════════════════════════════════════════
// 工具函数
// ═══════════════════════════════════════════════════

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
