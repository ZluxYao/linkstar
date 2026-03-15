package stun

import (
	"fmt"
	"linkstar/global"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

// InitSTUN 初始化入口
// 顺序：
//  1. 并行：GetFastStunServer + GetNatRouterList + DiscoverUPnPGateway
//  2. 串行：GetPublicIPInfo（依赖 BestSTUN）
//  3. 启动全局 UPnP serial worker
//  4. 分批启动所有服务（StartAllServices）
func InitSTUN() error {
	var err error

	// 读取配置
	global.StunConfig, err = ReadStunConfig()
	if err != nil {
		logrus.Fatal("读取配置文件失败", err)
	}

	// 退出时保存配置
	go SetupShutdownHook(func() {
		if e := UpdateStunConfig(global.StunConfig); e != nil {
			logrus.Error("保存配置失败：", e)
		}
	})

	// ── 阶段 1：并行初始化（互相独立，3 件事同时跑）────────────
	var gw *upnpGateway
	var g errgroup.Group

	g.Go(func() error {
		global.StunConfig.BestSTUN = GetFastStunServer()
		return nil
	})

	g.Go(func() error {
		list, e := GetNatRouterList()
		if e != nil {
			logrus.Warnf("获取 NatRouterList 失败: %v", e) // 非致命，继续
			return nil
		}
		global.StunConfig.NatRouterList = list
		return nil
	})

	g.Go(func() error {
		// UPnP 发现（SSDP 广播），结果缓存到 gw，后续不再重复发现
		gw = DiscoverUPnPGateway()
		return nil
	})

	if err := g.Wait(); err != nil {
		return fmt.Errorf("并行初始化失败: %w", err)
	}

	// ── 阶段 2：获取公网 IP（依赖 BestSTUN，必须串行在后）──────
	publicIPInfo, err := GetPublicIPInfo()
	if err != nil {
		return fmt.Errorf("获取公网 IP 失败: %w", err)
	}
	global.StunConfig.PublicIP = publicIPInfo.PublicIP
	global.StunConfig.LocalIP = publicIPInfo.LocalIP
	global.StunConfig.UpdatedAt = time.Now()

	logrus.Infof("最快 STUN: %s", global.StunConfig.BestSTUN)
	logrus.Infof("本地 IP: %s  公网 IP: %s", global.StunConfig.LocalIP, global.StunConfig.PublicIP)

	// ── 阶段 3：启动全局 UPnP 串行 worker（唯一实例）────────────
	// 必须在 StartAllServices 之前启动，否则 worker 还没跑，
	// service goroutine 就往 channel 里写了
	StartUPnPWorker(gw)
	logrus.Info("[UPnP] 串行 worker 已启动")

	// ── 阶段 4：分批启动所有服务 ─────────────────────────────────
	go StartAllServices()

	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("✅ STUN 初始化完成，服务启动中...")
	return nil
}
