package stun

import (
	"fmt"
	"io"
	"linkstar/global"
	"linkstar/modules/stun/model"
	"net"
	"time"

	"github.com/libp2p/go-reuseport"
	"github.com/pion/stun"
	"github.com/sirupsen/logrus"
)

// SetupDeviceServices 初始化设备服务
func SetupDeviceServices(device *model.Device) error {
	for i := range device.Services {
		svc := &device.Services[i]
		if !svc.Enabled {
			continue
		}
		// 建议：如果目标IP是本机且配置为跨网段IP（如192.168.1.x），这里可能需要逻辑判断
		// 但为了通用性，保持原样，在 forward 中处理连接错误
		go maintainService(device.IP, svc)
	}
	return nil
}

// maintainService 负责服务的守护运行
func maintainService(targetIP string, svc *model.Service) {
	logger := logrus.WithField("service", svc.Name)
	for {
		err := runTunnel(targetIP, svc)
		if err != nil {
			logger.Errorf("❌ 服务中断: %v", err)
		}
		logger.Info("⏳ 5秒后重试重新建立穿透...")
		time.Sleep(5 * time.Second)
	}
}

// runTunnel 实现了双层 NAT 穿透的核心逻辑
func runTunnel(targetIP string, svc *model.Service) error {
	// 1. STUN 拨号：使用 SO_REUSEPORT 在本地随机端口上建立连接
	// localAddr 使用 :0 让系统分配，但绑定在配置的 LocalIP 上
	localAddr := fmt.Sprintf("%s:0", global.StunConfig.LocalIP)
	stunConn, err := reuseport.Dial("tcp", localAddr, global.StunConfig.BestSTUN)
	if err != nil {
		return fmt.Errorf("STUN拨号失败 [%s]: %w", global.StunConfig.BestSTUN, err)
	}

	// 获取系统分配的本地随机端口
	localPort := uint16(stunConn.LocalAddr().(*net.TCPAddr).Port)

	// 2. STUN 握手：探测运营商 NAT 映射后的公网 IP 和 端口
	publicIP, publicPort, err := doStunHandshake(stunConn)
	if err != nil {
		stunConn.Close()
		return fmt.Errorf("STUN握手失败: %w", err)
	}

	// 3. 端口复用监听：在拨号用的同一个 localPort 上启动 TCP 监听
	listenAddr := fmt.Sprintf("%s:%d", global.StunConfig.LocalIP, localPort)
	listener, err := reuseport.Listen("tcp", listenAddr)
	if err != nil {
		stunConn.Close()
		return fmt.Errorf("端口监听失败: %w", err)
	}

	// 4. 配置路由器 UPnP (针对双层 NAT 的关键修复)
	// 必须异步执行，避免阻塞主流程，且允许 UPnP 失败（因为不是所有环境都支持）
	go func() {
		description := fmt.Sprintf("LinkStar-%s", svc.Name)
		// 外部端口和内部端口都必须是 localPort，因为运营商流量是指向这个端口的
		err := AddPortMapping(localPort, localPort, "TCP", description)
		if err != nil {
			// 仅作为警告，因为在单层 NAT 下不需要 UPnP 也能工作
			logrus.Warnf("[%s] UPnP 映射失败 (非致命): %v", svc.Name, err)
		} else {
			logrus.Infof("[%s] UPnP 映射成功: 路由器 WAN:%d -> 本机:%d", svc.Name, localPort, localPort)
		}
	}()

	logrus.Infof("🚀 [%s] 穿透就绪:", svc.Name)
	logrus.Infof("   🌍 访问地址: http://%s:%d", publicIP, publicPort)
	logrus.Infof("   🔄 链路: 公网:%d -> 路由器:%d -> 本机:%d -> 目标:%s:%d",
		publicPort, localPort, localPort, targetIP, svc.InternalPort)

	svc.ExternalPort = uint16(publicPort)

	// 定义资源清理闭包
	defer func() {
		logrus.Infof("[%s] 正在清理资源...", svc.Name)
		stunConn.Close()
		listener.Close()
		// 尝试删除 UPnP 映射
		go DeletePortMapping(localPort, "TCP")
	}()

	errCh := make(chan error, 2)

	// 5. 数据转发：接受来自监听端口的连接并转发给目标服务
	go func() {
		targetAddr := fmt.Sprintf("%s:%d", targetIP, svc.InternalPort)
		for {
			clientConn, err := listener.Accept()
			if err != nil {
				// 如果 listener 关闭了，Accept 会报错，属于正常退出流程
				errCh <- fmt.Errorf("监听器退出或Accept错误: %w", err)
				return
			}
			logrus.Infof("🔀 [%s] 收到外部连接: %s", svc.Name, clientConn.RemoteAddr())
			// 启动协程进行转发
			go forward(clientConn, targetAddr)
		}
	}()

	// 6. 持续保活：定期发送 STUN 请求
	// 这是维持 NAT 映射不被运营商关闭的关键
	go func() {
		errCh <- keepAlive(stunConn, publicPort, localPort)
	}()

	// 阻塞等待，直到发生错误（心跳失败或监听失败）
	return <-errCh
}

