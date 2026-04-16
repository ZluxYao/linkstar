package stun

import (
	"context"
	"go/constant"
	"linkstar/modules/stun/model"
	"sync"
	"time"
)

// ServicePhase 服务阶段──────────────────────────────────────
type ServicePhase int

const (
	Probing    ServicePhase = iota // 探针阶段：验证配置可行性，最多 maxProbes 次连续失败
	Running                        // 运行阶段：穿透成功且稳定过，短线后无限重启
	Restarting                     // 等待重启阶段:
	Failed                         // 运行失败：探针耗尽，需人为修改配置文件然后重启启动
	Stopped                        // 主动停止
)

// service 单个服务的控制句柄──────────────────────────────────────
type serviceHandle struct {
	cancel context.CancelFunc
	done   chan struct{}

	// 以下字段供面板读取，goroutine 写，面板读，用 RWMutex 保护
	mu           sync.RWMutex
	phase        ServicePhase // 当前阶段
	restartCount int          // 重启次数
	lastError    string
	updateAt     time.Time
}

// 创建 service
func newService(cancel context.CancelFunc) *serviceHandle {
	return &serviceHandle{
		cancel:   cancel,
		done:     make(chan struct{}),
		phase:    Probing,
		updateAt: time.Now(),
	}
}

// StateEvent  推送给面板的状态变更事件──────────────────────────────────────
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

// Scheduler  调度器主体──────────────────────────────────────
type Scheduler struct {
	mu      sync.RWMutex
	service map[string]*serviceHandle // key:"deviceID-serviceID" 所有服务的管理
	meta    map[string][2]string      // value → [deviceName, serviceName]  用来展示

	eventCh chan StateEvent // 状态变更事件，面板订阅
}

// 核心状态机────────────────────────────────────

// 启动服务
func (S *Scheduler) serviceRun(ctx context.Context, service *model.Service, handle *serviceHandle) {
	defer close(handle.done)

	//探测阶段最大尝试次数
	const maxProbes
	probeCount := 0

	for {
		// 1.ctx 已取消，退出
		if ctx.Err() != nil {
			handle.phase = Stopped
			return
		}

		// 2.启动内网穿透服务，同时跑一个watcher监听是否成功
		innerCtx, innerCancel := context.WithCancel(ctx)

	}

}
