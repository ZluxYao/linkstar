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
}

var (
	servicesMu      sync.Mutex
	runningServices = make(map[string]*serviceEntry) // key: "deviceID-serviceID"
)

func serviceKey(deviceID, serviceID uint) string {
	return fmt.Sprintf("%d-%d", deviceID, serviceID)
}

// StartService 启动单个服务的 goroutine（已在运行则先停止）
func StartService(device *model.Device, service *model.Service) {
	key := serviceKey(device.DeviceID, service.ID)

	servicesMu.Lock()
	// 若已有同 key 的服务在运行，先取消它
	if entry, ok := runningServices[key]; ok {
		entry.cancel()
		delete(runningServices, key)
		logrus.Infof("[%s - %s] 停止旧实例", device.Name, service.Name)
	}

	if !service.Enabled {
		servicesMu.Unlock()
		logrus.Infof("[%s - %s] 服务未启用，跳过", device.Name, service.Name)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	runningServices[key] = &serviceEntry{cancel: cancel}
	servicesMu.Unlock()

	go func() {
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
	defer servicesMu.Unlock()

	if entry, ok := runningServices[key]; ok {
		entry.cancel()
		delete(runningServices, key)
		logrus.Infof("服务 %s 已停止", key)
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
