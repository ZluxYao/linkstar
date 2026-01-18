package stun

import (
	"bufio"
	"fmt"
	"linkstar/modules/stun/model"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"time"

	"github.com/sirupsen/logrus"
)

// 预编译CIDR
var (
	_, private10, _  = net.ParseCIDR("10.0.0.0/8")
	_, private172, _ = net.ParseCIDR("172.16.0.0/12")
	_, private192, _ = net.ParseCIDR("192.168.0.0/16")
	_, cgnRange, _   = net.ParseCIDR("100.64.0.0/10")
	ipRegex          = regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)
)

// IP类型常量
const (
	IPTypePrivate = "private"
	IPTypeCGN     = "cgn"
	IPTypePublic  = "public"
)

func GetNatRouterList() ([]model.NatRouterInfo, error) {
	// 记录时间
	startTime := time.Now()

	// 全链路扫描
	logrus.Info("实时扫描网络层级")
	natChain, err := scanNATChain("114.114.114.114")
	if err != nil {
		return nil, err
	}

	// 计算总耗时
	endTime := time.Now()
	logrus.Infof("扫描耗时%vs", endTime.Sub(startTime))

	return natChain, nil
}

// scanNateChain 扫描NAT链路
func scanNATChain(target string) ([]model.NatRouterInfo, error) {
	cmd := buildTracerouteCmd(target)

	// 创建管道获取信息
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("创建管道失败: %w", err)
	}

	//启动tracert扫描
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动tracert失败:%w", err)
	}

	// 确保进程清理
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
	}()

	var natChain []model.NatRouterInfo
	scanner := bufio.NewScanner(stdout)
	level := uint(0)

	for scanner.Scan() {
		line := scanner.Text()

		// 提取ip
		ips := ipRegex.FindAllString(line, -1)
		if len(ips) == 0 {
			continue
		}

		ip := ips[0]
		// 跳过一个target
		if ip == target {
			continue
		}

		ipType := classifyIP(ip)

		// 只记录私网ip
		if ipType != IPTypePublic {
			level++
			natChain = append(natChain, model.NatRouterInfo{
				NatLevel: level,
				LanIp:    ip,
			})
			if ipType == IPTypeCGN {
				// 遇到cgn，立即终止
				logrus.Infof("探测到cgn出口: %s，终止扫描", ip)
				break
			}
		} else {
			// 遇到公网IP，立即终止
			logrus.Infof("探测到公网出口: %s，终止扫描", ip)
			break
		}
	}

	return natChain, nil
}

// buildTracerouteCmd 构建traceroute命令
func buildTracerouteCmd(target string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		// -d 不解析主机名, -h 10 最大跳数, -w 300 超时300ms
		return exec.Command("tracert", "-d", "-h", "10", "-w", "300", target)
	}
	// -n 不解析主机名, -m 10 最大跳数, -w 1 超时1秒, -q 1 每跳只测一次
	return exec.Command("traceroute", "-n", "-m", "10", "-w", "1", "-q", "1", target)
}

// classifyIP OP分类
func classifyIP(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return IPTypePrivate
	}
	if cgnRange.Contains(ip) {
		return IPTypeCGN
	}
	if private10.Contains(ip) || private172.Contains(ip) || private192.Contains(ip) {
		return IPTypePrivate
	}
	return IPTypePublic
}