// doStunHandshake 执行一次 STUN 绑定请求
func doStunHandshake(conn net.Conn) (string, int, error) {
	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	if _, err := conn.Write(msg.Raw); err != nil {
		return "", 0, err
	}

	// 设置读取超时，防止永久阻塞
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer conn.SetReadDeadline(time.Time{}) // 清除超时设置

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

// forward 双向数据转发
func forward(src net.Conn, targetAddr string) {
	defer src.Close()

	// 增加连接超时控制，防止内网 IP 不可达导致协程堆积
	dst, err := net.DialTimeout("tcp", targetAddr, 3*time.Second)
	if err != nil {
		// 这里是你日志中 "i/o timeout" 的来源
		logrus.Errorf("❌ 连接内网目标失败 [%s]: %v (请检查: 1.目标服务是否启动 2.本机IP与目标IP是否跨网段)", targetAddr, err)
		return
	}
	defer dst.Close()

	// 使用通道或 WaitGroup 可以更优雅，但 io.Copy 配合 goroutine 足以处理简单的双向流
	go func() {
		_, _ = io.Copy(dst, src)
	}()
	_, _ = io.Copy(src, dst)
}

// keepAlive 维持 STUN 连接活跃 (带自动重连机制)
func keepAlive(initConn net.Conn, expectedPublicPort int, localPort uint16) error {
	// 心跳间隔
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	// 当前使用的连接
	currentConn := initConn
	defer func() {
		if currentConn != nil {
			currentConn.Close()
		}
	}()

	logrus.Info("💓 启动智能心跳保活机制...")

	for range ticker.C {
		// 1. 发送心跳包
		_, port, err := doStunHandshake(currentConn)

		// 2. 如果发生错误 (连接被切断/超时)
		if err != nil {
			logrus.Warnf("⚠️ STUN连接断开 (%v)，正在尝试原地重连...", err)

			// 关闭旧连接
			currentConn.Close()

			// === 核心修复: 尝试使用 SO_REUSEPORT 原地重连 ===
			// 必须绑定到原来的 localPort，这样才能维持 NAT 映射表
			localAddr := fmt.Sprintf("%s:%d", global.StunConfig.LocalIP, localPort)
			newConn, dialErr := reuseport.Dial("tcp", localAddr, global.StunConfig.BestSTUN)

			if dialErr != nil {
				// 重连都失败了，那才是真的断了
				return fmt.Errorf("重连失败，服务无法恢复: %w", dialErr)
			}

			// 重连成功，验证公网端口是否改变
			_, newPublicPort, stunErr := doStunHandshake(newConn)
			if stunErr != nil {
				newConn.Close()
				return fmt.Errorf("重连后STUN握手失败: %w", stunErr)
			}

			if newPublicPort != expectedPublicPort {
				newConn.Close()
				return fmt.Errorf("公网端口已漂移 %d -> %d (需要重启服务)", expectedPublicPort, newPublicPort)
			}

			// 完美恢复：端口没变，更新连接对象，继续循环
			logrus.Infof("✅ 原地重连成功! 公网端口仍为 %d, 业务未中断", newPublicPort)
			currentConn = newConn
			continue
		}

		// 3. 如果没有错误，检查端口是否一致
		if port != expectedPublicPort {
			return fmt.Errorf("公网端口发生变化 %d -> %d", expectedPublicPort, port)
		}

		// logrus.Debug("💓 心跳正常") // 调试时可开启
	}
	return nil
}
