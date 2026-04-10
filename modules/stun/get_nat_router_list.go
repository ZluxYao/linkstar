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

	// tracepath 行首的跳数编号（有冒号）: " 1:  192.168.100.1"
	tracepathHopRegex = regexp.MustCompile(`^\s*(\d+)\??:`)
	// traceroute 行首的跳数编号（无冒号）: " 1  192.168.100.1  0.248 ms"
	tracerouteHopRegex = regexp.MustCompile(`^\s*(\d+)\s+\d`)
)

// IP类型常量
const (
	IPTypePrivate = "private"
	IPTypeCGN     = "cgn"
	IPTypePublic  = "public"
)

func GetNatRouterList() ([]model.NatRouterInfo, error) {
	startTime := time.Now()

	logrus.Info("实时扫描网络层级")
	natChain, err := scanNATChain("114.114.114.114")
	if err != nil {
		return nil, err
	}

	endTime := time.Now()
	logrus.Infof("扫描耗时%vs", endTime.Sub(startTime))

	return natChain, nil
}

// scanNATChain 扫描NAT链路
func scanNATChain(target string) ([]model.NatRouterInfo, error) {
	cmd, isTracepath := buildTracerouteCmd(target)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("创建管道失败: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动tracert失败:%w", err)
	}

	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
	}()

	var natChain []model.NatRouterInfo
	scanner := bufio.NewScanner(stdout)
	level := uint(0)
	lastHopNum := -1

	for scanner.Scan() {
		line := scanner.Text()

		// Linux 下根据命令类型做行过滤与去重
		if runtime.GOOS == "linux" {
			if isTracepath {
				// tracepath 格式：" 1:  192.168.100.1" 或 " 1?: ..."
				m := tracepathHopRegex.FindStringSubmatch(line)
				if m == nil {
					continue
				}
				hopNum := 0
				fmt.Sscanf(m[1], "%d", &hopNum)
				if hopNum == lastHopNum {
					continue // 同一跳的重复行，跳过
				}
				lastHopNum = hopNum
			} else {
				// traceroute 格式：" 1  192.168.100.1  0.248 ms"
				if tracerouteHopRegex.FindString(line) == "" {
					continue // 过滤掉 header 行、* 超时行等无效行
				}
			}
		}

		// 提取IP
		ips := ipRegex.FindAllString(line, -1)
		if len(ips) == 0 {
			continue
		}

		ip := ips[0]
		if ip == target {
			continue
		}

		ipType := classifyIP(ip)

		if ipType != IPTypePublic {
			level++
			natChain = append(natChain, model.NatRouterInfo{
				NatLevel: level,
				LanIp:    ip,
			})
			if ipType == IPTypeCGN {
				logrus.Infof("探测到cgn出口: %s，终止扫描", ip)
				break
			}
		} else {
			logrus.Infof("探测到公网出口: %s，终止扫描", ip)
			break
		}
	}

	return natChain, nil
}

// buildTracerouteCmd 构建traceroute命令，返回命令及是否为tracepath
func buildTracerouteCmd(target string) (*exec.Cmd, bool) {
	switch runtime.GOOS {
	case "windows":
		// -d 不解析主机名, -h 10 最大跳数, -w 300 超时300ms
		return exec.Command("tracert", "-d", "-h", "10", "-w", "300", target), false
	case "linux":
		if _, err := exec.LookPath("traceroute"); err == nil {
			return exec.Command("traceroute", "-n", "-m", "10", "-w", "1", "-q", "1", target), false
		}
		return exec.Command("tracepath", "-n", "-m", "8", target), true
	default: // mac
		// -n 不解析主机名, -m 10 最大跳数, -w 1 超时1秒, -q 1 每跳只测一次
		return exec.Command("traceroute", "-n", "-m", "10", "-w", "1", "-q", "1", target), false
	}
}

// classifyIP IP分类
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
