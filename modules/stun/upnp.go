package stun

import (
	"fmt"
	"linkstar/global"
	"time"

	"github.com/huin/goupnp/dcps/internetgateway1"
	"github.com/huin/goupnp/dcps/internetgateway2"
	"github.com/sirupsen/logrus"
)

// upnpOp 操作类型
type upnpOp int

const (
	upnpOpAdd upnpOp = iota
	upnpOpDelete
	upnpOpRenew // 续约，走 urgent 通道
)

// upnpReq 一次 UPnP 操作请求
type upnpReq struct {
	op           upnpOp
	externalPort uint16
	internalPort uint16
	protocol     string
	description  string
	internalIP   string
	resultCh     chan error // buffered 1，调用方等这个
}

// upnpGateway 持有已发现的网关客户端（启动时发现一次，之后复用）
type upnpGateway struct {
	v1    []*internetgateway1.WANIPConnection1
	v1ppp []*internetgateway1.WANPPPConnection1
	v2    []*internetgateway2.WANIPConnection1
	v2ppp []*internetgateway2.WANPPPConnection1
}

var (
	// 全局两个优先级通道
	// urgent: Renew 走这里，保活优先
	// normal: Add / Delete 走这里
	upnpUrgentCh = make(chan *upnpReq, 20)
	upnpNormalCh = make(chan *upnpReq, 200)
)

// DiscoverUPnPGateway 启动时调用一次，发现并缓存网关
func DiscoverUPnPGateway() *upnpGateway {
	gw := &upnpGateway{}

	if clients, _, err := internetgateway1.NewWANIPConnection1Clients(); err == nil {
		gw.v1 = clients
	}
	if clients, _, err := internetgateway1.NewWANPPPConnection1Clients(); err == nil {
		gw.v1ppp = clients
	}
	if clients, _, err := internetgateway2.NewWANIPConnection1Clients(); err == nil {
		gw.v2 = clients
	}
	if clients, _, err := internetgateway2.NewWANPPPConnection1Clients(); err == nil {
		gw.v2ppp = clients
	}

	if len(gw.v1)+len(gw.v1ppp)+len(gw.v2)+len(gw.v2ppp) == 0 {
		logrus.Warn("[UPnP] 未发现任何网关设备")
	} else {
		logrus.Infof("[UPnP] 发现网关: v1=%d v1ppp=%d v2=%d v2ppp=%d",
			len(gw.v1), len(gw.v1ppp), len(gw.v2), len(gw.v2ppp))
	}
	return gw
}

// StartUPnPWorker 启动全局唯一串行 worker，在 InitSTUN 里调用一次
func StartUPnPWorker(gw *upnpGateway) {
	go func() {
		for {
			// 优先消费 urgent，urgent 空了才取 normal
			var req *upnpReq
			select {
			case req = <-upnpUrgentCh:
			default:
				select {
				case req = <-upnpUrgentCh:
				case req = <-upnpNormalCh:
				}
			}

			var err error
			switch req.op {
			case upnpOpAdd, upnpOpRenew:
				err = gw.addMapping(req)
			case upnpOpDelete:
				err = gw.deleteMapping(req)
			}

			// 结果写回，调用方有 select+timeout 兜底，这里不会阻塞
			req.resultCh <- err

			// 两次请求之间间隔，防止打爆网关
			time.Sleep(150 * time.Millisecond)
		}
	}()
}

// sendUPnPReq 向 worker 提交请求，urgent=true 走优先通道
// 返回 resultCh，调用方自己 select 等结果
func sendUPnPReq(req *upnpReq, urgent bool) chan error {
	req.resultCh = make(chan error, 1)
	if urgent {
		select {
		case upnpUrgentCh <- req:
		default:
			// urgent 通道满了（极少），降级到 normal
			select {
			case upnpNormalCh <- req:
			default:
				// 两个都满，直接返回错误，不阻塞调用方
				req.resultCh <- fmt.Errorf("upnp queue full")
			}
		}
	} else {
		select {
		case upnpNormalCh <- req:
		default:
			req.resultCh <- fmt.Errorf("upnp queue full")
		}
	}
	return req.resultCh
}

// ---- 实际执行 SOAP，复用已发现的网关 ----

func (gw *upnpGateway) addMapping(req *upnpReq) error {
	for _, c := range gw.v1 {
		if err := c.AddPortMapping("", req.externalPort, req.protocol, req.internalPort,
			req.internalIP, true, req.description, 0); err == nil {
			return nil
		}
	}
	for _, c := range gw.v1ppp {
		if err := c.AddPortMapping("", req.externalPort, req.protocol, req.internalPort,
			req.internalIP, true, req.description, 0); err == nil {
			return nil
		}
	}
	for _, c := range gw.v2 {
		if err := c.AddPortMapping("", req.externalPort, req.protocol, req.internalPort,
			req.internalIP, true, req.description, 0); err == nil {
			return nil
		}
	}
	for _, c := range gw.v2ppp {
		if err := c.AddPortMapping("", req.externalPort, req.protocol, req.internalPort,
			req.internalIP, true, req.description, 0); err == nil {
			return nil
		}
	}
	return fmt.Errorf("所有网关均无法添加映射 port=%d", req.externalPort)
}

func (gw *upnpGateway) deleteMapping(req *upnpReq) error {
	for _, c := range gw.v1 {
		if err := c.DeletePortMapping("", req.externalPort, req.protocol); err == nil {
			return nil
		}
	}
	for _, c := range gw.v2 {
		if err := c.DeletePortMapping("", req.externalPort, req.protocol); err == nil {
			return nil
		}
	}
	return fmt.Errorf("删除映射失败 port=%d", req.externalPort)
}

// ── 兼容函数：供 stun.go 里的 RunStunTunnel / RunStunTunnelWithContext 调用 ──
// 内部走全局 UPnP worker channel，保持串行

// AddPortMapping 添加端口映射（兼容旧调用）
func AddPortMapping(externalPort, internalPort uint16, protocol, description string) error {
	rCh := sendUPnPReq(&upnpReq{
		op:           upnpOpAdd,
		externalPort: externalPort,
		internalPort: internalPort,
		protocol:     protocol,
		description:  description,
		internalIP:   global.StunConfig.LocalIP,
	}, false)
	select {
	case err := <-rCh:
		return err
	case <-time.After(10 * time.Second):
		return fmt.Errorf("AddPortMapping 超时")
	}
}

// DeletePortMapping 删除端口映射（兼容旧调用）
func DeletePortMapping(externalPort uint16, protocol string) error {
	rCh := sendUPnPReq(&upnpReq{
		op:           upnpOpDelete,
		externalPort: externalPort,
		protocol:     protocol,
	}, false)
	select {
	case err := <-rCh:
		return err
	case <-time.After(10 * time.Second):
		return fmt.Errorf("DeletePortMapping 超时")
	}
}
