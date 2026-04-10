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

// serviceEntry 记录一个正在运行的服务
type serviceEntry struct {
	cancel context.CancelFunc
	done   chan struct{} // goroutine 退出时关闭，用于等待旧实例真正结束
}

var (
	servicesMu      sync.Mutex
	runningServices = make(map[string]*serviceEntry) // key: "deviceID-serviceID"
)

func serviceKey(deviceID, serviceID uint) string {
	return fmt.Sprintf("%d-%d", deviceID, serviceID)
}

// StartService 启动单个服务的 goroutine（已在运行则先停止并等待退出）
func StartService(device *model.Device, service *model.Service) {
	key := serviceKey(device.DeviceID, service.ID)

	servicesMu.Lock()
	// 若已有同 key 的服务在运行，先取消它并等待其真正退出
	if entry, ok := runningServices[key]; ok {
		entry.cancel()
		oldDone := entry.done // 持有 done channel 引用，Unlock 后继续等待
		delete(runningServices, key)
		servicesMu.Unlock()

		// 等旧实例真正退出，避免新旧两代 goroutine 同时存活造成泄露
		// 超时 15s 是因为 firstTcpHealthKeep 最长阻塞 2+4+8=14s
		select {
		case <-oldDone:
			logrus.Infof("[%s] 旧实例已退出", key)
		case <-time.After(15 * time.Second):
			logrus.Warnf("[%s] 旧实例超时未退出，强制继续", key)
		}

		servicesMu.Lock()
	}

	if !service.Enabled {
		servicesMu.Unlock()
		logrus.Infof("[%s - %s] 服务未启用，跳过", device.Name, service.Name)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}) // 每次启动新建一个 done channel
	runningServices[key] = &serviceEntry{cancel: cancel, done: done}
	servicesMu.Unlock()

	go func() {
		defer close(done) // 无论以何种方式退出，都关闭 done 通知等待方

		maxRetries := 5
		attempt := 0
		for {
			select {
			case <-ctx.Done():
				logrus.Infof("[%s - %s] 服务已停止", device.Name, service.Name)
				return
			default:
			}

			attempt++
			logrus.Infof("[%s - %s] 启动服务 (第 %d 次)", device.Name, service.Name, attempt)

			err := RunStunTunnelWithContext(ctx, device.IP, service)

			// ctx 被取消，正常退出
			if ctx.Err() != nil {
				service.PunchSuccess = false
				logrus.Infof("[%s - %s] 服务已被取消退出", device.Name, service.Name)
				return
			}

			if err != nil {
				logrus.Errorf("❌ [%s - %s] STUN 穿透失败 (第 %d/%d 次): %v",
					device.Name, service.Name, attempt, maxRetries, err)

				if service.StartupSuccess {
					attempt = 0
					service.StartupSuccess = false
				}

				if attempt >= maxRetries {
					service.Enabled = false
					service.PunchSuccess = false
					logrus.Errorf("[%s - %s] 达到最大重试次数，关闭服务", device.Name, service.Name)
					servicesMu.Lock()
					delete(runningServices, key)
					servicesMu.Unlock()
					return
				}

				time.Sleep(time.Second)
				continue
			}
		}
	}()
}

// StopService 停止指定服务的 goroutine
func StopService(deviceID, serviceID uint) {
	key := serviceKey(deviceID, serviceID)
	servicesMu.Lock()

	entry, ok := runningServices[key]
	if !ok {
		servicesMu.Unlock()
		return
	}

	entry.cancel()
	oldDone := entry.done
	delete(runningServices, key)
	servicesMu.Unlock()

	// 等待 goroutine 真正退出
	select {
	case <-oldDone:
		logrus.Infof("服务 %s 已停止", key)
	case <-time.After(15 * time.Second):
		logrus.Warnf("服务 %s 停止超时", key)
	}
}

// StartAllServices 启动全部已启用的服务（程序初始化时调用）
func StartAllServices() {
	for i := range global.StunConfig.Devices {
		device := &global.StunConfig.Devices[i]
		for j := range device.Services {
			service := &device.Services[j]
			if service.Enabled {
				StartService(device, service)
			}
		}
	}
}
