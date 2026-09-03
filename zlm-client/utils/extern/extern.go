package extern

import (
	"sync/atomic"
	"unsafe"
)

type Status int32

var stateName = map[Status]string{
	S_INVALID: "invalid",
	S_INITIAL: "initialized",
	S_SETTING: "setting",
	S_SETUPED: "setuped",
	S_STRTING: "starting",
	S_STARTED: "started",
	S_RUNNING: "running",
	S_PAUSING: "pausing",
	S_STOPING: "stopping",
	S_STOPPED: "stopped",
	S_DELTING: "deleting",
	S_DELETED: "deleted",
	E_INITING: "initing err",
	E_SETTING: "setting err",
	E_STRTING: "start   err",
	E_RUNNING: "running err",
	E_PAUSING: "pausing err",
	E_STOPING: "stop    err",
	E_DELTING: "delete  err",
	E_UNKNOWN: "unknow  err",
}

const (
	S_INVALID Status = iota
	S_INITIAL
	S_SETTING
	S_SETUPED
	S_STRTING
	S_STARTED
	S_RUNNING
	S_PAUSING
	S_STOPING
	S_STOPPED
	S_DELTING
	S_DELETED
	E_INITING
	E_SETTING
	E_STRTING
	E_RUNNING
	E_PAUSING
	E_STOPING
	E_DELTING
	E_UNKNOWN = 0xFF
)

func (s *Status) Str() string { return stateName[*s] }

func (s *Status) Set(st Status) {
	atomic.StoreInt32((*int32)(unsafe.Pointer(s)), int32(st))
}

func (s *Status) IsBegun() bool {
	v := Status(atomic.LoadInt32((*int32)(unsafe.Pointer(s))))
	return v >= S_STRTING && v <= S_PAUSING
}

func (s *Status) IsEnded() bool {
	v := Status(atomic.LoadInt32((*int32)(unsafe.Pointer(s))))
	return v >= S_STOPING && v <= S_DELETED
}

func (s *Status) IsError() bool {
	v := Status(atomic.LoadInt32((*int32)(unsafe.Pointer(s))))
	return v >= E_INITING && v <= E_UNKNOWN
}
