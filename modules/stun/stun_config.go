package stun

import (
	"linkstar/modules/stun/model"
	"linkstar/utils/utilsFile"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
)

const stunConfigPath = "config/stunConfig.json"

// shutdownChan 用于接收退出信号
var shutdownChan = make(chan struct{})

// 读取stun_config 配置文件
func ReadStunConfig() (model.StunConfig, error) {
	var config model.StunConfig

	//检测文件是否存在
	if fileInfo, err := os.Stat(stunConfigPath); os.IsNotExist(err) || fileInfo.Size() == 0 {
		//不存在创建空配置文件
		return createStunConfig()

	} else {
		//文件存在读取配置文件
		config, err = utilsFile.ReadJsonFile[model.StunConfig](stunConfigPath)
		if err != nil {
			logrus.Error("StunConfig读取失败：", err)
			return config, err
		}

	}
	return config, nil

}

func createStunConfig() (model.StunConfig, error) {
	var config model.StunConfig
	// 首次创建，设置创建时间
	config.CreatedAt = time.Now()
	config.UpdatedAt = time.Now()

	// 确保 config 目录存在
	if err := os.MkdirAll("config", 0755); err != nil {
		logrus.Error("创建config目录失败：", err)
		return config, err
	}

	// 写入一个空的配置文件
	if err := utilsFile.WriteJsonFile(stunConfigPath, config); err != nil {
		logrus.Error("StunConfig写入失败：", err)
		return config, err
	}
	return config, nil
}

// UpdateStunConfig 更新stun配置文件
func UpdateStunConfig(config model.StunConfig) error {
	const stunConfigPath = "config/stunConfig.json"

	// 更新时间戳
	config.UpdatedAt = time.Now()

	// 写入配置文件
	if err := utilsFile.WriteJsonFile(stunConfigPath, config); err != nil {
		logrus.Error("StunConfig写入失败：", err)
		return err
	}

	logrus.Info("STUN配置文件已更新")
	return nil
}

// SetupShutdownHook 监听退出信号，确保配置文件被保存
func SetupShutdownHook(saveFn func()) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		logrus.Infof("收到退出信号: %v，正在保存配置...", sig)

		// 执行保存函数
		if saveFn != nil {
			saveFn()
		}

		logrus.Info("配置已保存，程序退出")
		os.Exit(0)
	}()
}
