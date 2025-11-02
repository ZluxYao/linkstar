package main

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway1"
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

	clients, _, err := internetgateway1.NewWANIPConnection1Clients()
	if err != nil {
		return fmt.Errorf("获取UPnP客户端失败: %v", err)
	}

	if len(clients) == 0 {
		return fmt.Errorf("未找到可用的UPnP网关设备")
	}

	// 尝试所有客户端，忽略718冲突错误
	for i, client := range clients {
		err = client.AddPortMapping(
			"",           // NewRemoteHost
			externalPort, // NewExternalPort
			protocol,     // NewProtocol
			internalPort, // NewInternalPort
			localIP,      // NewInternalClient
			true,         // NewEnabled
			description,  // NewPortMappingDescription
			uint32(0),    // NewLeaseDuration (0表示永久)
		)

		if err == nil {
			fmt.Printf("✓ 端口映射添加成功!\n")
			return nil
		}

		// 只记录非冲突错误
		if err.Error() != "SOAP fault. Code: s:Client | Explanation: UPnPError | Detail: <UPnPError xmlns=\"urn:schemas-upnp-org:control-1-0\"><errorCode>718</errorCode><errorDescription>ConflictInMappingEntry</errorDescription></UPnPError>" {
			log.Printf("客户端 %d 失败: %v\n", i+1, err)
		}
	}

	return fmt.Errorf("所有客户端均失败")
}

// deletePortMapping 删除端口映射
func deletePortMapping(externalPort uint16, protocol string) error {
	clients, _, err := internetgateway1.NewWANIPConnection1Clients()
	if err != nil {
		return fmt.Errorf("获取UPnP客户端失败: %v", err)
	}

	for _, client := range clients {
		err = client.DeletePortMapping("", externalPort, protocol)
		if err == nil {
			fmt.Printf("✓ 端口 %d (%s) 映射删除成功!\n", externalPort, protocol)
			return nil
		}
	}

	return fmt.Errorf("删除端口映射失败")
}

func main() {
	port := 63699
	// 添加 TCP 端口映射
	fmt.Println("========== 添加 TCP 端口映射 ==========")
	err := addPortMapping(uint16(port), 3333, "TCP", "Custom Port 3334 TCP")
	if err != nil {
		log.Printf("添加 TCP 端口映射失败: %v\n", err)
	}

	time.Sleep(1 * time.Second)

	// 添加 UDP 端口映射
	fmt.Println("\n========== 添加 UDP 端口映射 ==========")
	err = addPortMapping(uint16(port), 3333, "UDP", "Custom Port 3333 UDP")
	if err != nil {
		log.Printf("添加 UDP 端口映射失败: %v\n", err)
	}

	fmt.Println("\n端口映射配置完成!")

	// 删除端口映射
	fmt.Println("\n========== 删除端口映射 ==========")
	// deletePortMapping(3335, "UDP")
	// deletePortMapping(25185, "UDP")
	deletePortMapping(25185, "TCP")
	deletePortMapping(27579, "TCP")
	deletePortMapping(3355, "TCP")
	deletePortMapping(3355, "UDP")
	deletePortMapping(63699, "TCP")
	deletePortMapping(63699, "UDP")

}
