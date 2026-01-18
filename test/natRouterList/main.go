package main

import (
	"bufio"
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"time"

	"github.com/pion/stun"
)

// 定义 IP 类型
const (
	IPTypePrivate = "🔒 私网 (本地路由)"
	IPTypeCGN     = "🏢 运营商NAT (CGN)"
	IPTypePublic  = "🌍 公网"
)

// 预编译 CIDR
var (
	_, private10, _  = net.ParseCIDR("10.0.0.0/8")
	_, private172, _ = net.ParseCIDR("172.16.0.0/12")
	_, private192, _ = net.ParseCIDR("192.168.0.0/16")
	_, cgnRange, _   = net.ParseCIDR("100.64.0.0/10")
)

func main() {
	fmt.Println("🚀 智能NAT链路探测 (实时极速版)\n")

	startTime := time.Now()

	// 1. 基础信息
	localIP := getLocalIP()
	publicIP := getPublicIP()
	fmt.Printf("📍 本地 IP: %s\n", localIP)
	fmt.Printf("🌐 公网 IP: %s\n", publicIP)

	// 2. 扫描链路
	fmt.Println("\n📡 正在分析网络层级 (实时扫描中)...")
	// 即使目标设定为20跳，只要中途遇到公网IP，我们会立即杀死进程，所以不用担心慢
	natChain := scanNATChain("114.114.114.114")

	endTime := time.Now()

	// 3. 输出最终简报
	printAnalysis(natChain)

	// 计算并输出耗时
	duration := endTime.Sub(startTime)
	fmt.Printf("\n⏱️  总耗时: %v (已优化)\n", duration)
}

// -------------------------------------------------------
// 核心逻辑 (重写版)
// -------------------------------------------------------

type NATHop struct {
	HopNum int
	IP     string
	Type   string
}

func scanNATChain(target string) []NATHop {
	// 准备命令，但不立即执行 CombinedOutput
	cmd := prepareTracerouteCmd(target)

	// 获取标准输出管道
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		fmt.Printf("❌ 无法创建管道: %v\n", err)
		return nil
	}

	// 启动命令
	if err := cmd.Start(); err != nil {
		fmt.Printf("❌ 启动命令失败: %v\n", err)
		return nil
	}

	var chain []NATHop
	scanner := bufio.NewScanner(stdout)
	ipRegex := regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)
	hopCount := 0

	// 实时读取输出流
	for scanner.Scan() {
		line := scanner.Text()

		// 提取 IP
		ips := ipRegex.FindAllString(line, -1)
		if len(ips) == 0 {
			continue
		}

		currentIP := ips[0]
		if currentIP == target {
			continue
		}

		hopCount++
		ipType := classifyIP(currentIP)

		// 构造当前跳的数据
		hop := NATHop{
			HopNum: hopCount,
			IP:     currentIP,
			Type:   ipType,
		}

		// 🟢 实时打印出来，让你立刻看到
		fmt.Printf("   ├─ 第 %d 跳: %-15s [%s]\n", hopCount, currentIP, ipType)

		chain = append(chain, hop)

		// 🛑 核心刹车逻辑优化 🛑
		if ipType == IPTypePublic {
			fmt.Println("   └─ ⚡ 探测到公网出口，立即终止后续扫描...")

			// 关键操作：直接杀死系统进程！
			// 如果不杀，tracert 还会傻傻地跑完剩下的跳数，导致耗时20秒
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
			break
		}
	}

	// 等待命令彻底结束（或清理僵尸进程）
	cmd.Wait()
	return chain
}

// -------------------------------------------------------
// 辅助函数
// -------------------------------------------------------

// 将命令创建逻辑分离，方便管理
func prepareTracerouteCmd(target string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		// Windows: -d 不解析主机名(快), -h 20 最大跳数, -w 300 超时300ms
		return exec.Command("tracert", "-d", "-h", "20", "-w", "300", target)
	} else {
		// Linux/Mac: -n 不解析主机名, -m 20 最大跳数, -w 1 超时1秒, -q 1 每跳只测一次(极速)
		return exec.Command("traceroute", "-n", "-m", "20", "-w", "1", "-q", "1", target)
	}
}

func classifyIP(ipStr string) string {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return "未知"
	}
	if cgnRange.Contains(ip) {
		return IPTypeCGN
	}
	if private10.Contains(ip) || private172.Contains(ip) || private192.Contains(ip) {
		return IPTypePrivate
	}
	return IPTypePublic
}

func printAnalysis(chain []NATHop) {
	fmt.Println("\n========== 📝 最终报告 ==========")

	if len(chain) == 0 {
		fmt.Println("⚠️  未获取到任何路由信息 (可能是权限不足或网络阻塞)")
		return
	}

	lastHop := chain[len(chain)-1]

	// 统计 NAT 层数（不包含最后的公网IP）
	natLayers := 0
	for _, hop := range chain {
		if hop.Type != IPTypePublic {
			natLayers++
		}
	}

	if lastHop.Type == IPTypePublic {
		fmt.Printf("✅ 链路正常穿透。\n")
		fmt.Printf("🧱 你的网络前面有 %d 层 NAT (私网/运营商网关)。\n", natLayers)
		if natLayers > 1 {
			fmt.Println("   (提示: NAT层数越少，P2P联机成功率越高)")
		}
	} else if lastHop.Type == IPTypeCGN {
		fmt.Printf("❌ 也是多层NAT。最后一层停在了运营商大内网 (CGN)。\n")
		fmt.Printf("   这意味着你没有独立的公网IP。\n")
	} else {
		fmt.Printf("❓ 扫描在私网 IP 处中断，未能到达互联网。\n")
	}
}

// -------------------------------------------------------
// 基础工具 (IP获取)
// -------------------------------------------------------

func getLocalIP() string {
	conn, err := net.Dial("udp", "114.114.114.114:80")
	if err != nil {
		return "未知"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func getPublicIP() string {
	c, err := stun.Dial("udp", "stun.telnyx.com:3478")
	if err != nil {
		return "检测超时"
	}
	defer c.Close()
	var xorAddr stun.XORMappedAddress
	message := stun.MustBuild(stun.TransactionID, stun.BindingRequest)
	if err := c.Do(message, func(res stun.Event) {
		if res.Error == nil {
			xorAddr.GetFrom(res.Message)
		}
	}); err != nil {
		return "检测超时"
	}
	return xorAddr.IP.String()
}
