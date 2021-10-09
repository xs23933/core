package core

type ErrType uint64

const (
	// ErrTypeAny indicates any other error.
	ErrTypeAny ErrType = 1<<64 - 1
	// ErrorTypeNu indicates any other error.
	ErrTypeNu = 2
)
