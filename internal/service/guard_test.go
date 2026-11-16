package service

import (
	"errors"
	"strings"
	"testing"
)

// panic 必须变成可读 error，而不是让 net/http 关掉连接（前端 ERR_EMPTY_RESPONSE）。
func TestRecoverAsErrorTurnsPanicIntoError(t *testing.T) {
	var err error
	func() {
		defer recoverAsError("Boom", &err)
		panic("测试用的 panic")
	}()

	if err == nil {
		t.Fatal("panic 应当被转成 error")
	}
	if !errors.Is(err, err) || err.Error() == "" {
		t.Errorf("error 文案不该为空：%v", err)
	}
	if got := err.Error(); !strings.Contains(got, "Boom") || !strings.Contains(got, "测试用的 panic") {
		t.Errorf("error 应包含方法名与 panic 内容，实际 %q", got)
	}
}

// 正常返回时不能改写 err（也不能吞掉 nil）。
func TestRecoverAsErrorNoopWhenNoPanic(t *testing.T) {
	var err error
	func() {
		defer recoverAsError("Fine", &err)
	}()
	if err != nil {
		t.Errorf("没有 panic 时不该产生 error：%v", err)
	}
}
