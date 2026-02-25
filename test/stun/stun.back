package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/libp2p/go-reuseport"
	"github.com/pion/stun"
	"github.com/sirupsen/logrus"
)

// HealthCheckConfig 健康检查配置
type HealthCheckConfig struct {
	Interval     time.Duration // 检查间隔
	Timeout      time.Duration // 超时时间
	MaxFailures  int           // 最大失败次数
	UseHTTPCheck bool          // 是否使用HTTP检查
}

var defaultHealthCheck = HealthCheckConfig{
	Interval:     30 * time.Second, // 30秒检查一次
	Timeout:      10 * time.Second,
	MaxFailures:  3,    // 连续3次失败才认为服务中断
	UseHTTPCheck: true, // 优先使用HTTP检查
}

// SetupDeviceServices 循环处理设备下的所有服务
func SetupDeviceServices(device *model.Device) error {
	for i := range device.Services {
		svc := &device.Services[i]

		if !svc.Enabled {
			logrus.Infof("🚫 服务 [%s] 已禁用，跳过", svc.Name)
			continue
		}

		// 为每个服务开启独立的隧道协程
		go func(targetIP string, s *model.Service) {
			for {
				logrus.Infof("🔄 正在尝试启动服务隧道: %s (%d -> %d)", s.Name, s.InternalPort, s.ExternalPort)

				err := runTunnel(targetIP, s)
				if err != nil {
					logrus.Errorf("❌ 服务 [%s] 隧道异常退出: %v", s.Name, err)
					// 发生错误时等待一段时间后重试（例如网线拔插、网络抖动）
					time.Sleep(10 * time.Second)
					continue
				}

				// 如果 runTunnel 正常返回（虽然目前逻辑是阻塞的），也进行重试
				time.Sleep(1 * time.Second)
			}
		}(device.IP, svc)
	}
	return nil
}

// runTunnel 实现了双层 NAT 穿透的核心逻辑
func runTunnel(targetIP string, svc *model.Service) error {
	// 1. STUN 拨号
	localAddr := fmt.Sprintf("%s:0", global.StunConfig.LocalIP)
	stunConn, err := reuseport.Dial("tcp", localAddr, global.StunConfig.BestSTUN)
	if err != nil {
		return fmt.Errorf("STUN拨号失败 [%s]: %w", global.StunConfig.BestSTUN, err)
	}

	localPort := uint16(stunConn.LocalAddr().(*net.TCPAddr).Port)

	// 2. STUN 握手
	publicIP, publicPort, err := doStunHandshake(stunConn)
	if err != nil {
		stunConn.Close()
		return fmt.Errorf("STUN握手失败: %w", err)
	}

	// 3. 端口复用监听
	listenAddr := fmt.Sprintf("%s:%d", global.StunConfig.LocalIP, localPort)
	listener, err := reuseport.Listen("tcp", listenAddr)
	if err != nil {
		stunConn.Close()
		return fmt.Errorf("端口监听失败: %w", err)
	}

	// 4. 配置路由器 UPnP
	go func() {
		description := fmt.Sprintf("LinkStar-%s", svc.Name)
		err := AddPortMapping(localPort, localPort, "TCP", description)
		if err != nil {
			logrus.Warnf("[%s] UPnP 映射失败 (非致命): %v", svc.Name, err)
		} else {
			logrus.Infof("[%s] UPnP 映射成功: 路由器 WAN:%d -> 本机:%d", svc.Name, localPort, localPort)
		}
	}()

	publicURL := fmt.Sprintf("http://%s:%d", publicIP, publicPort)
	logrus.Infof("🚀 [%s] 穿透就绪:", svc.Name)
	logrus.Infof("   🌍 访问地址: %s", publicURL)
	logrus.Infof("   🔄 链路: 公网:%d -> 路由器:%d -> 本机:%d -> 目标:%s:%d",
		publicPort, localPort, localPort, targetIP, svc.InternalPort)

	svc.ExternalPort = uint16(publicPort)

	defer func() {
		logrus.Infof("[%s] 正在清理资源...", svc.Name)
		stunConn.Close()
		listener.Close()
		go DeletePortMapping(localPort, "TCP")
	}()

	errCh := make(chan error, 2)

	// 5. 数据转发
	go func() {
		targetAddr := fmt.Sprintf("%s:%d", targetIP, svc.InternalPort)
		for {
			clientConn, err := listener.Accept()
			if err != nil {
				errCh <- fmt.Errorf("监听器退出: %w", err)
				return
			}
			logrus.Infof("🔀 [%s] 收到外部连接: %s", svc.Name, clientConn.RemoteAddr())
			go forward(clientConn, targetAddr)
		}
	}()

	// 6. 改进的健康检查机制
	go func() {
		errCh <- advancedHealthCheck(stunConn, publicURL, publicPort, localPort)
	}()

	return <-errCh
}

