package cne

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 端到端：**设置里只填目录**，Runner 也要能真的把 cne 跑起来。
//
// 这是容器/NAS 的主用法：cne 装在宿主机上，按原路径挂进容器，界面里填那个目录。
// 只测 ResolveCNE 能解析出路径还不够 —— 真正会翻车的是"解析出来了但 exec 失败"
// （比如挂进来的脚本没有执行位、或 shebang 指向的解释器不存在）。
//
// 这里用一个真的 shell 脚本冒充 cne，跑完检查它确实被执行、输出落进了日志。
func TestRunnerUsesDirectoryConfiguredCNE(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用 #!/bin/sh 脚本伪造 cne，Windows 上不适用")
	}

	// 1. 假 cne：放在"宿主机目录"里，内容会写进 stdout（Runner 会重定向到日志）
	binDir := t.TempDir()
	cnePath := filepath.Join(binDir, "cne")
	script := "#!/bin/sh\necho fake-cne-ok\n"
	if err := os.WriteFile(cnePath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	// 2. 最小可用的工程目录（Runner 会校验 configs/cnequity.toml）
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "configs", "cnequity.toml"),
		[]byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 3. CNEPath 只给目录 —— 不给可执行文件
	r := NewRunner(Options{
		CNEPath:    binDir,
		ProjectDir: proj,
		LogDir:     t.TempDir(),
	})
	r.foreignRunning = func() (int, bool) { return 0, false }

	job, err := r.Start(KindFull, "")
	if err != nil {
		t.Fatalf("填目录时应当能启动：%v", err)
	}

	select {
	case <-job.done:
	case <-time.After(30 * time.Second):
		t.Fatal("任务没有结束")
	}

	logPath := r.Status().LogPath
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读日志失败（%s）：%v", logPath, err)
	}
	if !strings.Contains(string(raw), "fake-cne-ok") {
		t.Errorf("cne 没有被真正执行，日志里没有它的输出：\n%s", string(raw))
	}
}
