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
		logrus.Errorf("❌ 连接内网目标失败 [%s]: %v", targetAddr, err)
		return
	}
	defer dst.Close()

	go func() {
		_, _ = io.Copy(dst, src)
	}()
	_, _ = io.Copy(src, dst)
}
