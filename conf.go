package core

import "github.com/pelletier/go-toml"

var (
	// Config 配置信息
	Config *toml.Tree
)

// NewConfig 读取配置文件
func NewConfig(file string) *toml.Tree {
	var err error
	Config, err = toml.LoadFile(file)
	if err != nil {
		panic(err.Error())
	}
	return Config
}
