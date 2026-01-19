package stun

import (
	"fmt"
	"linkstar/global"

	"github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/huin/goupnp/dcps/internetgateway2"
	"github.com/sirupsen/logrus"
)

// 添加UPNP端口映射
func AddPortMapping(externalPort, internalPort uint16, protocol, description string) error {

	logrus.Infof("尝试添加端口映射: 外部端口 %d -> 内部端口 %d (%s)", externalPort, internalPort, protocol)

	// 先尝试旧版协议
	clients1, _, err := internetgateway1.NewWANIPConnection1Clients()
	if err == nil && len(clients1) > 0 {
		logrus.Infof("使用 Internet Gateway Device v1")
		for _, client := range clients1 {
			err = client.AddPortMapping(
				"",                        // NewRemoteHost: 空字符串表示接受来自任意IP的连接
				externalPort,              // NewExternalPort: 外网端口号
				protocol,                  // NewProtocol: "TCP" 或 "UDP"
				internalPort,              // NewInternalPort: 内网端口号
				global.StunConfig.LocalIP, // NewInternalClient: 内网目标IP（本机IP）
				true,                      // NewEnabled: 是否启用此映射
				description,               // NewPortMappingDescription: 映射说明
				uint32(0),                 // NewLeaseDuration: 租期，0表示永久有效
			)
			if err != nil {
				logrus.Infof("IGDv1 添加失败: %v，尝试下一个客户端\n", err)
				continue
			}
			logrus.Info("✓ 端口映射添加成功!")
			return nil
		}
	}

	clients1ppp, _, err := internetgateway1.NewWANPPPConnection1Clients()
	if err == nil && len(clients1ppp) > 0 {
		logrus.Infof("使用 Internet Gateway Device v1 (PPP)")
		for _, client := range clients1ppp {
			err = client.AddPortMapping(
				"",
				externalPort,
				protocol,
				internalPort,
				global.StunConfig.LocalIP,
				true,
				description,
				uint32(0),
			)
			if err != nil {
				logrus.Infof("IGDv1 PPP 添加失败: %v，尝试下一个客户端\n", err)
				continue
			}
			logrus.Infof("✓ 端口映射添加成功!")
			return nil
		}
	}

	clients2, _, err := internetgateway2.NewWANPPPConnection1Clients()
	if err == nil && len(clients2) > 0 {
		logrus.Infof("使用 Internet Gateway Device v2")
		for _, client := range clients1 {
			err = client.AddPortMapping(
				"",                        // NewRemoteHost: 空字符串表示接受来自任意IP的连接
				externalPort,              // NewExternalPort: 外网端口号
				protocol,                  // NewProtocol: "TCP" 或 "UDP"
				internalPort,              // NewInternalPort: 内网端口号
				global.StunConfig.LocalIP, // NewInternalClient: 内网目标IP（本机IP）
				true,                      // NewEnabled: 是否启用此映射
				description,               // NewPortMappingDescription: 映射说明
				uint32(0),                 // NewLeaseDuration: 租期，0表示永久有效
			)
			if err != nil {
				logrus.Infof("IGDv2 添加失败: %v，尝试下一个客户端\n", err)
				continue
			}
			logrus.Info("✓ 端口映射添加成功!")
			return nil
		}
	}

	clients2ppp, _, err := internetgateway2.NewWANPPPConnection1Clients()
	if err == nil && len(clients2ppp) > 0 {
		logrus.Infof("使用 Internet Gateway Device v2 (PPP)")
		for _, client := range clients1ppp {
			err = client.AddPortMapping(
				"",
				externalPort,
				protocol,
				internalPort,
				global.StunConfig.LocalIP,
				true,
				description,
				uint32(0),
			)
			if err != nil {
				logrus.Infof("IGDv2 PPP 添加失败: %v，尝试下一个客户端\n", err)
				continue
			}
			logrus.Infof("✓ 端口映射添加成功!")
			return nil
		}
	}

	// 所有方法都失败，返回错误
	return fmt.Errorf("未找到可用的UPnP网关设备或所有尝试均失败")
}

func DeletePortMapping(externalPort uint16, protocol string) error {
	// 使用IGDv1 删除
	clients, _, err := internetgateway1.NewWANIPConnection1Clients()
	if err == nil && len(clients) > 0 {
		for _, client := range clients {
			// 删除端口只需要外网端口和协议类型
			err = client.DeletePortMapping(
				"",
				externalPort,
				protocol,
			)
			if err == nil {
				logrus.Infof("✓ %d端口映射删除成功!", externalPort)
				return nil
			}
		}
	}

	// 使用IGDv1 删除
	clients2, _, err := internetgateway2.NewWANIPConnection1Clients()
	if err == nil && len(clients) > 0 {
		for _, client := range clients2 {
			// 删除端口只需要外网端口和协议类型
			err = client.DeletePortMapping(
				"",
				externalPort,
				protocol,
			)
			if err == nil {
				logrus.Infof("✓ %d端口映射删除成功!", externalPort)
				return nil
			}
		}
	}

	return fmt.Errorf("删除端口映射失败")
}
