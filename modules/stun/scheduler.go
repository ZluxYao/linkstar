package stun

import (
	"context"
	"fmt"
	"linkstar/global"
	"linkstar/modules/stun/model"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// ═══════════════════════════════════════════════════
// ServicePhase 服务阶段
// ═══════════════════════════════════════════════════

type ServicePhase int

const (
	PhaseProbing    ServicePhase = iota // 探针期：验证配置，最多 maxProbes 次连续失败
	PhaseRunning                        // 运行期：穿透曾经成功过，断线后无限重启
	PhaseRestarting                     // 等待重启中（backoff 计时）
	PhaseFailed                         // 终态：探针耗尽，需人工改配置后重新启用
	PhaseStopped                        // 主动停止
)

func (p ServicePhase) String() string {
	switch p {
	case PhaseProbing:
		return "PROBING"
	case PhaseRunning:
		return "RUNNING"
	case PhaseRestarting:
		return "RESTARTING"
	case PhaseFailed:
		return "FAILED"
	case PhaseStopped:
		return "STOPPED"
	default:
		return "UNKNOWN"
	}
}

// ═══════════════════════════════════════════════════
// LogEntry + RingLog  每个服务独立的环形日志缓冲
// ═══════════════════════════════════════════════════

type LogLevel string

const (
	LogInfo  LogLevel = "INFO"
	LogWarn  LogLevel = "WARN"
	LogError LogLevel = "ERROR"
)

type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   LogLevel  `json:"level"`
	Message string    `json:"message"`
}

type RingLog struct {
	mu    sync.RWMutex
	buf   []LogEntry
	head  int // 下一个写入位置
	count int // 已写入条数（未超容量时 < bufLen）
}

func NewRingLog(capacity int) *RingLog {
	return &RingLog{buf: make([]LogEntry, capacity)}
}

// Write 写一条日志
func (r *RingLog) Write(level LogLevel, msg string) {
	r.mu.Lock()
	r.buf[r.head] = LogEntry{Time: time.Now(), Level: level, Message: msg}
	r.head = (r.head + 1) % len(r.buf)
	if r.count < len(r.buf) {
		r.count++
	}
	r.mu.Unlock()
}

// ReadAll 返回从旧到新排好序的副本切片，面板可直接使用
func (r *RingLog) ReadAll() []LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]LogEntry, r.count)
	bufLen := len(r.buf) // 不用 cap 作变量名，避免遮蔽内置函数
	if r.count < bufLen {
		copy(result, r.buf[:r.count])
	} else {
		// 写满后 head 指向最旧的位置
		n := copy(result, r.buf[r.head:])
		copy(result[n:], r.buf[:r.head])
	}
	return result
}

// ═══════════════════════════════════════════════════
// Backoff  退避计时器
// ═══════════════════════════════════════════════════

type Backoff struct {
	steps []time.Duration
	idx   int
}

func NewBackoff() *Backoff {
	return &Backoff{
		steps: []time.Duration{
			1 * time.Second,
			2 * time.Second,
			4 * time.Second,
			5 * time.Minute,
		},
	}
}

func (b *Backoff) Next() time.Duration {
	if b.idx < len(b.steps) {
		d := b.steps[b.idx]
		b.idx++
		return d
	}
	return 5 * time.Minute
}

func (b *Backoff) Reset() {
	b.idx = 0
}

// sleepWithCtx 可被 ctx 打断的 sleep，返回 false 表示 ctx 被取消
func sleepWithCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// ═══════════════════════════════════════════════════
// StateEvent  推送给面板的状态变更事件
// ═══════════════════════════════════════════════════

type StateEvent struct {
	Key          string       `json:"key"`
	DeviceName   string       `json:"deviceName"`
	ServiceName  string       `json:"serviceName"`
	Phase        ServicePhase `json:"phase"`
	PhaseStr     string       `json:"phaseStr"`
	RestartCount int          `json:"restartCount"`
	LastError    string       `json:"lastError"`
	UpdatedAt    time.Time    `json:"updatedAt"`
}

// ═══════════════════════════════════════════════════
// serviceEntry  单个服务的控制句柄
// ═══════════════════════════════════════════════════

type serviceEntry struct {
	cancel context.CancelFunc
	done   chan struct{}

	// 以下字段供面板读取，goroutine 写，面板读，用 RWMutex 保护
	mu           sync.RWMutex
	phase        ServicePhase
	restartCount int
	lastError    string
	updatedAt    time.Time

	ringLog *RingLog // 每个服务独立的环形日志
}

