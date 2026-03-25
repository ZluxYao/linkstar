package stun

import (
	"io"
	"net"
	"time"

	"github.com/sirupsen/logrus"
)

// 双向复制
func Forward(src net.Conn, targetAddr string, protocol string) {
	defer src.Close()

	dst, err := net.DialTimeout(protocol, targetAddr, 3*time.Second)
	if err != nil {
		logrus.Errorf("连接内网目标失败 [%s]: %v", targetAddr, err)
		return
	}
	defer dst.Close()

	go func() {
		_, _ = io.Copy(dst, src)
		dst.Close() // src断了，关掉dst，让下面的io.Copy立刻返回
	}()

	_, _ = io.Copy(src, dst)
	src.Close() // dst断了，关掉src，让上面的io.Copy立刻返回
}
