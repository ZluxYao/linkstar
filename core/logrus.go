package core

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"sync"

	"github.com/sirupsen/logrus"
)

type MyLog struct {
}

// 颜色
const (
	red    = 31
	yellow = 33
	blue   = 36
	gray   = 37
)

func (MyLog) Format(entry *logrus.Entry) ([]byte, error) {
	var levelColor int
	switch entry.Level {
	case logrus.DebugLevel, logrus.TraceLevel:
		levelColor = gray
	case logrus.WarnLevel:
		levelColor = yellow
	case logrus.ErrorLevel, logrus.FatalLevel, logrus.PanicLevel:
		levelColor = red
	default:
		levelColor = blue
	}
	var b *bytes.Buffer
	if entry.Buffer != nil {
		b = entry.Buffer
	} else {
		b = &bytes.Buffer{}
	}
	timestamp := entry.Time.Format("2006-01-02 15:04:05")
	if entry.HasCaller() {
		funcVal := entry.Caller.Function
		fileVal := fmt.Sprintf("%s:%d", path.Base(entry.Caller.File), entry.Caller.Line)
		fmt.Fprintf(b, "[%s] \x1b[%dm[%s]\x1b[0m [%s,%s] \x1b[%dm %s \x1b[0m\n", timestamp, levelColor, entry.Level, fileVal, funcVal, levelColor, entry.Message)
	} else {
		fmt.Fprintf(b, "[%s] \x1b[%dm[%s]\x1b[0m \x1b[%dm %s \x1b[0m\n", timestamp, levelColor, entry.Level, levelColor, entry.Message)
	}
	return b.Bytes(), nil
}

func InitLogger() {
	logrus.SetLevel(logrus.DebugLevel)
	logrus.SetReportCaller(true)
	logrus.SetFormatter(MyLog{})
	logrus.AddHook(&Myhook{
		logPath: "logs",
	})
}

type Myhook struct {
	file    *os.File
	errFile *os.File
	date    string
	logPath string
	mu      sync.Mutex
}

func (hook *Myhook) Fire(entry *logrus.Entry) error {
	hook.mu.Lock()
	defer hook.mu.Unlock()

	date := entry.Time.Format("2006-01-02")
	if hook.date != date {
		if err := hook.rotateFiles(date); err != nil {
			return fmt.Errorf("rotateFiles失败: %w", err)
		}
		hook.date = date
	}

	msg, err := entry.String()
	if err != nil {
		return fmt.Errorf("序列化日志失败: %w", err)
	}

	if hook.file != nil {
		if _, err := hook.file.Write([]byte(msg)); err != nil {
			return fmt.Errorf("写入info.log失败: %w", err)
		}
	}

	if entry.Level <= logrus.ErrorLevel && hook.errFile != nil {
		if _, err := hook.errFile.Write([]byte(msg)); err != nil {
			return fmt.Errorf("写入err.log失败: %w", err)
		}
	}

	return nil
}

func (hook *Myhook) rotateFiles(date string) error {
	// 关闭旧文件
	if hook.file != nil {
		hook.file.Close()
		hook.file = nil
	}
	if hook.errFile != nil {
		hook.errFile.Close()
		hook.errFile = nil
	}

	// 创建日志目录，0755 保证目录有 x 位，linux 下可进入
	logDir := fmt.Sprintf("%s/%s", hook.logPath, date)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("创建日志目录失败 %s: %w", logDir, err)
	}

	// 打开 info.log
	infoPath := fmt.Sprintf("%s/info.log", logDir)
	file, err := os.OpenFile(infoPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("打开info.log失败 %s: %w", infoPath, err)
	}
	hook.file = file

	// 打开 err.log
	errPath := fmt.Sprintf("%s/err.log", logDir)
	errFile, err := os.OpenFile(errPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		// 打开 err.log 失败时，关掉已打开的 info.log，避免 fd 泄漏
		hook.file.Close()
		hook.file = nil
		return fmt.Errorf("打开err.log失败 %s: %w", errPath, err)
	}
	hook.errFile = errFile

	return nil
}

// Levels 决定哪些级别日志走 Fire 方法
func (*Myhook) Levels() []logrus.Level {
	return logrus.AllLevels
}
