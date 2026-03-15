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
	pionStun "github.com/pion/stun"
	"github.com/sirupsen/logrus"
)

// 启动STUN服务 StarStun
func StarStun(devices []model.Device) error {

	errChan := make(chan error, 1)
	activeServices := 0

	// 直接用下标遍历 global.StunConfig.Devices，确保拿到的是真实指针
	for i := range global.StunConfig.Devices {
		for j := range global.StunConfig.Devices[i].Services {
			device := &global.StunConfig.Devices[i]
			service := &global.StunConfig.Devices[i].Services[j]

			// 未启用
			if !service.Enabled {
				logrus.Infof("[%s - %s] 服务未启用，跳过", device.Name, service.Name)
				continue
			}

			activeServices++
			time.Sleep(300 * time.Millisecond)
			// 为每个服务创建单独进程
			go func(device *model.Device, service *model.Service) {
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
			}(device, service)
		}
	}

	if activeServices == 0 {
		return fmt.Errorf("没有启用的服务需要穿透")
	}

	logrus.Infof("✅ 已启动 %d 个服务的 STUN 穿透", activeServices)

	// 阻塞等待第一个严重错误（如果所有服务都正常运行，会一直阻塞）
	return <-errChan
}

// RunStunTunnelWithContext 支持 context 取消的穿透逻辑（供 service_manager 使用）
func RunStunTunnelWithContext(ctx context.Context, targetIP string, service *model.Service) error {
	protocol := strings.ToLower(service.Protocol)
	if protocol == "ssh" {
		protocol = "tcp"
	}
	localAddr := fmt.Sprintf("%s:0", global.StunConfig.LocalIP)

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

	listenAddr := fmt.Sprintf("%s:%d", global.StunConfig.LocalIP, localPort)
	listener, err := reuseport.Listen(protocol, listenAddr)
	if err != nil {
		stunConn.Close()
		return fmt.Errorf("端口监听失败：%w", err)
	}

	go func() {
		description := fmt.Sprintf("LinkStar-%s", service.Name)
		err := AddPortMapping(localPort, localPort, "TCP", description)
		if err != nil {
			logrus.Warnf("[%s] UPnP 映射失败 (非致命): %v", service.Name, err)
		} else {
			logrus.Infof("[%s] UPnP 映射成功: 路由器 WAN:%d -> 本机:%d", service.Name, localPort, localPort)
		}
	}()

	// ctx 取消时关闭连接，让所有阻塞调用立即返回
	go func() {
		<-ctx.Done()
		logrus.Infof("[%s] ctx 取消，关闭连接", service.Name)
		stunConn.Close()
		listener.Close()
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

	var publicURL string
	if service.TLS {
		publicURL = fmt.Sprintf("https://%s:%d", publicIP, publicPort)
	} else {
		publicURL = fmt.Sprintf("http://%s:%d", publicIP, publicPort)
	}

	service.ExternalPort = uint16(publicPort)
	service.PunchSuccess = true

	if protocol == "tcp" {
		go func() {
			service.StartupSuccess = true
			err = tcpStunHealthCheck(stunConn, publicURL, publicIP, publicPort, localPort, service)
			if err != nil {
				service.PunchSuccess = false
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

	logrus.Infof("   访问地址: %s", publicURL)
	return <-errCh
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
	if service.TLS {
		publicURL = fmt.Sprintf("https://%s:%d", publicIP, publicPort)
	} else {
		publicURL = fmt.Sprintf("http://%s:%d", publicIP, publicPort)
	}

	// 穿透成功，直接通过指针更新 service 的公网端口和成功状态
	service.ExternalPort = uint16(publicPort)
	service.PunchSuccess = true

	if protocol == "tcp" {
		go func() {
			service.StartupSuccess = true
			err = tcpStunHealthCheck(stunConn, publicURL, publicIP, publicPort, localPort, service)
			if err != nil {
				service.PunchSuccess = false
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
				service.PunchSuccess = false
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
	msg := pionStun.MustBuild(pionStun.TransactionID, pionStun.BindingRequest)
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
	var response pionStun.Message
	response.Raw = buf[:n]
	if err = response.Decode(); err != nil {
		return "", 0, fmt.Errorf("解码stun失败%s", err)
	}

	var xorAddr pionStun.XORMappedAddress
	if err = xorAddr.GetFrom(&response); err != nil {
		return "", 0, fmt.Errorf("获取映射地址失败: %s", err)

	}

	return xorAddr.IP.String(), xorAddr.Port, nil
}

// 与STUN服务器握手
func doUDPStunHandshake(conn *net.UDPConn, stunServerAddr *net.UDPAddr) (string, int, error) {

	// 发送STUN请求
	msg := pionStun.MustBuild(pionStun.TransactionID, pionStun.BindingRequest)
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
	var response pionStun.Message
	response.Raw = buf[:n]
	if err = response.Decode(); err != nil {
		return "", 0, fmt.Errorf("解码UDP STUN失败%s", err)
	}

	var xorAddr pionStun.XORMappedAddress
	if err = xorAddr.GetFrom(&response); err != nil {
		return "", 0, fmt.Errorf("获取UDP映射地址失败: %v", err)

	}

	return xorAddr.IP.String(), xorAddr.Port, nil
}
