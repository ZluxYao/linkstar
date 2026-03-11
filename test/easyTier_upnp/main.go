package main

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/huin/goupnp/dcps/internetgateway2"
)

// 强制使用的网关 IP (iStoreOS)
const forcedGatewayIP = "10.126.126.1"

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
	fmt.Printf("强制网关IP: %s\n", forcedGatewayIP)
	fmt.Printf("尝试添加端口映射: 外部端口 %d -> 内部端口 %d (%s)\n", externalPort, internalPort, protocol)

	// 先尝试通过强制网关的 UPnP
	fmt.Printf("尝试通过强制网关 %s 发现 UPnP 设备...\n", forcedGatewayIP)
	rootURL := "http://" + forcedGatewayIP + ":1900/rootDesc.xml"

	// 尝试通过 HTTP 获取设备描述
	resp, err := http.Get(rootURL)
	if err != nil {
		fmt.Printf("无法连接到 %s: %v\n", rootURL, err)
	} else {
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("HTTP 请求成功, 状态码: %d, Body长度: %d\n", resp.StatusCode, len(body))

		// 解析设备描述，查找服务URL
		if serviceURL := findServiceURL(string(body), forcedGatewayIP); serviceURL != "" {
			fmt.Printf("找到 WANIPConnection 服务: %s\n", serviceURL)
			// 尝试直接发送 SOAP 请求
			if err := addPortMappingBySOAP(serviceURL, externalPort, internalPort, protocol, localIP, description); err == nil {
				fmt.Println("✓ 端口映射添加成功 (通过强制网关)!")
				return nil
			}
		}
	}

	// 如果强制网关失败，回退到自动发现
	fmt.Println("强制网关失败，回退到自动发现...")
	return addPortMappingAuto(externalPort, internalPort, protocol, description, localIP)
}

// findServiceURL 从设备描述 XML 中查找 WANIPConnection 服务 URL
func findServiceURL(xmlContent string, gatewayIP string) string {
	// 简单的字符串查找，查找 serviceType 为 WANIPConnection 的 controlURL
	lines := strings.Split(xmlContent, "\n")
	for i, line := range lines {
		if strings.Contains(line, "urn:upnp-org:serviceType:WANIPConnection") ||
			strings.Contains(line, "urn:schemas-upnp-org:service:WANIPConnection") {
			// 向上查找 controlURL
			for j := i - 1; j >= 0 && j >= i-10; j-- {
				if strings.Contains(lines[j], "controlURL") {
					// 提取 URL
					start := strings.Index(lines[j], ">")
					end := strings.Index(lines[j], "</")
					if start > 0 && end > start {
						url := strings.TrimSpace(lines[j][start+1 : end])
						if !strings.HasPrefix(url, "http") {
							// 相对路径，需要补全
							url = "http://" + gatewayIP + ":1900" + url
						}
						return url
					}
				}
			}
		}
	}
	return ""
}

// addPortMappingBySOAP 直接发送 SOAP 请求添加端口映射
func addPortMappingBySOAP(controlURL string, externalPort, internalPort uint16, protocol, internalClient, description string) error {
	soapRequest := fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
  <s:Body>
    <u:AddPortMapping xmlns:u="urn:schemas-upnp-org:service:WANIPConnection:1">
      <NewRemoteHost></NewRemoteHost>
      <NewExternalPort>%d</NewExternalPort>
      <NewProtocol>%s</NewProtocol>
      <NewInternalPort>%d</NewInternalPort>
      <NewInternalClient>%s</NewInternalClient>
      <NewEnabled>1</NewEnabled>
      <NewPortMappingDescription>%s</NewPortMappingDescription>
      <NewLeaseDuration>0</NewLeaseDuration>
    </u:AddPortMapping>
  </s:Body>
</s:Envelope>`, externalPort, protocol, internalPort, internalClient, description)

	req, err := http.NewRequest("POST", controlURL, bytes.NewBufferString(soapRequest))
	if err != nil {
		return fmt.Errorf("创建请求失败: %v", err)
	}

	req.Header.Set("Content-Type", "text/xml; charset=\"utf-8\"")
	req.Header.Set("SOAPACTION", "urn:schemas-upnp-org:service:WANIPConnection:1#AddPortMapping")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("发送请求失败: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Printf("SOAP 响应: %s\n", body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("SOAP 请求失败, 状态码: %d", resp.StatusCode)
}

// addPortMappingAuto 自动发现端口映射 (回退方案)
func addPortMappingAuto(externalPort, internalPort uint16, protocol, description string, localIP string) error {
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

	// 尝试 IGDv2
	clients, _, err := internetgateway2.NewWANIPConnection2Clients()
	if err == nil && len(clients) > 0 {
		fmt.Println("使用 Internet Gateway Device v2")
		for _, client := range clients {
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
	// 添加 TCP 3333 端口映射
	fmt.Println("========== 添加 TCP 端口映射 ==========")
	err := addPortMapping(3333, 3333, "TCP", "EasyTier UPnP Test TCP")
	if err != nil {
		log.Printf("添加 TCP 端口映射失败: %v\n", err)
	}

	time.Sleep(1 * time.Second)

	// 添加 UDP 3333 端口映射
	fmt.Println("\n========== 添加 UDP 端口映射 ==========")
	err = addPortMapping(3333, 3333, "UDP", "EasyTier UPnP Test UDP")
	if err != nil {
		log.Printf("添加 UDP 端口映射失败: %v\n", err)
	}

	fmt.Println("\n端口映射配置完成!")

	// 如果需要删除端口映射，可以使用以下代码:
	fmt.Println("\n========== 删除端口映射 ==========")
	deletePortMapping(3333, "TCP")
	deletePortMapping(3333, "UDP")
}
