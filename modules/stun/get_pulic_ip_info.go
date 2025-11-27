package stun

import (
	"fmt"
	"linkstar/global"
	"net"
	"time"

	"github.com/pion/stun"
)

type PublicIPInfo struct {
	// 基础网络信息
	LocalIP     string `json:"localIP"`     // 本机内网IP
	PublicIP    string `json:"publicIP"`    // 真实公网IP
	RouterWanIP string `json:"routerWanIP"` // 路由器WAN口IP
	IsNAT       bool   `json:"isNat"`       // 是否多层NAT

}

// 获取网络基本信息
func GetPublicIPInfo() (*PublicIPInfo, error) {
	var info PublicIPInfo

	// 获取本机ip
	LocalIP, err := GetLocalIP()
	if err != nil {
		fmt.Printf("获取本机ip失败：%s \n", err)
	}
	info.LocalIP = LocalIP

	// 获取真实公网ip
	PublicIP, err := GetPublicIP()
	if err != nil {
		fmt.Printf("获取真实公网ip失败%s", err)
	}
	info.PublicIP = PublicIP

	fmt.Println(info)

	return &info, nil
}

// 获取本机ip
func GetLocalIP() (string, error) {
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

// 获取公网ip
func GetPublicIP() (string, error) {

	// 链接STUN服务器
	conn, err := net.DialTimeout("tcp4", global.StunConfig.BestSTUN, 3*time.Second) //指定tcp4
	if err != nil {
		return "", fmt.Errorf("连接STUN服务器失败: %w", err)
	}
	defer conn.Close()

	// 发送STUN请求
	msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	if _, err := conn.Write(msg.Raw); err != nil {
		return "", fmt.Errorf("发送STUN请求失败%s", err)
	}

	// 读取响应
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return "", fmt.Errorf("读取响应失败：%s", err)
	}

	// 解析响应
	var response stun.Message
	response.Raw = buf[:n]
	if err = response.Decode(); err != nil {
		return "", fmt.Errorf("解码stun失败%s", err)
	}

	var xorAddr stun.XORMappedAddress
	if err = xorAddr.GetFrom(&response); err != nil {
		return "", fmt.Errorf("获取映射地址失败: %s", err)

	}

	return xorAddr.IP.String(), nil
}
