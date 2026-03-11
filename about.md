# LinkStar 项目文档

## 项目概述

**LinkStar** 是一个基于 Go 语言开发的 NAT 穿透服务管理平台，主要实现内网穿透功能，支持通过 STUN 协议和 UPnP 协议进行端口映射，使外部网络可以访问内网服务。

**愿景**：打造一个「一键穿透工具」，让 NAT4 用户也能轻松实现内网穿透。

## 技术栈

| 技术 | 说明 |
|------|------|
| Go 1.25.1 | 后端语言 |
| Gin | Web 框架 |
| pion/stun | STUN 协议实现 |
| huin/goupnp | UPnP 协议实现 |
| logrus | 日志库 |
| Vue.js (嵌入) | 前端框架 |

## 穿透模式

```
┌─────────────────────────────────────────────────────────────────┐
│                     LinkStar 穿透模式                            │
├─────────────────────────────────────────────────────────────────┤
│                                                                 │
│  模式1：本地模式 (只 STUN)                                       │
│  ────────────────────────                                       │
│  LinkStar ──► STUN服务器 ──► 公网 ◄── 目标设备(同网络)          │
│       └──► 本地 UPnP                                             │
│                                                                 │
│  模式2：组网模式 (只 EasyTier)                                   │
│  ────────────────────────                                       │
│  LinkStar ──► EasyTier ──► 内网设备 ◄── 你(通过组网访问)        │
│                                                                 │
│  模式3：组网+STUN (推荐)                                         │
│  ────────────────────────                                       │
│  LinkStar ──► EasyTier ──► 内网设备 ──► STUN+UPnP ──► 公网      │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
模式1 公网可访问  nat3
模式2 可以跨网访问  nat4
模式3 借助跨网实现公网访问  nat4
```

### 模式说明

| 模式 | 适用场景 | 说明 |
|------|----------|------|
| 本地模式 | 目标设备与 LinkStar 在同一内网 | 直接 STUN 打洞 + UPnP |
| 组网模式 | 目标设备在内网，需要远程访问 | 通过 EasyTier 访问内网设备 |
| 组网+STUN | NAT4 用户，无法直接打洞 | 借助目标网络内的路由器 UPnP 实现穿透 |

## 核心功能

1. **STUN 穿透** - 使用 STUN 协议获取公网 IP 和端口映射
2. **UPnP 自动端口映射** - 自动发现并配置 UPnP 网关
3. **多服务管理** - 支持管理多个设备和多个服务
4. **热重载** - 支持动态添加/修改/删除设备和服务
5. **自动最优 STUN 服务器选择** - 自动测速选择最快的 STUN 服务器
6. **EasyTier 组网集成** - 支持内网组网，突破 NAT4 限制

## 项目结构

```
linkstar/
├── api/                    # API 接口层
│   └── stun_api/          # STUN 相关 API
├── core/                  # 核心模块（日志等）
├── global/                # 全局变量
├── modules/
│   ├── stun/             # STUN 核心模块
│   │   ├── stun.go      # STUN 主逻辑
│   │   ├── upnp.go      # UPnP 端口映射
│   │   ├── forward.go   # 端口转发
│   │   └── model/       # 数据模型
│   └── network/          # 网络适配层（未来扩展）
│       └── adapter.go   # 接口定义
├── routers/              # 路由配置
├── config/               # 配置文件
├── test/                 # 测试代码
└── web/dist/             # 前端静态资源（嵌入）
```

## 启动配置

- **服务端口**: 3333
- **配置文件**: config/stunConfig.json
- **日志目录**: logs/

## 核心模块说明

### modules/stun/stun.go
- STUN 穿透核心逻辑
- 获取公网 IP 信息
- 端口打洞实现

### modules/stun/upnp.go
- UPnP 设备发现
- 端口映射/删除

### modules/stun/forward.go
- 端口转发
- 数据代理

### modules/stun/service_manager.go
- 服务生命周期管理
- 健康检测

## API 接口

| 接口 | 方法 | 说明 |
|------|------|------|
| /api/stun/config | GET | 获取 STUN 配置 |
| /api/stun/device/add | POST | 添加设备 |
| /api/stun/device/update | POST | 更新设备 |
| /api/stun/device/delete | POST | 删除设备 |
| /api/stun/service/add | POST | 添加服务 |
| /api/stun/service/update | POST | 更新服务 |
| /api/stun/service/delete | POST | 删除服务 |

## 数据模型

### StunConfig
- LocalIP: 本机内网 IP
- PublicIP: 公网 IP
- NatRouterList: NAT 路由信息
- BestSTUN: 最快的 STUN 服务器
- Devices: 设备列表
- StunServerList: STUN 服务器列表

### Device (设备)
- DeviceID: 设备 ID
- Name: 设备名称
- IP: 内网 IP
- Services: 服务列表

### Service (服务)
- ID: 服务 ID
- Name: 服务名称
- InternalPort: 内网端口
- ExternalPort: 外部端口
- Protocol: 协议 (TCP/UDP)
- TLS: 是否启用 TLS
- UseUpnp: 是否使用 UPnP
- Enabled: 是否启用
- PunchSuccess: 打洞是否成功

## 未来规划

### 第二阶段：自动化

```
组网 + STUN 自动化：
│
├── 1. 连接 EasyTier 网络
│
├── 2. 自动发现内网设备
│   ├── 扫描 EasyTier peers 获取可达节点
│   ├── 对每个节点尝试连接其内网IP:22/80 等常见端口
│   └── 发现存活的内网设备列表
│
├── 3. 自动 STUN 打洞（对每个内网设备）
│   ├── 尝试 STUN 穿透
│   ├── 尝试 UPnP 端口映射（如果目标有 UPnP）
│   └── 记录打洞结果
│
└── 4. 配置界面展示
    ├── 设备列表 + 在线状态
    ├── 公网访问地址
    └── 穿透状态
```

### 第三阶段：集成化

- LinkStar 内置 EasyTier
- 一键安装/启动
- 完整 GUI 配置
- 设备自动发现

## 运行方式

```bash
# 编译
go build -o linkstar

# 运行
./linkstar
```

## 注意事项

1. 需要在支持 UPnP 的路由器环境下使用
2. STUN 穿透对某些 NAT 类型可能不适用（NAT4 无法直接打洞）
3. 配置文件会在程序退出时自动保存
4. 组网模式需要目标网络内有 UPnP 能力的设备（如软路由）
