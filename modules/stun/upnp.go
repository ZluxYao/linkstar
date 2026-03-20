package stun

import (
	"context"
	"fmt"
	"linkstar/global"
	"linkstar/modules/stun/model"
	"strings"
	"sync"

	"github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/huin/goupnp/dcps/internetgateway2"
	"github.com/sirupsen/logrus"
)

// 全局队列，在程序启动时初始化一次
var upnpQueue = NewUpnpQueue(10)

// unppTask upnp单个任务
type upnpTask struct {
	fn       func() error
	resultCh chan error
}

// unnpQueue upnp队列
type UpnpQueue struct {
	taskCh chan upnpTask
	once   sync.Once
	cancel context.CancelFunc
}

// // upnp网关
// type UpnpGateway struct {
// 	DefaultGateway string // 默认使用的网关类型

// 	DefaultV2    *internetgateway2.WANIPConnection1
// 	DefaultV1    *internetgateway1.WANIPConnection1
// 	DefaultV2ppp *internetgateway2.WANPPPConnection1
// 	DefaultV1ppp *internetgateway1.WANPPPConnection1

// 	V2    []*internetgateway2.WANIPConnection1
// 	V1    []*internetgateway1.WANIPConnection1
// 	V2ppp []*internetgateway2.WANPPPConnection1
// 	V1ppp []*internetgateway1.WANPPPConnection1
// }

// 发现网关
func DiscoverUPnPGateway() *model.UpnpGateway {
	gw := &model.UpnpGateway{}

	if clients, _, err := internetgateway1.NewWANIPConnection1Clients(); err == nil {
		gw.V1 = clients
	}

	if clients, _, err := internetgateway2.NewWANIPConnection1Clients(); err == nil {
		gw.V2 = clients
	}

	if clients, _, err := internetgateway1.NewWANPPPConnection1Clients(); err == nil {
		gw.V1ppp = clients
	}

	if clients, _, err := internetgateway2.NewWANPPPConnection1Clients(); err == nil {
		gw.V2ppp = clients
	}

	if len(gw.V1)+len(gw.V2)+len(gw.V1ppp)+len(gw.V2ppp) == 0 {
		logrus.Warnf("未发现upnp网关")
		return gw
	}

	logrus.Infof("发现UPnP网关: IGDv1=%d, IGDv2=%d, IGDv1-PPP=%d, IGDv2-PPP=%d",
		len(gw.V1), len(gw.V2), len(gw.V1ppp), len(gw.V2ppp))

	return gw
}

// 选择默认网关
func SelectDefaultGateway(gw *model.UpnpGateway) {
	// 获取本机ip
	localIP, err := GetLocalIP()
	if err != nil {
		logrus.Error("获取本机ip失败")
		return
	}

	// 获取本机前三段的ip，家庭宽带普遍路由器子网掩码255.255.255.0
	localPrefix := ipPrefix(localIP)

	// 按顺序设置默认upnp网关
	for _, client := range gw.V2 {
		ext, _ := client.GetExternalIPAddress() // 这里忽略err，有些设备不upnp不支持返回外部ip
		if strings.HasPrefix(client.Location.Hostname(), localPrefix) {
			gw.DefaultV2 = client
			gw.DefaultGateway = "IGDv2"
			logrus.Infof("选择默认网关IDGv2 外部ip：%s  内部ip:%s", ext, client.Location.Hostname())
			return
		}
	}

	for _, client := range gw.V1 {
		ext, _ := client.GetExternalIPAddress()
		if strings.HasPrefix(client.Location.Hostname(), localPrefix) {
			gw.DefaultV1 = client
			gw.DefaultGateway = "IGDv1"
			logrus.Infof("选择默认网关IDGv1 外部ip：%s  内部ip:%s", ext, client.Location.Hostname())
			return
		}
	}

	for _, client := range gw.V2ppp {
		ext, _ := client.GetExternalIPAddress()
		if strings.HasPrefix(client.Location.Hostname(), localPrefix) {
			gw.DefaultV2ppp = client
			gw.DefaultGateway = "IGDv2ppp"
			logrus.Infof("选择默认网关IDGv2ppp 外部ip：%s  内部ip:%s", ext, client.Location.Hostname())
			return
		}
	}

	for _, client := range gw.V1ppp {
		ext, _ := client.GetExternalIPAddress()
		if strings.HasPrefix(client.Location.Hostname(), localPrefix) {
			gw.DefaultV1ppp = client
			gw.DefaultGateway = "IGDv1ppp"
			logrus.Infof("选择默认网关IDGv1ppp 外部ip：%s  内部ip:%s", ext, client.Location.Hostname())
			return
		}
	}

	// 没找到同网段的网关
	logrus.Warn("没找到同网段的网关，使用第一个可用网关")
	switch {
	case len(gw.V2) > 0:
		gw.DefaultV2 = gw.V2[0]
		gw.DefaultGateway = "IGDv2"
	case len(gw.V1) > 0:
		gw.DefaultV1 = gw.V1[0]
		gw.DefaultGateway = "IGDv1"
	case len(gw.V2ppp) > 0:
		gw.DefaultV2ppp = gw.V2ppp[0]
		gw.DefaultGateway = "IGDv2ppp"
	case len(gw.V1ppp) > 0:
		gw.DefaultV1ppp = gw.V1ppp[0]
		gw.DefaultGateway = "IGDv1ppp"

	}

}

