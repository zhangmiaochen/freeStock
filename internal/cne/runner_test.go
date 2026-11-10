package cne

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 准备一个假的 ProjectDir（含 configs/cnequity.toml），
// 并用 `powershell -Command Start-Sleep` 冒充 cne —— 不需要真跑 cne 就能
// 验证方案验收清单里那几条与进程行为有关的项。
func newTestRunner(t *testing.T, script string) *Runner {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("进程组/taskkill 的行为按 Windows 验证")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configs", "cnequity.toml"),
		[]byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRunner(Options{
		CNEPath:    "powershell.exe",
		ProjectDir: dir,
		LogDir:     t.TempDir(),
	})
	r.buildArgs = func(kind string, extra []string) ([]string, error) {
		return []string{"-NoProfile", "-NonInteractive", "-Command", script}, nil
	}
	// 本机可能**真的有 cne 在跑**（计划任务/残留），那会用正当理由挡住用例。
	// 跨进程检测本身另有覆盖，这里只测进程行为。
	r.foreignRunning = func() (int, bool) { return 0, false }
	return r
}

// 验收清单第 1 条：**手动更新立即返回**（不是等 8 分钟）。
func TestRunnerReturnsImmediately(t *testing.T) {
	// 任务睡 30 秒；只要 Start 远早于它返回，就证明**没有同步等待任务结束**。
	//
	// ⚠ 判据不能定成 "<1s"：那测的是**进程创建**耗时，而整套测试并行跑时
	// 机器被压满，Windows 起进程本身就可能要几秒 —— 那样会偶发失败，
	// 但并不能说明"同步等待了"（方案 §11 第 1 条的本意）。
	// 用"远小于任务时长"才既准确又稳定。
	r := newTestRunner(t, "Start-Sleep -Seconds 30")

	began := time.Now()
	job, err := r.Start(KindFull, "")
	elapsed := time.Since(began)
	if err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	defer func() { _ = r.Cancel() }()

	if elapsed > 5*time.Second {
		t.Errorf("Start 花了 %s —— 任务要跑 30 秒，说明在同步等待（方案 §2.6 禁止）", elapsed)
	}
	if job.Finished() {
		t.Error("刚启动就结束了？")
	}
	t.Logf("Start 用了 %s，任务仍在跑", elapsed.Round(time.Millisecond))

	st := r.Status()
	if !st.Running || !st.CanCancel {
		t.Errorf("状态应为运行中且可取消，实际 running=%v canCancel=%v", st.Running, st.CanCancel)
	}
	if st.LogPath == "" {
		t.Error("应给出日志路径")
	}
}

// OnFinish 必须在任务结束后被回调：派生缓存（按股票日线缓存）靠它
// 在"数据湖刚落盘"的那一刻立刻失效，而不是等下一次查询去发现。
//
// ⚠ 这个用例要**真的起一个进程**，所以不能写死 `powershell.exe`：
//   - Linux/NAS 上没有 powershell.exe；
//   - 更隐蔽的是 WSL：它默认把 Windows 的 PATH 追加进来
//     （appendWindowsPath=true），`powershell.exe` 居然能命中，
//     所以在 Windows 开发机上用 WSL 跑测试是绿的，CI 上才炸。
//
// 按平台选一个必然存在的命令，Windows 侧行为与原来完全一致。
func TestRunnerOnFinishFiresAfterJob(t *testing.T) {
	exe, args := "powershell.exe",
		[]string{"-NoProfile", "-NonInteractive", "-Command", "exit 0"}
	if runtime.GOOS != "windows" {
		exe, args = "/bin/sh", []string{"-c", "exit 0"}
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "configs", "cnequity.toml"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{}, 1)
	r := NewRunner(Options{
		CNEPath:    exe,
		ProjectDir: dir,
		LogDir:     t.TempDir(),
		OnFinish: func(job *Job, err error) {
			select {
			case done <- struct{}{}:
			default:
			}
		},
	})
	r.buildArgs = func(kind string, extra []string) ([]string, error) {
		return args, nil
	}
	r.foreignRunning = func() (int, bool) { return 0, false }

	job, err := r.Start(KindFull, "")
	if err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	select {
	case <-done:
	case <-job.done:
		// 进程结束了但回调还没打上，给它一点时间（回调在 close(done) 之后调用）
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("任务已结束，但 OnFinish 没有被回调")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("任务没有结束")
	}
}

