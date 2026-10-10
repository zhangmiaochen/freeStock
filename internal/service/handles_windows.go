package service

import (
	"runtime"
	"syscall"
	"unsafe"
)

// openHandleCount 返回当前进程打开的内核句柄数（仅 Windows 有意义）。
//
// **为什么要它**：用户报「历史回填报错 unable to open database file (14)」
// （SQLite 的 `SQLITE_CANTOPEN`，对应 `open()` 返回 EMFILE/ENFILE），
// 而**重启应用后就能成功** —— 这是典型的资源累积型故障。
//
// 但代码审查没找到明显的句柄泄漏（`os.Open`/`sql.Rows` 都有 Close），
// 所以不再靠猜：在数据库打开失败的那一刻把句柄数记进日志，
// 下次就能直接判断"是不是句柄耗尽"，而不是继续推理。
//
// 非 Windows 平台返回 -1（不适用）。
func openHandleCount() int {
	if runtime.GOOS != "windows" {
		return -1
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	proc := kernel32.NewProc("GetProcessHandleCount")
	handle, err := syscall.GetCurrentProcess()
	if err != nil {
		return -1
	}
	var count uint32
	// BOOL GetProcessHandleCount(HANDLE, PDWORD)
	ret, _, _ := proc.Call(uintptr(handle), uintptr(unsafe.Pointer(&count)))
	if ret == 0 {
		return -1
	}
	return int(count)
}
