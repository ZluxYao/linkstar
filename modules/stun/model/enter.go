package model

import "time"

type StunConfig struct {
	// 基础网络信息
	LocalIP     string    `json:"localIP"`     // 本机内网IP
	PublicIP    string    `json:"publicIP"`    // 真实公网IP
	RouterWanIP string    `json:"routerWanIP"` // 路由器WAN口IP
	IsNAT       bool      `json:"isNat"`       // 是否多层NAT
	BestSTUN    string    `json:"bestStun"`    // 最快的STUN服务器
	CreatedAt   time.Time `json:"createdAt"`   // 配置创建时间
	UpdatedAt   time.Time `json:"updatedAt"`   // 最后更新时间

	StunServiceList []StunService `json:"stunServiceList"` // stun服务列表
}

type StunService struct {
	// 标识信息
	ID          uint      `json:"id"`          // 唯一标识符
	ServiceName string    `json:"serviceName"` // 服务名称（如 "HTTP", "SSH", "MySQL"）
	Description string    `json:"description"` // 描述信息
	Enabled     bool      `json:"enabled"`     // 是否启用
	CreatedAt   time.Time `json:"createdAt"`   // 创建时间
	UpdatedAt   time.Time `json:"updatedAt"`   // 最后更新时间

	ServiceIP    string        `json:"serviceIP"`    // 服务IP地址
	ServicePorts []ServicePort `json:"servicePorts"` // 服务端口

}

// ServicePort 服务端口配置 - 管理单个端口映射
type ServicePort struct {
	Protocol     string `json:"protocol"`     // 协议类型: "TCP" 或 "UDP"
	InternalPort uint16 `json:"internalPort"` // 内网端口
	ExternalPort uint16 `json:"externalPort"` // 外网端口
	Description  string `json:"description"`  // 端口描述
	Enabled      bool   `json:"enabled"`      // 是否启用此端口映射

	// UPnP
	UseUPnP        bool   `json:"useUpnp"`        // 是否使用UPnP端口映射
	UPnPMappedPort uint16 `json:"upnpMappedPort"` // UPnP

	LastError string    `json:"lastError"` // 最后的错误信息
	UpdatedAt time.Time `json:"updatedAt"` // 最后更新时间
}