func newEntry(cancel context.CancelFunc) *serviceEntry {
	return &serviceEntry{
		cancel:    cancel,
		done:      make(chan struct{}),
		phase:     PhaseProbing,
		updatedAt: time.Now(),
		ringLog:   NewRingLog(200), // 保留最近 200 条日志
	}
}

// setState goroutine 内部调用，更新状态
func (e *serviceEntry) setState(phase ServicePhase, errMsg string) {
	e.mu.Lock()
	e.phase = phase
	e.lastError = errMsg
	e.updatedAt = time.Now()
	if phase == PhaseRestarting {
		e.restartCount++
	}
	e.mu.Unlock()
}

// log 写服务专属日志，同时透传给 logrus 全局日志
func (e *serviceEntry) log(level LogLevel, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	e.ringLog.Write(level, msg)
	switch level {
	case LogInfo:
		logrus.Info(msg)
	case LogWarn:
		logrus.Warn(msg)
	case LogError:
		logrus.Error(msg)
	}
}

// snapshot 读取当前状态快照（线程安全）
func (e *serviceEntry) snapshot(key, deviceName, serviceName string) StateEvent {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return StateEvent{
		Key:          key,
		DeviceName:   deviceName,
		ServiceName:  serviceName,
		Phase:        e.phase,
		PhaseStr:     e.phase.String(),
		RestartCount: e.restartCount,
		LastError:    e.lastError,
		UpdatedAt:    e.updatedAt,
	}
}

// ═══════════════════════════════════════════════════
// Scheduler  调度器主体
// ═══════════════════════════════════════════════════

type Scheduler struct {
	mu       sync.RWMutex
	services map[string]*serviceEntry // key: "deviceID-serviceID"
	meta     map[string][2]string     // key → [deviceName, serviceName]

	// eventCh 状态变更事件，面板订阅
	// buffer=100：面板消费慢时 goroutine 不会被阻塞
	eventCh chan StateEvent
}

func NewScheduler() *Scheduler {
	return &Scheduler{
		services: make(map[string]*serviceEntry),
		meta:     make(map[string][2]string),
		eventCh:  make(chan StateEvent, 100),
	}
}

func serviceKey(deviceID, serviceID uint) string {
	return fmt.Sprintf("%d-%d", deviceID, serviceID)
}

// Subscribe 返回只读事件 channel，面板订阅状态变更（SSE / WebSocket 推送用）
func (s *Scheduler) Subscribe() <-chan StateEvent {
	return s.eventCh
}

// Snapshot 返回所有服务当前状态快照（HTTP 轮询用）
func (s *Scheduler) Snapshot() []StateEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]StateEvent, 0, len(s.services))
	for key, entry := range s.services {
		names := s.meta[key]
		result = append(result, entry.snapshot(key, names[0], names[1]))
	}
	return result
}

// GetLogs 返回指定服务的日志（HTTP 查询用）
func (s *Scheduler) GetLogs(deviceID, serviceID uint) []LogEntry {
	key := serviceKey(deviceID, serviceID)
	s.mu.RLock()
	entry, ok := s.services[key]
	s.mu.RUnlock()
	if !ok {
		return nil
	}
	return entry.ringLog.ReadAll()
}

// ═══════════════════════════════════════════════════
// emit  推送状态事件到 eventCh
// ═══════════════════════════════════════════════════

func (s *Scheduler) emit(entry *serviceEntry, key string) {
	s.mu.RLock()
	names := s.meta[key]
	s.mu.RUnlock()

	event := entry.snapshot(key, names[0], names[1])
	select {
	case s.eventCh <- event:
	default:
		// 面板消费太慢，丢弃本次事件
		// 状态已经在 entry 里更新，面板可以通过 Snapshot() 轮询补偿
		logrus.Warnf("[%s] 事件队列满，丢弃状态推送", key)
	}
}

// ═══════════════════════════════════════════════════
// watchPunchSuccess
//
// 在 RunStunTunnelWithContext 运行期间，轮询 service.PunchSuccess。
// 一旦检测到变为 true，立即将 entry.phase 升级为 PhaseRunning，
// 并重置 backoff，然后退出（后续由主循环维护状态）。
//
// 为什么需要这个 goroutine？
// stun.go 在穿透成功时置 service.PunchSuccess = true，
// 在 defer 清理时置回 false。
// RunStunTunnelWithContext 返回后调度器已无法感知"本次是否曾经成功过"。
// 因此必须在函数运行期间实时捕获这个信号。
// ═══════════════════════════════════════════════════

