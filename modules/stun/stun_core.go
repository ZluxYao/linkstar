package stun

import (
	"context"
	"crypto/tls"
	"fmt"
	"linkstar/global"
	"linkstar/modules/stun/model"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/libp2p/go-reuseport"
	pionStun "github.com/pion/stun"
	"github.com/sirupsen/logrus"
)

// normalizeProtocol ssh -> tcp
func normalizeProtocol(p string) string {
	p = strings.ToLower(p)
	if p == "ssh" {
		return "tcp"
	}
	return p
}

// buildPublicURL 构造公网访问 URL
func buildPublicURL(service *model.Service, ip string, port int) string {
	if service.TLS {
		return fmt.Sprintf("https://%s:%d", ip, port)
	}
	return fmt.Sprintf("http://%s:%d", ip, port)
}

// stunDial STUN 握手，返回连接、本地端口、公网 IP、公网端口
func stunDial(ctx context.Context, protocol string) (net.Conn, uint16, string, int, error) {
	localAddr := fmt.Sprintf("%s:0", global.StunConfig.LocalIP)

	dialCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	type dialResult struct {
		conn net.Conn
		err  error
	}
	ch := make(chan dialResult, 1)
	go func() {
		conn, err := reuseport.Dial(protocol, localAddr, global.StunConfig.BestSTUN)
		ch <- dialResult{conn, err}
	}()

	var stunConn net.Conn
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, 0, "", 0, fmt.Errorf("STUN 拨号失败: %w", r.err)
		}
		stunConn = r.conn
	case <-dialCtx.Done():
		return nil, 0, "", 0, fmt.Errorf("STUN 拨号超时")
	}

	var localPort uint16
	switch addr := stunConn.LocalAddr().(type) {
	case *net.TCPAddr:
		localPort = uint16(addr.Port)
	case *net.UDPAddr:
		localPort = uint16(addr.Port)
	}

	stunConn.SetDeadline(time.Now().Add(5 * time.Second))
	var publicIP string
	var publicPort int
	var err error
	if protocol == "tcp" {
		publicIP, publicPort, err = doTcpStunHandshake(stunConn)
	} else {
		udpConn := stunConn.(*net.UDPConn)
		stunServerAddr, _ := net.ResolveUDPAddr("udp", global.StunConfig.BestSTUN)
		publicIP, publicPort, err = doUDPStunHandshake(udpConn, stunServerAddr)
	}
	stunConn.SetDeadline(time.Time{})

	if err != nil {
		stunConn.Close()
		return nil, 0, "", 0, fmt.Errorf("STUN 握手失败: %w", err)
	}

	return stunConn, localPort, publicIP, publicPort, nil
}

// listenReusePort 端口复用监听
func listenReusePort(protocol, ip string, port uint16) (net.Listener, error) {
	return reuseport.Listen(protocol, fmt.Sprintf("%s:%d", ip, port))
}

// sendTCPHeartbeatConn 发 STUN Binding Request 保活
func sendTCPHeartbeatConn(conn net.Conn) error {
	msg := pionStun.MustBuild(pionStun.TransactionID, pionStun.BindingRequest)
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})
	_, err := conn.Write(msg.Raw)
	return err
}

// RunForwardLoop 接受外部连接并转发到内网目标
func RunForwardLoop(ctx context.Context, listener net.Listener, targetAddr, protocol, serviceName string) {
	for {
		clientConn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logrus.Warnf("[%s] Accept 失败: %v", serviceName, err)
			return
		}
		logrus.Infof("[%s] 新连接: %s", serviceName, clientConn.RemoteAddr())
		go Forward(clientConn, targetAddr, protocol)
	}
}

// serviceHealthCheck 根据协议选择合适的检查方式
func serviceHealthCheck(service *model.Service, publicURL, publicIP string, publicPort int) bool {
	switch strings.ToLower(service.Protocol) {
	case "ssh":
		return sshConnectCheck(publicIP, publicPort, 3*time.Second)
	case "http", "https":
		return httpCheckWithRetry(publicURL, 2, 3*time.Second)
	default:
		return tcpConnectCheck(publicIP, publicPort, 3*time.Second)
	}
}

func tcpConnectCheck(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), timeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func sshConnectCheck(host string, port int, timeout time.Duration) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), timeout)
	if err != nil {
		return false
	}
	defer conn.Close()
	buf := make([]byte, 64)
	conn.SetReadDeadline(time.Now().Add(timeout))
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		return false
	}
	return strings.HasPrefix(string(buf[:n]), "SSH-")
}

