// Package logbridge lets independent clients use Core's configured logger without
// importing the application, database, discovery, or server packages.
package logbridge

import (
	"log"
	"sync/atomic"
)

type Sink struct {
	Debug func(string, ...any)
	Error func(string, ...any)
	Print func(string, ...any)
}

var sink atomic.Pointer[Sink]

func Set(s Sink) { sink.Store(&s) }
func Debug(format string, args ...any) {
	if s := sink.Load(); s != nil && s.Debug != nil {
		s.Debug(format, args...)
	}
}
func Error(format string, args ...any) {
	if s := sink.Load(); s != nil && s.Error != nil {
		s.Error(format, args...)
		return
	}
	log.Printf(format, args...)
}
func Print(format string, args ...any) {
	if s := sink.Load(); s != nil && s.Print != nil {
		s.Print(format, args...)
		return
	}
	log.Printf(format, args...)
}
