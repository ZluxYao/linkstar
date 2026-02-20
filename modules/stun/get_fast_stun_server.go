package stun

import (
	"fmt"
	"linkstar/global"

	"net"
	"sync"
	"time"

	"github.com/pion/stun"
)

// 获取当前网络最快stun服务器
func GetFastStunServer() string {

	type result struct {
		server string
		delay  time.Duration
	}

	results := make(chan result, len(global.StunConfig.StunServerList))
	var wg sync.WaitGroup

	for _, server := range global.StunConfig.StunServerList {

		wg.Add(1)

		// 尝试建立TCP链接 超时2秒
		go func(srv string) {
			defer wg.Done()

			star := time.Now()
			conn, err := net.DialTimeout("tcp", srv, 1*time.Second)
			if err != nil {
				fmt.Printf("❌ %s - 建立tcp链接失败: %v\n", srv, err)
				return
			}
			defer conn.Close()

			//发送stun请求
			msg := stun.MustBuild(stun.TransactionID, stun.BindingRequest)

			_, err = conn.Write(msg.Raw)
			if err != nil {
				fmt.Printf("❌ %s - 发送STUN请求失败: %v\n", srv, err)
				return
			}

			// 设置读取超时时间
			conn.SetDeadline(time.Now().Add(3 * time.Second))

			// 读取响应
			buf := make([]byte, 1024)
			n, err := conn.Read(buf)
			if n == 0 || err != nil {
				fmt.Printf("%s - 读取失败：%s", srv, err)
			}

			delay := time.Since(star)
			fmt.Printf("✅ %s - %dms \n", srv, delay.Milliseconds())

			results <- result{srv, delay}

		}(server)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var bestStun string
	var bestDelay time.Duration = time.Hour

	//找最快的服务器
	for res := range results {
		if res.delay < bestDelay {
			bestDelay = res.delay
			bestStun = res.server
		}
	}
	return bestStun
}
