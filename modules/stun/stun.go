package stun

import (
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

// 实现stun内网穿透逻辑
func RunStunTunnel(targetIP string, service *model.Service) error {
	protocol := strings.ToLower(service.Protocol) // 转为小写
	localAddr := fmt.Sprintf("%s:0", global.StunConfig.LocalIP)

	// 1.STUN拨号
	stunConn, err := reuseport.Dial(protocol, localAddr, global.StunConfig.BestSTUN) // 使用reuseport SO_REUSEPORT 可以复用端口
	if err != nil {
		stunConn.Close()
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
	publicURL := fmt.Sprintf("http://%s:%d", publicIP, publicPort)

	if protocol == "tcp" {
		go func() {
			err = tcpStunHealthCheck(stunConn, publicURL, publicPort, localPort)
			if err != nil {
				errCh <- fmt.Errorf("TCP健康检查失败: %w", err)
			}
		}()
	} else {
		go func() {
			udpConn := stunConn.(*net.UDPConn)
			stunServerAddr, _ := net.ResolveUDPAddr("udp", global.StunConfig.BestSTUN)
			err = udpStunHealthCheck(udpConn, stunServerAddr, publicPort, localPort)
			if err != nil {
				errCh <- fmt.Errorf("TCP健康检查失败: %w", err)
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

// TCP STUN 健康检测
func tcpStunHealthCheck(stunConn net.Conn, publicURL string, expectedPublicPort int, localPort uint16) error {
	ticker := time.NewTicker(30 * time.Second) // 每30s 检测一次
	defer ticker.Stop()

	consecutiveFailures := 0 // 连续失败计数器
	maxFailures := 3         // 失败阈值
	currentStunConn := stunConn

	logrus.Info("启动TCP建康检查 间隔")

	for range ticker.C {
		// 策略1
		// HTTP 端到端检测 GET检测
		if httpCheckOK(publicURL, 3*time.Second) {
			consecutiveFailures = 0 // HTTP成功则重置失败计数
			continue                // 跳过stun检查
		}

		// HTTP检测失败，记录
		consecutiveFailures++

		// 策略2
		//STUN 检测NAT映射
		_, port, err := doTcpStunHandshake(stunConn)
		if err != nil {
			logrus.Infof("STUN连接断开，尝试重连...")

			// 重连
			currentStunConn.Close()
			localAddr := fmt.Sprintf("%s:%d", global.StunConfig.LocalIP, localPort)
			newConn, err := reuseport.Dial("tcp", localAddr, global.StunConfig.BestSTUN)
			if err != nil {
				if err != nil {
					// 重连失败，但如果HTTP之前一直成功，可能只是临时问题
					if consecutiveFailures >= maxFailures {
						return fmt.Errorf("STUN重连失败且HTTP连续%d次失败", consecutiveFailures)
					}
					logrus.Warnf("STUN重连失败但未达阈值: %v", err)
					continue
				}
			}
			// 验证新的连接，确保外部端口没变
			_, newPort, err := doTcpStunHandshake(newConn)
			if err != nil {
				newConn.Close()
				if consecutiveFailures >= maxFailures {
					return fmt.Errorf("重连后STUN验证失败")
				}
				continue
			}

			// 判断端口变没，如果变了必须退出，重新打洞
			if newPort != expectedPublicPort {
				newConn.Close()
				return fmt.Errorf("公网端口漂移 %d -> %d", expectedPublicPort, newPort)
			}

			logrus.Infof("✅ STUN重连成功，端口保持 %d", newPort)
			currentStunConn = newConn
			consecutiveFailures = 0 // 重连成功，重置计数
			continue
		}

		// STUN 正常 但是端口变了，得重启
		if port != expectedPublicPort {
			return fmt.Errorf("❌ 公网端口变化 %d -> %d", expectedPublicPort, port)
		}

		// STUN正常 HTTP失败
		if consecutiveFailures >= maxFailures {
			return fmt.Errorf("HTTP连续失败%d次", consecutiveFailures)
		}

	}
	return nil
}

// UDP健康检测
func udpStunHealthCheck(udpConn *net.UDPConn, stunServer *net.UDPAddr, expectedPublicPort int, localPort uint16) error {
	ticker := time.NewTicker(30 * time.Second) // 每30s 检测一次
	defer ticker.Stop()

	consecutiveFailures := 0 // 连续失败计数器
	maxFailures := 3         // 失败阈值
	currentConn := udpConn

	logrus.Info("启动UDP健康检查 间隔")

	for range ticker.C {
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

// HTTP端到端检查
func httpCheckOK(url string, timeout time.Duration) bool {
	client := http.Client{
		Timeout: timeout,
		// 不根随重定向 301 302说明服务通过
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode > 0 // 即使返回404也是穿透成功的
}

// 发送TCP STUN 心跳包
func sendTCPHeartbeat(conn net.Conn) error {
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
func sendUdpHeartbeat(conn *net.UDPConn, stunServer *net.UDPAddr) error {
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
