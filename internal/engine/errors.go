package engine

import (
	"errors"
	"fmt"
)

// PermanentError 表示「再试一次也不会成功」的失败。
//
// 典型场景：ffmpeg 不存在、编码不兼容且没开转码兜底。
// 这类错误不该触发整任务重排 —— 重排会把几百个分片再下一遍，
// 既浪费带宽，又让用户在界面上看到反复重试的假象。
type PermanentError struct {
	msg string
	err error
}

func (e *PermanentError) Error() string { return e.msg }
func (e *PermanentError) Unwrap() error { return e.err }

func permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{msg: err.Error(), err: err}
}

// permanentf 用格式化文本构造永久错误，原始错误挂在 Unwrap 上。
func permanentf(err error, format string, args ...any) error {
	return &PermanentError{msg: fmt.Sprintf(format, args...), err: err}
}

// IsPermanent 判断错误是否属于「重试无意义」。
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}
