package stun

import (
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// UPnP队列管理器 - 串行化处理所有UPnP请求，避免并发冲突
type UpnpQueueManager struct {
	queue   chan *upnpRequest
	wg      sync.WaitGroup
	started bool
	mu      sync.Mutex
}

// UPnP请求
type upnpRequest struct {
	externalPort uint16
	internalPort uint16
	protocol     string
	description  string
	resultChan   chan error // 用于返回结果
}

var (
	upnpQueue *UpnpQueueManager
	once      sync.Once
)

// 获取UPnP队列管理器单例
func GetUpnpQueue() *UpnpQueueManager {
	once.Do(func() {
		upnpQueue = &UpnpQueueManager{
			queue: make(chan *upnpRequest, 200), // 缓冲200个请求
		}
	})
	return upnpQueue
}

// 启动UPnP队列处理器
func (m *UpnpQueueManager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return
	}

	m.started = true
	m.wg.Add(1)

	go func() {
		defer m.wg.Done()
		logrus.Info("🔧 UPnP队列处理器已启动")

		for req := range m.queue {
			// 串行处理每个UPnP请求
			err := AddPortMapping(req.externalPort, req.internalPort, req.protocol, req.description)

			// 返回结果
			req.resultChan <- err
			close(req.resultChan)

			// 每次请求之间延迟，避免路由器过载
			time.Sleep(200 * time.Millisecond)
		}

		logrus.Info("🔧 UPnP队列处理器已停止")
	}()
}

// 停止UPnP队列处理器
func (m *UpnpQueueManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return
	}

	close(m.queue)
	m.wg.Wait()
	m.started = false
}

// 添加UPnP端口映射（同步接口，会等待结果）
func (m *UpnpQueueManager) AddPortMappingSync(externalPort, internalPort uint16, protocol, description string) error {
	if !m.started {
		return fmt.Errorf("UPnP队列未启动")
	}

	req := &upnpRequest{
		externalPort: externalPort,
		internalPort: internalPort,
		protocol:     protocol,
		description:  description,
		resultChan:   make(chan error, 1),
	}

	// 发送请求到队列
	m.queue <- req

	// 等待结果
	err := <-req.resultChan
	return err
}
