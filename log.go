package core

import (
	"log"
)

var (
	// Log 日志输出信息 (default)
	Log = func(f string, args ...interface{}) {
		log.Printf(f+"\n", args...)
	}
)
