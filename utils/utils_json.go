package utils

import (
	"encoding/json"
	"os"
)

// 读取json配置文件
func ReadJsonFile[T any](filePath string) (T, error) {

	var result T

	// 读取文件
	data, err := os.ReadFile(filePath)
	if err != nil {
		return result, err
	}

	// 反序列化
	if err = json.Unmarshal(data, &result); err != nil {
		return result, err
	}

	return result, nil
}

// 写入json配置文件
func WriteJsonFile[T any](filePath string, obj T) error {
	// 序列化为json，带缩进
	data, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}

	// 写入文件，权限设置为0644
	if err = os.WriteFile(filePath, data, 0644); err != nil {
		return err
	}

	return nil
}
