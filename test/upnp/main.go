package main

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/huin/goupnp/dcps/internetgateway2"
)

// getLocalIP 获取本机内网IP地址
func getLocalIP() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String(), nil
			}
		}
	}
	return "", fmt.Errorf("未找到本机IP地址")
}

// addPortMapping 添加端口映射
func addPortMapping(externalPort, internalPort uint16, protocol, description string) error {
	localIP, err := getLocalIP()
	if err != nil {
		return fmt.Errorf("获取本机IP失败: %v", err)
	}

	fmt.Printf("本机IP: %s\n", localIP)
	fmt.Printf("尝试添加端口映射: 外部端口 %d -> 内部端口 %d (%s)\n", externalPort, internalPort, protocol)

	// 尝试 IGDv1
	clients1, _, err := internetgateway1.NewWANIPConnection1Clients()
	if err == nil && len(clients1) > 0 {
		fmt.Println("使用 Internet Gateway Device v1")
		for _, client := range clients1 {
			fmt.Println(client)
			err = client.AddPortMapping(
				"",
				externalPort,
				protocol,
				internalPort,
				localIP,
				true,
				description,
				uint32(0),
			)
			if err != nil {
				log.Printf("IGDv1 添加失败: %v，尝试下一个客户端\n", err)
				continue
			}
			fmt.Println("✓ 端口映射添加成功!")
			return nil
		}
	}

	// 尝试 IGDv1 的 PPP 连接
	clients1ppp, _, err := internetgateway1.NewWANPPPConnection1Clients()
	if err == nil && len(clients1ppp) > 0 {
		fmt.Println("使用 Internet Gateway Device v1 (PPP)")
		for _, client := range clients1ppp {
			err = client.AddPortMapping(
				"",
				externalPort,
				protocol,
				internalPort,
				localIP,
				true,
				description,
				uint32(0),
			)
			if err != nil {
				log.Printf("IGDv1 PPP 添加失败: %v，尝试下一个客户端\n", err)
				continue
			}
			fmt.Println("✓ 端口映射添加成功!")
			return nil
		}
	}

	// 先尝试 IGDv2
	clients, _, err := internetgateway2.NewWANIPConnection2Clients()
	if err == nil && len(clients) > 0 {
		fmt.Println("使用 Internet Gateway Device v2")
		for _, client := range clients {
			err = client.AddPortMapping(
				"",           // NewRemoteHost (空字符串表示任意)
				externalPort, // NewExternalPort
				protocol,     // NewProtocol (TCP/UDP)
				internalPort, // NewInternalPort
				localIP,      // NewInternalClient
				true,         // NewEnabled
				description,  // NewPortMappingDescription
				uint32(0),    // NewLeaseDuration (0表示永久)
			)
			if err != nil {
				log.Printf("IGDv2 添加失败: %v，尝试下一个客户端\n", err)
				continue
			}
			fmt.Println("✓ 端口映射添加成功!")
			return nil
		}
	}

	// 尝试 IGDv2 的 PPP 连接
	clients2, _, err := internetgateway2.NewWANPPPConnection1Clients()
	if err == nil && len(clients2) > 0 {
		fmt.Println("使用 Internet Gateway Device v2 (PPP)")
		for _, client := range clients2 {
			err = client.AddPortMapping(
				"",
				externalPort,
				protocol,
				internalPort,
				localIP,
				true,
				description,
				uint32(0),
			)
			if err != nil {
				log.Printf("IGDv2 PPP 添加失败: %v，尝试下一个客户端\n", err)
				continue
			}
			fmt.Println("✓ 端口映射添加成功!")
			return nil
		}
	}

	return fmt.Errorf("未找到可用的UPnP网关设备或所有尝试均失败")
}

// deletePortMapping 删除端口映射
func deletePortMapping(externalPort uint16, protocol string) error {
	// 尝试 IGDv2
	clients, _, err := internetgateway2.NewWANIPConnection2Clients()
	if err == nil && len(clients) > 0 {
		for _, client := range clients {
			err = client.DeletePortMapping("", externalPort, protocol)
			if err == nil {
				fmt.Println("✓ 端口映射删除成功!")
				return nil
			}
		}
	}

	// 尝试 IGDv1
	clients1, _, err := internetgateway1.NewWANIPConnection1Clients()
	if err == nil && len(clients1) > 0 {
		for _, client := range clients1 {
			err = client.DeletePortMapping("", externalPort, protocol)
			if err == nil {
				fmt.Println("✓ 端口映射删除成功!")
				return nil
			}
		}
	}

	return fmt.Errorf("删除端口映射失败")
}

func main() {
	// 添加 TCP 333 端口映射
	fmt.Println("========== 添加 TCP 端口映射 ==========")
	err := addPortMapping(3339, 3339, "TCP", "Custom Port 333 TCP")
	if err != nil {
		log.Printf("添加 TCP 端口映射失败: %v\n", err)
	}

	time.Sleep(1 * time.Second)

	// 添加 UDP 333 端口映射
	fmt.Println("\n========== 添加 UDP 端口映射 ==========")
	err = addPortMapping(3339, 3339, "UDP", "Custom Port 333 UDP")
	if err != nil {
		log.Printf("添加 UDP 端口映射失败: %v\n", err)
	}

	fmt.Println("\n端口映射配置完成!")

	// 如果需要删除端口映射，可以使用以下代码:
	fmt.Println("\n========== 删除端口映射 ==========")
	deletePortMapping(3339, "TCP")
	deletePortMapping(3339, "UDP")
}