// advancedHealthCheck 综合健康检查（HTTP优先 + STUN备用）
func advancedHealthCheck(stunConn net.Conn, publicURL string, expectedPublicPort int, localPort uint16) error {
	cfg := defaultHealthCheck
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	consecutiveFailures := 0
	currentStunConn := stunConn

	logrus.Infof("💓 启动智能健康检查 (HTTP优先，%v间隔)", cfg.Interval)

	for range ticker.C {
		// 策略1: 优先使用HTTP端到端检查
		if cfg.UseHTTPCheck {
			if httpCheckOK(publicURL, cfg.Timeout) {
				consecutiveFailures = 0
				logrus.Debugf("✅ HTTP检查正常: %s", publicURL)
				continue
			}
			consecutiveFailures++
			logrus.Warnf("⚠️ HTTP检查失败 (%d/%d): %s", consecutiveFailures, cfg.MaxFailures, publicURL)
		}

		// 策略2: HTTP失败时，用STUN检查NAT映射是否还在
		_, port, stunErr := doStunHandshake(currentStunConn)

		if stunErr != nil {
			logrus.Warnf("⚠️ STUN连接断开 (%v)，尝试原地重连...", stunErr)

			// 尝试重连STUN
			currentStunConn.Close()
			localAddr := fmt.Sprintf("%s:%d", global.StunConfig.LocalIP, localPort)
			newConn, dialErr := reuseport.Dial("tcp", localAddr, global.StunConfig.BestSTUN)

			if dialErr != nil {
				if consecutiveFailures >= cfg.MaxFailures {
					return fmt.Errorf("STUN重连失败且HTTP检查连续%d次失败", consecutiveFailures)
				}
				logrus.Warnf("STUN重连失败但未达阈值: %v", dialErr)
				continue
			}

			_, newPort, verifyErr := doStunHandshake(newConn)
			if verifyErr != nil {
				newConn.Close()
				if consecutiveFailures >= cfg.MaxFailures {
					return fmt.Errorf("重连后STUN验证失败且HTTP连续失败")
				}
				continue
			}

			if newPort != expectedPublicPort {
				newConn.Close()
				return fmt.Errorf("公网端口漂移 %d -> %d", expectedPublicPort, newPort)
			}

			logrus.Infof("✅ STUN原地重连成功，端口保持 %d", newPort)
			currentStunConn = newConn
			consecutiveFailures = 0 // STUN成功则重置失败计数
			continue
		}

		// STUN正常但端口变了
		if port != expectedPublicPort {
			return fmt.Errorf("公网端口变化 %d -> %d", expectedPublicPort, port)
		}

		// STUN正常，可能是HTTP临时抖动
		if consecutiveFailures >= cfg.MaxFailures {
			return fmt.Errorf("HTTP端到端检查连续失败%d次", consecutiveFailures)
		}

		logrus.Debugf("💓 STUN正常 (端口:%d)，HTTP失败%d次", port, consecutiveFailures)
	}
	return nil
}

// httpCheckOK 通过HTTP GET检查公网地址是否可达
func httpCheckOK(url string, timeout time.Duration) bool {
	client := &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // 不跟随重定向
		},
	}

	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// 只要能连接上就算成功（不管是200/404/302等）
	// 因为我们只关心NAT穿透是否有效，不关心应用层响应
	return resp.StatusCode > 0
}

// doStunHandshake 执行一次 STUN 绑定请求（保持原有逻辑）
func doStunHandshake(conn net.Conn) (string, int, error) {
	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	if _, err := conn.Write(msg.Raw); err != nil {
		return "", 0, err
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return "", 0, err
	}

	res := &stun.Message{Raw: buf[:n]}
	if err := res.Decode(); err != nil {
		return "", 0, err
	}

	var xorAddr stun.XORMappedAddress
	if err := xorAddr.GetFrom(res); err != nil {
		return "", 0, err
	}

	return xorAddr.IP.String(), xorAddr.Port, nil
}

// forward 双向数据转发（保持原有逻辑）
func forward(src net.Conn, targetAddr string) {
	defer src.Close()

	dst, err := net.DialTimeout("tcp", targetAddr, 3*time.Second)
	if err != nil {
		logrus.Errorf("❌ 连接内网目标失败 [%s]: %v", targetAddr, err)
		return
	}
	defer dst.Close()

	go func() {
		_, _ = io.Copy(dst, src)
	}()
	_, _ = io.Copy(src, dst)
}