// "192.168.1.100" -> "192.168.1"  前三段
func ipPrefix(ip string) string {
	parts := strings.Split(ip, ".")
	if len(parts) > 3 {
		return parts[0] + "." + parts[1] + "." + parts[2]
	}
	return ip
}

// NewUpnpQueue 创建并启动队列
func NewUpnpQueue(bufSize int) *UpnpQueue {
	ctx, cancel := context.WithCancel(context.Background())
	q := &UpnpQueue{
		taskCh: make(chan upnpTask, bufSize), // 创建任务通道
		cancel: cancel,
	}

	go q.worker(ctx)
	return q
}

// 串行消费
func (q *UpnpQueue) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done(): //收到信号退出
			return
		case task := <-q.taskCh:
			task.resultCh <- task.fn() //结果写到返回通道
		}

	}
}

// 提交任务
func (q *UpnpQueue) submit(ctx context.Context, fn func() error) error {
	resultCh := make(chan error, 1)
	task := upnpTask{fn: fn, resultCh: resultCh}

	select {
	case q.taskCh <- task: //任务入队列
	case <-ctx.Done():
		return ctx.Err() // 调用方已放弃
	}

	select {
	case err := <-resultCh: //等待worker完成
		return err
	case <-ctx.Done(): // 超时或者取消，不在等待结构
		return ctx.Err()
	}

}

// stop 优雅停止
func (q *UpnpQueue) stop() {
	q.once.Do(q.cancel)
}

// 添加Upnp端口映射队列
func AddPortMappingQueue(ctx context.Context, externalPort, internalPort uint16, protocol, description string) error {
	return upnpQueue.submit(ctx, func() error {
		return AddPortMapping(externalPort, internalPort, protocol, description)
	})
}

// 删除Upnp端口映射
func DeletePortMappingSave(ctx context.Context, externalPort uint16, protocol string) error {
	return upnpQueue.submit(ctx, func() error {
		return DeletePortMapping(externalPort, protocol)
	})
}

