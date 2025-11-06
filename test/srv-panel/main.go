package main

import (
	"fmt"
	"net"
	"strings"

	"github.com/gin-gonic/gin"
)

// SRVRecord 结构体定义
type SRVRecord struct {
	Service  string `json:"service"`
	Priority uint16 `json:"priority"`
	Weight   uint16 `json:"weight"`
	Port     uint16 `json:"port"`
	Target   string `json:"target"`
}

// Response 响应结构
type Response struct {
	Success bool        `json:"success"`
	Data    []SRVRecord `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
	Debug   *DebugInfo  `json:"debug,omitempty"`
}

// DebugInfo 调试信息
type DebugInfo struct {
	Query       string   `json:"query"`
	Service     string   `json:"service"`
	Proto       string   `json:"proto"`
	Domain      string   `json:"domain"`
	DNSServers  []string `json:"dns_servers,omitempty"`
	ErrorDetail string   `json:"error_detail,omitempty"`
}

// SRVRequest 请求结构
type SRVRequest struct {
	Service string `json:"service" form:"service" binding:"required"`
	Proto   string `json:"proto" form:"proto" binding:"required"`
	Domain  string `json:"domain" form:"domain" binding:"required"`
}

// 解析 SRV 记录
func resolveSRV(service, proto, domain string) ([]SRVRecord, *DebugInfo, error) {
	// 构建查询名称
	query := fmt.Sprintf("_%s._%s.%s", service, proto, domain)

	debug := &DebugInfo{
		Query:   query,
		Service: service,
		Proto:   proto,
		Domain:  domain,
	}

	// 获取系统 DNS 配置
	config, err := net.DefaultResolver.LookupHost(nil, "")
	if err == nil {
		debug.DNSServers = config
	}

	// 方法1: 使用 net.LookupSRV (推荐)
	fmt.Printf("[查询] 正在查询 SRV: %s\n", query)
	_, addrs, err := net.LookupSRV(service, proto, domain)

	if err != nil {
		debug.ErrorDetail = err.Error()

		// 尝试方法2: 直接查询完整的 SRV 名称
		fmt.Printf("[重试] 尝试直接查询: %s\n", query)
		cname, addrs2, err2 := net.LookupSRV("", "", query)
		if err2 == nil {
			addrs = addrs2
			err = nil
			fmt.Printf("[成功] CNAME: %s, 记录数: %d\n", cname, len(addrs))
		} else {
			debug.ErrorDetail = fmt.Sprintf("方法1错误: %v | 方法2错误: %v", err, err2)
			return nil, debug, fmt.Errorf("SRV查询失败: %v", err)
		}
	}

	if len(addrs) == 0 {
		return nil, debug, fmt.Errorf("未找到 SRV 记录")
	}

	records := make([]SRVRecord, 0, len(addrs))
	for i, addr := range addrs {
		fmt.Printf("[记录 %d] Priority:%d Weight:%d Port:%d Target:%s\n",
			i+1, addr.Priority, addr.Weight, addr.Port, addr.Target)

		records = append(records, SRVRecord{
			Service:  query,
			Priority: addr.Priority,
			Weight:   addr.Weight,
			Port:     addr.Port,
			Target:   strings.TrimSuffix(addr.Target, "."),
		})
	}

	return records, debug, nil
}

// 处理 SRV 查询请求
func handleSRVQuery(c *gin.Context) {
	var req SRVRequest

	// 支持 GET 和 POST
	if c.Request.Method == "GET" {
		if err := c.ShouldBindQuery(&req); err != nil {
			c.JSON(400, Response{
				Success: false,
				Error:   "缺少必要参数: service, proto, domain",
			})
			return
		}
	} else {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, Response{
				Success: false,
				Error:   "无效的请求格式",
			})
			return
		}
	}

	// 清理输入
	req.Service = strings.TrimSpace(req.Service)
	req.Proto = strings.TrimSpace(req.Proto)
	req.Domain = strings.TrimSpace(req.Domain)

	// 移除可能的下划线前缀
	req.Service = strings.TrimPrefix(req.Service, "_")
	req.Proto = strings.TrimPrefix(req.Proto, "_")

	fmt.Printf("\n[请求] Service:%s Proto:%s Domain:%s\n", req.Service, req.Proto, req.Domain)

	// 解析 SRV 记录
	records, debug, err := resolveSRV(req.Service, req.Proto, req.Domain)

	if err != nil {
		c.JSON(200, Response{
			Success: false,
			Error:   err.Error(),
			Debug:   debug,
		})
		return
	}

	c.JSON(200, Response{
		Success: true,
		Data:    records,
		Debug:   debug,
	})
}

// 测试 DNS 连接
func handleDNSTest(c *gin.Context) {
	testDomains := []string{"google.com", "cloudflare.com", "baidu.com"}
	results := make(map[string]interface{})

	for _, domain := range testDomains {
		ips, err := net.LookupHost(domain)
		if err != nil {
			results[domain] = map[string]string{"error": err.Error()}
		} else {
			results[domain] = map[string]interface{}{"ips": ips}
		}
	}

	c.JSON(200, gin.H{
		"success": true,
		"results": results,
	})
}

// 直接查询（用于调试）
func handleDirectQuery(c *gin.Context) {
	query := c.Query("query")
	if query == "" {
		c.JSON(400, gin.H{"error": "需要 query 参数"})
		return
	}

	fmt.Printf("[直接查询] %s\n", query)

	cname, addrs, err := net.LookupSRV("", "", query)

	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"error":   err.Error(),
			"query":   query,
		})
		return
	}

	records := make([]SRVRecord, 0, len(addrs))
	for _, addr := range addrs {
		records = append(records, SRVRecord{
			Service:  query,
			Priority: addr.Priority,
			Weight:   addr.Weight,
			Port:     addr.Port,
			Target:   strings.TrimSuffix(addr.Target, "."),
		})
	}

	c.JSON(200, gin.H{
		"success": true,
		"cname":   cname,
		"data":    records,
		"query":   query,
	})
}

func main() {
	// 设置为 release 模式（生产环境）或 debug 模式（开发环境）
	// gin.SetMode(gin.ReleaseMode)

	r := gin.Default()

	// CORS 中间件
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	// 路由
	r.GET("/api/srv", handleSRVQuery)
	r.POST("/api/srv", handleSRVQuery)
	r.GET("/api/dns-test", handleDNSTest)
	r.GET("/api/direct", handleDirectQuery)

	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status": "ok",
		})
	})

	// 启动服务
	port := ":8080"
	fmt.Printf("\n🚀 SRV 解析服务已启动\n")
	fmt.Printf("📍 监听端口: %s\n", port)
	fmt.Printf("📡 API 端点:\n")
	fmt.Printf("   - SRV 查询: http://localhost:8080/api/srv\n")
	fmt.Printf("   - DNS 测试: http://localhost:8080/api/dns-test\n")
	fmt.Printf("   - 直接查询: http://localhost:8080/api/direct?query=_aa._tcp.istore\n")
	fmt.Printf("   - 健康检查: http://localhost:8080/health\n")
	fmt.Printf("\n💡 示例查询:\n")
	fmt.Printf("   curl 'http://localhost:8080/api/srv?service=aa&proto=tcp&domain=istore'\n\n")

	if err := r.Run(port); err != nil {
		fmt.Printf("❌ 服务启动失败: %v\n", err)
	}
}

// 运行前需要安装 Gin:
// go get -u github.com/gin-gonic/gin
