package cnlake

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// 回归测试：所有文件都读不出来时，bucketize 必须**返回错误**，不能挂死。
//
// 背景：并发版最初写成"worker 出错就 return"。jobs 是无缓冲通道，主循环还在
// 往里发；当所有 worker 都因错误退出后，`jobs <- path` 永久阻塞 ——
// 缓存构建从此挂死，而且没有任何日志。这类死锁在 NAS 上（权限/半截文件）
// 很容易被触发，必须用测试把它钉住。
func TestBucketizeAllFilesUnreadableDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "buckets")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	// 故意给一堆不存在的文件：每个 worker 的第一个文件就失败。
	files := make([]string, 0, 32)
	for i := 0; i < 32; i++ {
		files = append(files, filepath.Join(dir, "missing", "part.parquet"))
	}

	done := make(chan error, 1)
	go func() {
		cache := &SymbolBarsCache{}
		done <- cache.bucketize(files, out)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("文件全都读不出来时应当返回错误")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bucketize 挂死：worker 全部退出后发送方仍在阻塞（死锁）")
	}
}

// 部分文件可读、部分不可读：同样要返回错误而不是挂死。
func TestBucketizePartialFailureDoesNotHang(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "buckets")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	files := []string{
		filepath.Join(dir, "missing-a.parquet"),
		filepath.Join(dir, "missing-b.parquet"),
	}

	done := make(chan error, 1)
	go func() {
		cache := &SymbolBarsCache{}
		done <- cache.bucketize(files, out)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("应当返回错误")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("bucketize 挂死（死锁）")
	}
}