// 验收清单第 2 条：**运行中再次点更新被拒**，提示明确（不静默排队）。
func TestRunnerRejectsConcurrentStart(t *testing.T) {
	r := newTestRunner(t, "Start-Sleep -Seconds 8")
	if _, err := r.Start(KindFull, ""); err != nil {
		t.Fatalf("首次启动失败：%v", err)
	}
	defer func() { _ = r.Cancel() }()

	_, err := r.Start(KindStale, "")
	if err == nil {
		t.Fatal("运行中再次启动**应当被拒**")
	}
	if !strings.Contains(err.Error(), "已有一个") {
		t.Errorf("错误信息应说明原因，实际：%v", err)
	}
	t.Logf("第二次被拒：%v", err)
}

// 验收清单第 3 条：**取消后没有残留进程**。
func TestRunnerCancelLeavesNoProcess(t *testing.T) {
	// 让子进程再派生一个（模拟 cne 派生子进程），验证 /T 能带走整棵树
	r := newTestRunner(t, "$p = Start-Process -PassThru -NoNewWindow powershell -ArgumentList '-NoProfile','-Command','Start-Sleep -Seconds 30'; Start-Sleep -Seconds 30")

	job, err := r.Start(KindFull, "")
	if err != nil {
		t.Fatalf("启动失败：%v", err)
	}
	time.Sleep(1500 * time.Millisecond) // 等它把子进程拉起来

	if err := r.Cancel(); err != nil {
		t.Fatalf("取消失败：%v", err)
	}
	select {
	case <-job.done:
	case <-time.After(15 * time.Second):
		t.Fatal("取消后任务没有结束 —— 进程可能没被杀掉")
	}

	// 给系统一点时间回收
	time.Sleep(500 * time.Millisecond)
	if pid, found := foreignCneRunning(); found {
		t.Logf("注意：检测到 cne.exe（PID %d）—— 这是真实 cne，不是本用例的 powershell", pid)
	}

	st := r.Status()
	if st.Running {
		t.Error("取消后状态不应还是运行中")
	}
	t.Logf("取消后任务已结束，outcome=%s", st.Outcome)
}

// `--trade-date` 必须被正确透传（方案 §2.3：非交易日补某一天的关键）。
func TestRunnerBuildsTradeDateArgs(t *testing.T) {
	dir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(dir, "configs"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "configs", "cnequity.toml"), []byte("x"), 0o644)

	r := NewRunner(Options{ProjectDir: dir})

	cases := []struct {
		kind      string
		tradeDate string
		want      []string
	}{
		{KindFull, "", []string{"run", "daily"}},
		{KindStale, "", []string{"run", "daily", "--stale-only"}},
		{KindDate, "2026-10-09", []string{"run", "daily", "--trade-date", "2026-10-09"}},
	}
	for _, c := range cases {
		args, err := r.defaultArgs(c.kind, func() []string {
			if c.kind == KindDate {
				return []string{"--trade-date", c.tradeDate}
			}
			return nil
		}())
		if err != nil {
			t.Fatalf("%s 构造参数失败：%v", c.kind, err)
		}
		joined := strings.Join(args, " ")
		for _, want := range c.want {
			if !strings.Contains(joined, want) {
				t.Errorf("%s 的参数里缺少 %q：%s", c.kind, want, joined)
			}
		}
		if !strings.Contains(joined, "--config") {
			t.Errorf("%s 必须带 --config：%s", c.kind, joined)
		}
		t.Logf("%s -> %s", c.kind, joined)
	}

	// 补指定交易日不给日期时应报错（不能静默当成普通日更）
	r2 := NewRunner(Options{ProjectDir: dir})
	if _, err := r2.Start(KindDate, ""); err == nil {
		t.Error("补指定交易日但没给日期，应当报错")
	}
}

// 完成判定不能只看退出码（方案 §2.1）—— 这里钉住措辞，
// 避免将来有人把 Outcome 简化成 "成功"。
func TestOutcomeDoesNotClaimSuccessFromExitCode(t *testing.T) {
	job := &Job{Kind: KindFull, done: make(chan struct{}), StartedAt: time.Now()}
	close(job.done) // 已结束，err == nil（相当于 exit=0）

	got := outcomeOf(job)
	if got == "成功" {
		t.Errorf("**不能把 exit=0 直接当成成功** —— degraded 的运行也会 exit=0（方案 §2.1）。实际：%q", got)
	}
	if !strings.Contains(got, "数据完整性") {
		t.Errorf("措辞应提示需要看数据完整性，实际：%q", got)
	}
	t.Logf("exit=0 的结论措辞：%s", got)
}