func httpCheckWithRetry(url string, maxRetries int, timeout time.Duration) bool {
	tr := &http.Transport{
		TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for i := 0; i < maxRetries; i++ {
		resp, err := client.Get(url)
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if err == nil && resp.StatusCode > 0 {
			return true
		}
	}
	return false
}

// ── 以下函数供 stun.go 里的 RunStunTunnel / RunStunTunnelWithContext 调用 ──

// tcpStunHealthCheck TCP STUN 健康检测（原有逻辑保留）
func tcpStunHealthCheck(stunConn net.Conn, publicURL string, publicIP string, expectedPublicPort int, localPort uint16, service *model.Service) error {
	healthTicker := time.NewTicker(280 * time.Second)
	defer healthTicker.Stop()

	time.Sleep(6 * time.Second)
	serviceHealthCheck(service, publicURL, publicIP, expectedPublicPort)

	currentStunConn := stunConn
	logrus.Info("启动TCP健康检查 间隔28s")

	for range healthTicker.C {
		if serviceHealthCheck(service, publicURL, publicIP, expectedPublicPort) {
			continue
		}

		_, port, err := doTcpStunHandshake(currentStunConn)
		if err != nil {
			logrus.Infof("STUN连接断开，尝试重连...")
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
				return fmt.Errorf("公网端口漂移 %d -> %d", expectedPublicPort, newPort)
			}
			logrus.Infof("✅ STUN重连成功，端口保持 %d", newPort)
			currentStunConn = newConn
			continue
		}

		if port != expectedPublicPort {
			return fmt.Errorf("公网端口漂移 %d -> %d，需要重新打洞", expectedPublicPort, port)
		}
		logrus.Warn("STUN端口正常但服务检查持续失败，可能是上游服务问题")
	}
	return nil
}

// udpStunHealthCheck UDP STUN 健康检测（原有逻辑保留）
func udpStunHealthCheck(udpConn *net.UDPConn, stunServer *net.UDPAddr, expectedPublicPort int, localPort uint16) error {
	healthTicker := time.NewTicker(280 * time.Second)
	defer healthTicker.Stop()

	consecutiveFailures := 0
	maxFailures := 3
	currentConn := udpConn
	logrus.Info("启动UDP健康检查 间隔28s")

	for range healthTicker.C {
		_, port, err := doUDPStunHandshake(currentConn, stunServer)
		if err != nil {
			consecutiveFailures++
			logrus.Warnf("UDP STUN检查失败 (%d/%d): %v", consecutiveFailures, maxFailures, err)
			if consecutiveFailures >= maxFailures {
				currentConn.Close()
				localAddr := &net.UDPAddr{IP: net.ParseIP(global.StunConfig.LocalIP), Port: int(localPort)}
				newConn, err := net.ListenUDP("udp", localAddr)
				if err != nil {
					return fmt.Errorf("UDP重建失败: %v", err)
				}
				_, newPort, err := doUDPStunHandshake(newConn, stunServer)
				if err != nil {
					newConn.Close()
					return fmt.Errorf("重建后STUN验证失败: %v", err)
				}
				if newPort != expectedPublicPort {
					newConn.Close()
					return fmt.Errorf("公网端口漂移 %d -> %d", expectedPublicPort, newPort)
				}
				logrus.Infof("✅ UDP重建成功，端口保持 %d", newPort)
				currentConn = newConn
				consecutiveFailures = 0
			}
			continue
		}
		if port != expectedPublicPort {
			return fmt.Errorf("公网端口变化 %d -> %d", expectedPublicPort, port)
		}
		consecutiveFailures = 0
	}
	return nil
}

// httpCheckOK 单次 HTTP 检查（供旧代码兼容）
func httpCheckOK(url string, timeout time.Duration) bool {
	tr := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	client := http.Client{
		Timeout:   timeout,
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode > 0
}

// SendTCPHeartbeat 发送TCP STUN 心跳包（供旧代码调用）
func SendTCPHeartbeat(conn net.Conn) error {
	msg := pionStun.MustBuild(pionStun.TransactionID, pionStun.BindingRequest)
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})
	_, err := conn.Write(msg.Raw)
	if err != nil {
		return fmt.Errorf("发送心跳包失败")
	}
	return nil
}

// SendUdpHeartbeat 发送UDP STUN 心跳包（供旧代码调用）
func SendUdpHeartbeat(conn *net.UDPConn, stunServer *net.UDPAddr) error {
	msg := pionStun.MustBuild(pionStun.TransactionID, pionStun.BindingRequest)
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetWriteDeadline(time.Time{})
	_, err := conn.WriteToUDP(msg.Raw, stunServer)
	if err != nil {
		return fmt.Errorf("发送UDP心跳失败: %v", err)
	}
	return nil
}
