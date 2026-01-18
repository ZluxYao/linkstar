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
	//根据不同的level去展示颜色
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
	//自定义日期格式
	timestamp := entry.Time.Format("2006-01-02 15:04:05")
	if entry.HasCaller() {
		//自定义文件路径
		funcVal := entry.Caller.Function
		fileVal := fmt.Sprintf("%s:%d", path.Base(entry.Caller.File), entry.Caller.Line)
		//自定义输出格式
		fmt.Fprintf(b, "[%s] \x1b[%dm[%s]\x1b[0m [%s,%s] \x1b[%dm %s \x1b[0m\n", timestamp, levelColor, entry.Level, fileVal, funcVal, levelColor, entry.Message)
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
	file    *os.File   // 当前打开的日志文件
	errFile *os.File   //	错误日志
	date    string     // 当前的时间
	logPath string     // 日志目录
	mu      sync.Mutex // 锁
}

func (hook *Myhook) Fire(entry *logrus.Entry) error {
	// 1.写入到文件
	// 2.时间分片
	// 3.错误单独放

	hook.mu.Lock()
	defer hook.mu.Unlock()
	// 写入到文件
	msg, _ := entry.String()
	date := entry.Time.Format("2006-01-02")
	if hook.date != date {
		//换时间
		hook.rotateFiles(date)
		hook.date = date
	}
	// 错误单独放
	if entry.Level <= logrus.ErrorLevel {
		hook.errFile.Write([]byte(msg))
	}
	hook.file.Write([]byte(msg))

	return nil
}

func (hook *Myhook) rotateFiles(times string) error {
	if hook.file != nil {
		hook.file.Close()
		hook.errFile.Close()
	}
	if hook.file == nil {
		//创建目录
		logDir := fmt.Sprintf("%s/%s", hook.logPath, times)
		os.MkdirAll(logDir, 0666)
		logPath := fmt.Sprintf("%s/info.log", logDir)
		file, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		hook.file = file

		// 错误日志
		errlogPath := fmt.Sprintf("%s/err.log", logDir)
		errFile, _ := os.OpenFile(errlogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		hook.errFile = errFile

	}
	return nil
}

// 决定 哪些级别日志走 Fire 方法
func (*Myhook) Levels() []logrus.Level {
	return logrus.AllLevels
}
