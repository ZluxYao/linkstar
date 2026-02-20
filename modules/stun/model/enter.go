package model

import "time"

type StunConfig struct {
	// 基础网络信息
	LocalIP       string          `json:"localIP"`       // 本机内网IP
	PublicIP      string          `json:"publicIP"`      // 真实公网IP
	NatRouterList []NatRouterInfo `json:"natRouterList"` // 路由信息
	BestSTUN      string          `json:"bestStun"`      // 最快的STUN服务器
	CreatedAt     time.Time       `json:"createdAt"`     // 配置创建时间
	UpdatedAt     time.Time       `json:"updatedAt"`     // 最后更新时间

	Devices        []Device `json:"devices"`        // stun设备列表
	StunServerList []string `json:"stunServerList"` // stun服务器列表
}

type Device struct {
	DeviceID uint      `josn:"id"`       // 设备ID
	Name     string    `json:"name"`     // "本机" / "群晖NAS" / "树莓派"
	IP       string    `json:"ip"`       // 设备ip
	Services []Service `json:"services"` // 该设备上的服务

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Service 单个服务配置
type Service struct {
	ID             uint   `json:"id"` // 服务唯一标识符
	StartupSuccess bool   `json:"-"`
	Name           string `json:"name"`         // 服务名称,如 "SSH" / "Web管理" / "照片库"
	InternalPort   uint16 `json:"internalPort"` // 内网端口,如 22
	ExternalPort   uint16 `json:"externalPort"` // 外网映射端口,如 2222 (默认与 upnp映射端口一样
	Protocol       string `json:"protocol"`     // 传输协议 "TCP"/"UDP" (默认 TCP)
	TLS            bool   `json:"tls"`          // 证书

	// UPnP 相关配置
	UseUPnP        bool   `json:"useUpnp"`        // 是否启用 UPnP 自动端口映射 (默认 true)
	UPnPMappedPort uint16 `json:"upnpMappedPort"` // UPnP 实际映射成功的端口号

	Enabled     bool   `json:"enabled"`     // 服务是否启用 (默认 true)
	Description string `json:"description"` // 服务描述信息 (可选)

	LastError string    `json:"lastError"` // 最后一次操作的错误信息
	UpdatedAt time.Time `json:"updatedAt"` // 最后更新时间
}

// 每个Nat路由信息
type NatRouterInfo struct {
	NatLevel uint   `json:"natLevel"` // NAT层级
	LanIp    string `json:"lanIP"`    // LAN口IP地址
}