func watchPunchSuccess(
	ctx context.Context,
	service *model.Service,
	entry *serviceEntry,
	backoff *Backoff,
	key string,
	s *Scheduler,
	promoted chan<- struct{}, // 关闭表示"已升级为 PhaseRunning"
) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if service.PunchSuccess {
				// 穿透成功，升级阶段并重置 backoff
				entry.setState(PhaseRunning, "")
				backoff.Reset()
				s.emit(entry, key)
				close(promoted) // 通知主循环，只关闭一次
				return
			}
		}
	}
}

// ═══════════════════════════════════════════════════
// runService  goroutine 主体，服务生命周期状态机
//
// 阶段转换规则：
//
//	PhaseProbing：
//	  每次调用 RunStunTunnelWithContext，同时启动 watchPunchSuccess。
//	  - 若 watchPunchSuccess 检测到 PunchSuccess=true → 升级为 PhaseRunning（probeCount 不增加）
//	  - 若函数返回时仍未升级 → probeCount++
//	  - probeCount >= maxProbes → PhaseFailed（终态，需人工重新启用）
//
//	PhaseRunning：
//	  断线后无限重启，backoff 递增。
//	  再次穿透成功（watchPunchSuccess 触发）后 backoff 重置。
//
// ═══════════════════════════════════════════════════
func (s *Scheduler) runService(
	ctx context.Context,
	device *model.Device,
	service *model.Service,
	entry *serviceEntry,
	key string,
) {
	// 无论以何种方式退出，都关闭 done
	defer close(entry.done)

	const maxProbes = 5

	probeCount := 0
	backoff := NewBackoff()

	// 初始状态推送
	entry.setState(PhaseProbing, "")
	s.emit(entry, key)

	for {
		// 每次循环开始先检查 ctx，防止刚被 cancel 又启动一次穿透
		if ctx.Err() != nil {
			entry.log(LogInfo, "[%s] 调度器停止，服务退出", service.Name)
			entry.setState(PhaseStopped, "")
			s.emit(entry, key)
			return
		}

		// 读取当前阶段（加锁，避免数据竞争）
		entry.mu.RLock()
		currentPhase := entry.phase
		entry.mu.RUnlock()

		entry.log(LogInfo, "[%s] 启动穿透 phase=%s probe=%d restart=%d",
			service.Name, currentPhase.String(), probeCount, entry.restartCount)

		// innerCtx：控制 watchPunchSuccess 的生命周期，
		// RunStunTunnelWithContext 返回后立即取消，避免 watcher 泄露
		innerCtx, innerCancel := context.WithCancel(ctx)

		// promoted 关闭表示本次穿透期间曾经成功过
		promoted := make(chan struct{})
		go watchPunchSuccess(innerCtx, service, entry, backoff, key, s, promoted)

		err := RunStunTunnelWithContext(ctx, device.IP, service)
		innerCancel() // 停止 watcher

		// ctx 被取消（StopService 调用），正常退出，不计入失败
		if ctx.Err() != nil {
			entry.log(LogInfo, "[%s] 服务已停止", service.Name)
			entry.setState(PhaseStopped, "")
			s.emit(entry, key)
			return
		}

		// 检查本次穿透期间是否曾经成功（非阻塞读 promoted channel）
		everSucceeded := false
		select {
		case <-promoted:
			everSucceeded = true
		default:
		}

		errMsg := ""
		if err != nil {
			errMsg = err.Error()
		}
		entry.log(LogWarn, "[%s] 穿透中断: %v", service.Name, err)

		// 读取穿透结束后的最新阶段（watchPunchSuccess 可能已升级为 PhaseRunning）
		entry.mu.RLock()
		phaseNow := entry.phase
		entry.mu.RUnlock()

		// ── 阶段分支 ─────────────────────────────────────

		if phaseNow == PhaseRunning || everSucceeded {
			// 穿透曾经成功过（运行期断线），无限重启，backoff 递增
			wait := backoff.Next()
			entry.log(LogWarn, "[%s] 运行中断，%v 后重启 (第 %d 次)",
				service.Name, wait, entry.restartCount+1)

			entry.setState(PhaseRestarting, errMsg)
			s.emit(entry, key)

			if !sleepWithCtx(ctx, wait) {
				entry.setState(PhaseStopped, "")
				s.emit(entry, key)
				return
			}

			// 保持 PhaseRunning，等待下次穿透
			entry.setState(PhaseRunning, "")
			s.emit(entry, key)

		} else {
			// 穿透从未成功过，计入探针失败
			probeCount++
			entry.log(LogWarn, "[%s] 探针失败 %d/%d: %v", service.Name, probeCount, maxProbes, err)

			if probeCount >= maxProbes {
				// 探针耗尽 → FAILED 终态
				service.Enabled = false
				service.PunchSuccess = false
				entry.log(LogError, "[%s] 探针连续失败 %d 次，进入 FAILED，请检查配置后重新启用",
					service.Name, maxProbes)
				entry.setState(PhaseFailed, errMsg)
				s.emit(entry, key)

				// 从 map 里删掉自己，同时清理 meta，不再占用 key
				s.mu.Lock()
				delete(s.services, key)
				delete(s.meta, key)
				s.mu.Unlock()
				return
			}

			entry.setState(PhaseRestarting, errMsg)
			s.emit(entry, key)

			// 探针期用固定 1s，快速验证配置
			if !sleepWithCtx(ctx, 1*time.Second) {
				entry.setState(PhaseStopped, "")
				s.emit(entry, key)
				return
			}

			entry.setState(PhaseProbing, "")
			s.emit(entry, key)
		}
	}
}