// 添加UPNP端口映射
func AddPortMapping(externalPort, internalPort uint16, protocol, description string) error {

	logrus.Infof("尝试添加端口映射: 外部端口 %d -> 内部端口 %d (%s)", externalPort, internalPort, protocol)

	gw := global.UpnpGateway
	if gw.DefaultGateway == "" {
		return fmt.Errorf("未初始化网关")
	}

	var err error
	switch gw.DefaultGateway {
	case "IGDv2":
		err = gw.DefaultV2.AddPortMapping(
			"",                        // NewRemoteHost: 空字符串表示接受来自任意IP的连接
			externalPort,              // NewExternalPort: 外网端口号
			protocol,                  // NewProtocol: "TCP" 或 "UDP"
			internalPort,              // NewInternalPort: 内网端口号
			global.StunConfig.LocalIP, // NewInternalClient: 内网目标IP（本机IP）
			true,                      // NewEnabled: 是否启用此映射
			description,               // NewPortMappingDescription: 映射说明
			uint32(0),                 // NewLeaseDuration: 租期，0表示永久有效
		)
	case "IGDv1":
		err = gw.DefaultV1.AddPortMapping(
			"",
			externalPort,
			protocol,
			internalPort,
			global.StunConfig.LocalIP,
			true,
			description,
			uint32(0),
		)
	case "IGDv2ppp":
		err = gw.DefaultV2ppp.AddPortMapping(
			"",
			externalPort,
			protocol,
			internalPort,
			global.StunConfig.LocalIP,
			true,
			description,
			uint32(0),
		)
	case "IGDv1ppp":
		err = gw.DefaultV1ppp.AddPortMapping(
			"",
			externalPort,
			protocol,
			internalPort,
			global.StunConfig.LocalIP,
			true,
			description,
			uint32(0),
		)
	default:
		return fmt.Errorf("没有可用的默认UPnP网关")
	}
	if err != nil {
		return fmt.Errorf("添加端口映射失败 [%s]: %w", gw.DefaultGateway, err)
	}

	logrus.Infof("端口映射添加成功! 外部:%d -> 内部:%d (%s)", externalPort, internalPort, protocol)
	return nil
}

// // 先尝试旧版协议
// clients1, _, err := internetgateway1.NewWANIPConnection1Clients()
// if err == nil && len(clients1) > 0 {
// 	logrus.Infof("使用 Internet Gateway Device v1")
// 	for _, client := range clients1 {
// 		err = client.AddPortMapping(
// 			"",                        // NewRemoteHost: 空字符串表示接受来自任意IP的连接
// 			externalPort,              // NewExternalPort: 外网端口号
// 			protocol,                  // NewProtocol: "TCP" 或 "UDP"
// 			internalPort,              // NewInternalPort: 内网端口号
// 			global.StunConfig.LocalIP, // NewInternalClient: 内网目标IP（本机IP）
// 			true,                      // NewEnabled: 是否启用此映射
// 			description,               // NewPortMappingDescription: 映射说明
// 			uint32(0),                 // NewLeaseDuration: 租期，0表示永久有效
// 		)
// 		if err != nil {
// 			logrus.Infof("IGDv1 添加失败: %v，尝试下一个客户端\n", err)
// 			continue
// 		}
// 		logrus.Info("✓ 端口映射添加成功!")
// 		return nil
// 	}
// }

func DeletePortMapping(externalPort uint16, protocol string) error {

	gw := global.UpnpGateway
	if gw.DefaultGateway == "" {
		return fmt.Errorf("未初始化网关")
	}

	var err error
	switch gw.DefaultGateway {
	case "IGDv2":
		err = gw.DefaultV2.DeletePortMapping("", externalPort, protocol)
	case "IGDv1":
		err = gw.DefaultV1.DeletePortMapping("", externalPort, protocol)
	case "IGDv2ppp":
		err = gw.DefaultV2ppp.DeletePortMapping("", externalPort, protocol)
	case "IGDv1ppp":
		err = gw.DefaultV1ppp.DeletePortMapping("", externalPort, protocol)
	default:
		return fmt.Errorf("没有可用的默认UPnP网关")
	}
	if err != nil {
		return fmt.Errorf("删除端口映射失败 [%s]: %w", gw.DefaultGateway, err)
	}

	logrus.Infof("端口映射删除成功! 内部:%d (%s)", externalPort, protocol)
	return nil
}

// // 使用IGDv1 删除
// clients, _, err := internetgateway1.NewWANIPConnection1Clients()
// if err == nil && len(clients) > 0 {
// 	for _, client := range clients {
// 		// 删除端口只需要外网端口和协议类型
// 		err = client.DeletePortMapping(
// 			"",
// 			externalPort,
// 			protocol,
// 		)
// 		if err == nil {
// 			logrus.Infof("✓ %d端口映射删除成功!", externalPort)
// 			return nil
// 		}
// 	}
// }
