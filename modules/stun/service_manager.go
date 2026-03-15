package stun

import (
	"context"
	"fmt"
	"linkstar/global"
	"linkstar/modules/stun/model"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// 状态 & 事件定义
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

type workerState int

const (
	stateAlive   workerState = iota // 正常保活中
	stateProbing                    // 心跳失败，加速探活
)

type eventKind int

const (
	evHeartbeatOK   eventKind = iota
	evHeartbeatFail           // STUN 心跳超时/失败
	evPortDrift               // 外网端口漂移，必须重建
	evServiceOK               // 端到端服务检查通过
	evServiceFail             // 端到端服务检查失败
)

type workerEvent struct {
	kind eventKind
	info string
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// 全局 service 注册表
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

type serviceEntry struct {
	cancel context.CancelFunc
}

var (
	servicesMu      sync.Mutex
	runningServices = make(map[string]*serviceEntry)

	// 限制同时重建的 worker 数量，防止 100 个同时死然后同时重建
	rebuildSem = make(chan struct{}, 10)
)

func serviceKey(deviceID, serviceID uint) string {
	return fmt.Sprintf("%d-%d", deviceID, serviceID)
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// 对外 API
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

// StartService 启动（或重启）单个服务
func StartService(device *model.Device, service *model.Service) {
	key := serviceKey(device.DeviceID, service.ID)

	servicesMu.Lock()
	if entry, ok := runningServices[key]; ok {
		entry.cancel()
		delete(runningServices, key)
	}
	if !service.Enabled {
		servicesMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	runningServices[key] = &serviceEntry{cancel: cancel}
	servicesMu.Unlock()

	go runServiceLoop(ctx, device, service, key)
}

// StopService 停止指定服务
func StopService(deviceID, serviceID uint) {
	key := serviceKey(deviceID, serviceID)
	servicesMu.Lock()
	defer servicesMu.Unlock()
	if entry, ok := runningServices[key]; ok {
		entry.cancel()
		delete(runningServices, key)
	}
}

// StartAllServices 分批启动所有已启用的服务，最多 10 个并发启动
func StartAllServices() {
	startSem := make(chan struct{}, 10)
	for i := range global.StunConfig.Devices {
		device := &global.StunConfig.Devices[i]
		for j := range device.Services {
			service := &device.Services[j]
			if !service.Enabled {
				continue
			}
			startSem <- struct{}{}
			go func(d *model.Device, s *model.Service) {
				// 每个 worker 加随机抖动，防止同批同时发 SSDP/STUN
				time.Sleep(time.Duration(rand.Intn(500)) * time.Millisecond)
				StartService(d, s)
				// 等 worker goroutine 真正跑起来再释放 semaphore
				time.Sleep(300 * time.Millisecond)
				<-startSem
			}(device, service)
		}
	}
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// 核心：单个 service 的完整生命周期
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

// runServiceLoop 外层循环：负责退避重建、禁用判断
func runServiceLoop(ctx context.Context, device *model.Device, service *model.Service, key string) {
	lg := newLogger(device.Name, service.Name)

	totalDeaths := 0  // 历史总死亡次数
	deathsInHour := 0 // 1 小时内死亡次数
	hourReset := time.Now()

	for {
		// 外部 cancel（StopService）
		if ctx.Err() != nil {
			lg.info("服务已被外部停止")
			return
		}

		// 1 小时计数重置
		if time.Since(hourReset) > time.Hour {
			deathsInHour = 0
			hourReset = time.Now()
		}

		// 禁用判断
		//   - 历史总死亡 ≥ 10 次
		//   - 或 1 小时内死亡 ≥ 5 次（环境持续异常）
		if totalDeaths >= 10 || deathsInHour >= 5 {
			lg.errf("累计失败过多(total=%d hour=%d)，进入 DISABLED，停止自动重建", totalDeaths, deathsInHour)
			service.Enabled = false
			service.PunchSuccess = false
			servicesMu.Lock()
			delete(runningServices, key)
			servicesMu.Unlock()
			return
		}

		// acquire 重建 semaphore（限制全局同时重建数 ≤10）
		rebuildSem <- struct{}{}
		lg.info("开始建立连接（历史死亡 %d 次）", totalDeaths)

		err := runOnce(ctx, device, service)

		<-rebuildSem // release

		// 外部 cancel 不算死亡
		if ctx.Err() != nil {
			lg.info("服务已被外部停止")
			return
		}

		// 无论 runOnce 怎么退出，都算一次死亡
		totalDeaths++
		deathsInHour++
		service.PunchSuccess = false
		service.ExternalPort = 0

		if err != nil {
			lg.errf("连接断开: %v（第 %d 次死亡）", err, totalDeaths)
		}

		// 指数退避重建
		// 死亡次数: 1→2s  2→4s  3→8s  4→16s  5→32s  6→64s  7→128s  8+→300s
		// 每次加 [0, base/3] 的随机 jitter
		backoff := calcBackoff(totalDeaths)
		lg.info("%.0fs 后重建...", backoff.Seconds())
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
	}
}

// calcBackoff 指数退避，上限 300s
func calcBackoff(deaths int) time.Duration {
	if deaths <= 0 {
		deaths = 1
	}
	exp := deaths
	if exp > 8 {
		exp = 8
	}
	base := time.Duration(1<<uint(exp)) * time.Second // 2^exp 秒
	if base > 300*time.Second {
		base = 300 * time.Second
	}
	jitter := time.Duration(rand.Int63n(int64(base / 3)))
	return base + jitter
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// runOnce：STUN → UPnP → 保活，完整一次生命周期
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func runOnce(ctx context.Context, device *model.Device, service *model.Service) error {
	lg := newLogger(device.Name, service.Name)
	protocol := normalizeProtocol(service.Protocol)

	// ── Step 1: STUN 握手 ─────────────────────────────────────────
	// 内部已有 8s 超时 + ctx 感知
	lg.info("STUN 握手...")
	stunConn, localPort, publicIP, publicPort, err := stunDial(ctx, protocol)
	if err != nil {
		return fmt.Errorf("STUN 失败: %w", err)
	}
	lg.info("STUN OK: 本地:%d  公网:%s:%d", localPort, publicIP, publicPort)

	// ── Step 2: 端口复用监听 ──────────────────────────────────────
	listener, err := listenReusePort(protocol, global.StunConfig.LocalIP, localPort)
	if err != nil {
		stunConn.Close()
		return fmt.Errorf("端口监听失败: %w", err)
	}

	// 统一资源清理（defer 在任何路径退出时都执行）
	defer func() {
		stunConn.Close()
		listener.Close()
		service.PunchSuccess = false
		service.ExternalPort = 0
		// 后台删除 UPnP 映射，给 10s 超时
		go func() {
			rCh := sendUPnPReq(&upnpReq{
				op:           upnpOpDelete,
				externalPort: localPort,
				protocol:     "TCP",
				internalIP:   global.StunConfig.LocalIP,
			}, false)
			select {
			case <-rCh:
			case <-time.After(10 * time.Second):
			}
		}()
		lg.info("资源已清理")
	}()

	// ctx 取消时立刻关闭连接，让阻塞的 Accept/Read 返回
	go func() {
		<-ctx.Done()
		stunConn.Close()
		listener.Close()
	}()

	// ── Step 3: UPnP 端口映射（插队 urgent 通道，10s 超时）────────
	// 不管成功失败都继续，失败走 UNSTABLE 模式
	upnpOK := false
	{
		rCh := sendUPnPReq(&upnpReq{
			op:           upnpOpAdd,
			externalPort: localPort,
			internalPort: localPort,
			protocol:     "TCP",
			description:  "LinkStar-" + service.Name,
			internalIP:   global.StunConfig.LocalIP,
		}, true) // urgent

		select {
		case e := <-rCh:
			if e == nil {
				upnpOK = true
				lg.info("UPnP OK: WAN:%d -> 本机:%d", localPort, localPort)
			} else {
				lg.info("UPnP 失败，进入 UNSTABLE 模式: %v", e)
			}
		case <-time.After(10 * time.Second):
			lg.info("UPnP 超时，进入 UNSTABLE 模式")
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// ── Step 4: 更新 service 状态 ─────────────────────────────────
	service.ExternalPort = uint16(publicPort)
	service.PunchSuccess = true
	service.StartupSuccess = true
	publicURL := buildPublicURL(service, publicIP, publicPort)
	lg.info("穿透成功 %s (UPnP=%v)", publicURL, upnpOK)

	// ── Step 5: 启动保活（UPnP 结果确认后才起） ──────────────────
	eventCh := make(chan workerEvent, 10)

	// 心跳间隔：有 UPnP 租约 25s，UNSTABLE 10s
	hbInterval := 25 * time.Second
	if !upnpOK {
		hbInterval = 10 * time.Second
	}

	go goHeartbeat(ctx, stunConn, protocol, hbInterval, eventCh)
	go goServiceCheck(ctx, service, publicURL, publicIP, publicPort, eventCh)

	// UNSTABLE 模式：后台每 30s 重试 UPnP
	if !upnpOK {
		go goRetryUPnP(ctx, service, localPort, &upnpOK, lg)
	}

	// 转发：接受外部连接
	go RunForwardLoop(ctx, listener, fmt.Sprintf("%s:%d", device.IP, service.InternalPort), protocol, service.Name)

	// ── Step 6: 主循环，消费事件驱动状态机 ───────────────────────
	return runStateLoop(ctx, eventCh, service, lg)
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// 状态机主循环
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

func runStateLoop(ctx context.Context, eventCh chan workerEvent, service *model.Service, lg *svcLogger) error {
	state := stateAlive
	hbFails := 0  // 连续心跳失败计数（ALIVE/PROBING 状态下）
	svcFails := 0 // 连续服务检查失败计数
	aliveAt := time.Now()

	// 曾经稳定运行 >30min，允许更多探活机会
	probeLimit := func() int {
		if time.Since(aliveAt) > 30*time.Minute {
			return 5
		}
		return 3
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ev := <-eventCh:
			switch ev.kind {

			// ── 心跳 OK ──
			case evHeartbeatOK:
				hbFails = 0
				if state == stateProbing {
					lg.info("心跳恢复 -> ALIVE")
					state = stateAlive
					service.PunchSuccess = true
					aliveAt = time.Now() // 刷新稳定时间点
				}

			// ── 心跳失败 ──
			case evHeartbeatFail:
				hbFails++
				lg.info("心跳失败 %d 次 (state=%s)", hbFails, state)

				// 连续 2 次失败才进 PROBING（1 次算抖动）
				if state == stateAlive && hbFails >= 2 {
					lg.info("-> PROBING，加速探活")
					state = stateProbing
					service.PunchSuccess = false
				}

				// PROBING 超过阈值 -> 触发重建
				if state == stateProbing && hbFails >= probeLimit() {
					return fmt.Errorf("心跳连续失败 %d 次，触发重建", hbFails)
				}

			// ── 端口漂移，立刻重建 ──
			case evPortDrift:
				return fmt.Errorf("外网端口漂移: %s", ev.info)

			// ── 服务检查 OK ──
			case evServiceOK:
				svcFails = 0

			// ── 服务检查失败 ──
			case evServiceFail:
				svcFails++
				// 服务检查失败不直接重建（可能是上游服务问题，重建也没用）
				// 连续 5 次才打一条警告，重置计数继续观察
				if svcFails >= 5 {
					lg.info("服务端到端连续失败 5 次，可能是上游问题（不重建）")
					svcFails = 0
				}
			}
		}
	}
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// 保活 goroutine
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

// goHeartbeat 定时发 STUN Binding Request，把结果写 eventCh
func goHeartbeat(ctx context.Context, conn net.Conn, protocol string, interval time.Duration, eventCh chan workerEvent) {
	// 加随机 jitter，防止 100 个 worker 同时发
	jitter := time.Duration(rand.Intn(2000)) * time.Millisecond
	timer := time.NewTimer(interval + jitter)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			var err error
			if protocol == "tcp" {
				err = sendTCPHeartbeatConn(conn)
			}
			// UDP 不在这里发心跳，由 goServiceCheck 兼顾

			ev := workerEvent{kind: evHeartbeatOK}
			if err != nil {
				ev = workerEvent{kind: evHeartbeatFail, info: err.Error()}
			}
			select {
			case eventCh <- ev:
			default: // eventCh 满了不阻塞，下次再发
			}

			// 重置 timer
			jitter = time.Duration(rand.Intn(2000)) * time.Millisecond
			timer.Reset(interval + jitter)
		}
	}
}

// goServiceCheck 端到端服务检查，每 30s 一次，首次延迟 6s
func goServiceCheck(ctx context.Context, service *model.Service, publicURL, publicIP string, publicPort int, eventCh chan workerEvent) {
	// 首次延迟：等 NAT 映射稳定
	select {
	case <-time.After(6 * time.Second):
	case <-ctx.Done():
		return
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			kind := evServiceOK
			if !serviceHealthCheck(service, publicURL, publicIP, publicPort) {
				kind = evServiceFail
			}
			select {
			case eventCh <- workerEvent{kind: kind}:
			default:
			}
		}
	}
}

// goRetryUPnP UNSTABLE 模式：后台每 30s 重试 UPnP 映射
func goRetryUPnP(ctx context.Context, service *model.Service, localPort uint16, upnpOK *bool, lg *svcLogger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if *upnpOK {
				return
			}
			rCh := sendUPnPReq(&upnpReq{
				op:           upnpOpAdd,
				externalPort: localPort,
				internalPort: localPort,
				protocol:     "TCP",
				description:  "LinkStar-" + service.Name,
				internalIP:   global.StunConfig.LocalIP,
			}, false)
			select {
			case e := <-rCh:
				if e == nil {
					*upnpOK = true
					lg.info("UNSTABLE -> UPnP 补充成功")
				}
			case <-time.After(10 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}
}

// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
// 工具：日志 helper
// ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

type svcLogger struct{ prefix string }

func newLogger(device, service string) *svcLogger {
	return &svcLogger{prefix: fmt.Sprintf("[%s/%s]", device, service)}
}

func (l *svcLogger) info(format string, args ...any) {
	logrus.Infof(l.prefix+" "+format, args...)
}

func (l *svcLogger) errf(format string, args ...any) {
	logrus.Errorf(l.prefix+" "+format, args...)
}