// ═══════════════════════════════════════════════════
// StartService  启动单个服务
// ═══════════════════════════════════════════════════

func (s *Scheduler) StartService(device *model.Device, service *model.Service) {
	key := serviceKey(device.DeviceID, service.ID)

	s.mu.Lock()

	// 已有旧实例 → 先取消并等待真正退出
	if old, ok := s.services[key]; ok {
		old.cancel()
		oldDone := old.done
		delete(s.services, key)
		s.mu.Unlock() // 等待期间释放锁，防止 StopService 死锁

		select {
		case <-oldDone:
			logrus.Infof("[%s] 旧实例已退出", key)
		case <-time.After(15 * time.Second):
			logrus.Warnf("[%s] 旧实例超时，强制继续", key)
		}

		// 重新加锁后二次检查，防止并发 StartService 已写入新实例被覆盖
		s.mu.Lock()
		if _, exists := s.services[key]; exists {
			s.mu.Unlock()
			logrus.Warnf("[%s] 并发启动冲突，放弃本次", key)
			return
		}
	}

	if !service.Enabled {
		s.mu.Unlock()
		logrus.Infof("[%s - %s] 服务未启用，跳过", device.Name, service.Name)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry := newEntry(cancel)

	s.services[key] = entry
	s.meta[key] = [2]string{device.Name, service.Name}
	s.mu.Unlock()

	// goroutine 启动在锁外，避免持锁期间阻塞
	go s.runService(ctx, device, service, entry, key)
}

// ═══════════════════════════════════════════════════
// StopService  停止单个服务并等待退出
// ═══════════════════════════════════════════════════

func (s *Scheduler) StopService(deviceID, serviceID uint) {
	key := serviceKey(deviceID, serviceID)

	s.mu.Lock()
	entry, ok := s.services[key]
	if !ok {
		s.mu.Unlock()
		return
	}
	entry.cancel()
	oldDone := entry.done
	// 同时清理 meta，避免内存泄漏
	delete(s.services, key)
	delete(s.meta, key)
	s.mu.Unlock()

	select {
	case <-oldDone:
		logrus.Infof("[%s] 已停止", key)
	case <-time.After(15 * time.Second):
		logrus.Warnf("[%s] 停止超时", key)
	}
}

// ═══════════════════════════════════════════════════
// StopAll  并发停止所有服务（程序退出时调用）
// ═══════════════════════════════════════════════════

func (s *Scheduler) StopAll() {
	s.mu.Lock()
	entries := make([]*serviceEntry, 0, len(s.services))
	keys := make([]string, 0, len(s.services))
	for k, e := range s.services {
		e.cancel()
		entries = append(entries, e)
		keys = append(keys, k)
	}
	s.services = make(map[string]*serviceEntry)
	s.meta = make(map[string][2]string) // 同时清空 meta
	s.mu.Unlock()

	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(k string, entry *serviceEntry) {
			defer wg.Done()
			select {
			case <-entry.done:
				logrus.Infof("[%s] 已停止", k)
			case <-time.After(15 * time.Second):
				logrus.Warnf("[%s] 停止超时", k)
			}
		}(keys[i], e)
	}
	wg.Wait()
}

// ═══════════════════════════════════════════════════
// StartAllServices  启动全部已启用服务（程序初始化时调用）
// ═══════════════════════════════════════════════════

func (s *Scheduler) StartAllServices() {
	for i := range global.StunConfig.Devices {
		device := &global.StunConfig.Devices[i]
		for j := range device.Services {
			service := &device.Services[j]
			if service.Enabled {
				s.StartService(device, service)
			}
		}
	}
}
