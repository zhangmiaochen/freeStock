package cne

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// formatDateNumber 必须产出 cne 要的带横线形式 —— 方案 §2.3 的命令示例是
// `cne run daily --trade-date 2026-10-09`，写成 20261009 会被拒。
func TestFormatDateNumber(t *testing.T) {
	cases := map[int64]string{
		20261009: "2026-10-09",
		20260101: "2026-01-01",
		20261231: "2026-12-31",
		20260305: "2026-03-05", // 个位数月/日要补零
	}
	for in, want := range cases {
		if got := formatDateNumber(in); got != want {
			t.Errorf("formatDateNumber(%d) = %q，期望 %q", in, got, want)
		}
	}
}

// **串行是必须的**：cne 自己有 ingest 锁，并行跑要么失败要么空等。
//
// 判据用**时间戳**而不是计数器：每天的任务睡 300ms，串行的话相邻两次
// 启动的间隔必然 >= 300ms；一旦重叠，间隔会明显更小。
// （第一版写成"计数器只加不减"，等于在数总启动次数，测不出并发 —— 已修。）
func TestCatchUpRunsSerially(t *testing.T) {
	const sleepMS = 400

	var mu sync.Mutex
	var starts []time.Time
	var order []int64

	r := newTestRunner(t, "Start-Sleep -Milliseconds 400")
	base := r.buildArgs
	r.buildArgs = func(kind string, extra []string) ([]string, error) {
		mu.Lock()
		starts = append(starts, time.Now())
		if len(extra) >= 2 {
			var d int64
			_, _ = fmtSscan(extra[1], &d)
			order = append(order, d)
		}
		mu.Unlock()
		return base(kind, extra)
	}

	if err := r.StartCatchUp([]int64{20261007, 20261008, 20261009}); err != nil {
		t.Fatalf("启动补跑失败：%v", err)
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && r.CatchUpState().Running {
		time.Sleep(200 * time.Millisecond)
	}
	st := r.CatchUpState()
	if st.Running {
		t.Fatal("补跑没有在预期时间内结束")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 {
		t.Fatalf("应补 3 天，实际启动 %d 次：%v", len(order), order)
	}
	for i := 1; i < len(starts); i++ {
		gap := starts[i].Sub(starts[i-1])
		if gap < sleepMS*time.Millisecond*3/4 {
			t.Errorf("**第 %d 天与第 %d 天间隔只有 %s（< %dms）—— 说明出现了重叠，"+
				"而 cne 有 ingest 锁，必须逐日串行**",
				i, i+1, gap.Round(time.Millisecond), sleepMS*3/4)
		}
	}
	for i, want := range []int64{20261007, 20261008, 20261009} {
		if order[i] != want {
			t.Errorf("第 %d 天应为 %d，实际 %d（顺序必须按升序）", i+1, want, order[i])
		}
	}
	if st.Done != 3 || len(st.Failed) != 0 {
		t.Errorf("3 天都该成功：done=%d failed=%v", st.Done, st.Failed)
	}
	t.Logf("逐日串行：%v，间隔均 >= %dms，done=%d", order, sleepMS*3/4, st.Done)
}

// **某一天失败不能放弃后面所有天** —— 否则一个大缺口里有一天出问题，
// 剩下的永远补不上。
func TestCatchUpContinuesAfterFailure(t *testing.T) {
	r := newTestRunner(t, "Start-Sleep -Milliseconds 100")
	if err := r.StartCatchUp([]int64{20261007, 20261008, 20261009}); err != nil {
		t.Fatalf("启动补跑失败：%v", err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && r.CatchUpState().Running {
		time.Sleep(200 * time.Millisecond)
	}
	st := r.CatchUpState()
	if st.Running {
		t.Fatal("补跑没有结束")
	}
	// 这里三天都会成功（脚本正常退出），重点是"没有因为某天失败就中断"——
	// 由 TestCatchUpStartFailureIsRecordedAndContinues 直接构造失败场景。
	if st.Done != 3 {
		t.Errorf("3 天都该跑完：done=%d failed=%v", st.Done, st.Failed)
	}
	t.Logf("done=%d message=%s", st.Done, st.Message)
}

// 启动就失败（例如外部已有 cne 在跑）时，要记进 Failed 并且**继续下一天**。
func TestCatchUpStartFailureIsRecordedAndContinues(t *testing.T) {
	r := newTestRunner(t, "Start-Sleep -Milliseconds 50")
	// 让 external 检查永远说"有别的 cne 在跑" → Start 必然失败
	r.foreignRunning = func() (int, bool) { return 4242, true }

	if err := r.StartCatchUp([]int64{20261007, 20261008}); err != nil {
		t.Fatalf("补跑编排本身应当能启动：%v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && r.CatchUpState().Running {
		time.Sleep(100 * time.Millisecond)
	}
	st := r.CatchUpState()
	if len(st.Failed) != 2 {
		t.Errorf("两天都启动不了，都应记入 Failed，实际 %v", st.Failed)
	}
	if st.Done != 0 {
		t.Errorf("不该有成功的天，实际 done=%d", st.Done)
	}
	if st.LastError == "" {
		t.Error("应记录失败原因，便于用户判断是不是'已有任务在跑'")
	}
	if !strings.Contains(st.Message, "失败") {
		t.Errorf("收尾措辞应说明有失败，实际：%s", st.Message)
	}
	t.Logf("失败被记录且继续：failed=%v lastError=%s", st.Failed, st.LastError)
}

// 收尾措辞**不能说成"数据都补上了"** —— 进程正常退出不等于数据完整
// （方案 §2.1：exit=1 时可能有 5 个步骤成功，degraded 反而 exit=0）。
func TestCatchUpMessageDoesNotClaimDataIsComplete(t *testing.T) {
	r := newTestRunner(t, "Start-Sleep -Milliseconds 50")
	if err := r.StartCatchUp([]int64{20261009}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) && r.CatchUpState().Running {
		time.Sleep(100 * time.Millisecond)
	}
	msg := r.CatchUpState().Message
	if !strings.Contains(msg, "新鲜度") {
		t.Errorf("收尾应提示再查一次新鲜度确认，实际：%s", msg)
	}
	if strings.Contains(msg, "数据已补全") || strings.Contains(msg, "全部成功更新") {
		t.Errorf("**不能把进程退出说成数据补上了**：%s", msg)
	}
	t.Logf("收尾措辞：%s", msg)
}

// 没有日期时应当拒绝，而不是静默什么都不做。
func TestStartCatchUpRejectsEmpty(t *testing.T) {
	r := newTestRunner(t, "Start-Sleep -Milliseconds 10")
	if err := r.StartCatchUp(nil); err == nil {
		t.Error("空日期列表应当被拒绝")
	}
}

// 取消语义：**当前这一天跑完后不再继续**（不中途掐断，避免补一半的状态）。
func TestCancelCatchUpStopsAfterCurrentDay(t *testing.T) {
	r := newTestRunner(t, "Start-Sleep -Milliseconds 600")
	if err := r.StartCatchUp([]int64{20261007, 20261008, 20261009, 20261010}); err != nil {
		t.Fatal(err)
	}
	// 等第一天开始就请求停止
	time.Sleep(200 * time.Millisecond)
	if err := r.CancelCatchUp(); err != nil {
		t.Fatalf("请求停止失败：%v", err)
	}
	if !strings.Contains(r.CatchUpState().Message, "停止") {
		t.Errorf("请求停止后应给出提示，实际：%s", r.CatchUpState().Message)
	}

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && r.CatchUpState().Running {
		time.Sleep(200 * time.Millisecond)
	}
	st := r.CatchUpState()
	if st.Running {
		t.Fatal("补跑没有结束")
	}
	if len(st.Skipped) == 0 {
		t.Errorf("应当有被跳过的日期，实际 skipped=%v done=%d", st.Skipped, st.Done)
	}
	if st.Done >= 4 {
		t.Errorf("请求停止后不该把 4 天全跑完，实际 done=%d", st.Done)
	}
	t.Logf("停止生效：done=%d skipped=%v", st.Done, st.Skipped)
}

// 没有补跑时，取消应当报错而不是装作成功。
func TestCancelCatchUpWithoutRun(t *testing.T) {
	r := newTestRunner(t, "Start-Sleep -Milliseconds 10")
	if err := r.CancelCatchUp(); err == nil {
		t.Error("没有补跑时取消应当报错")
	}
}

// 小工具：把 "2026-10-09" 解析成 20261009（避免为一个测试引入 fmt）
func fmtSscan(s string, out *int64) (int, error) {
	var v int64
	digits := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '-' {
			continue
		}
		if s[i] < '0' || s[i] > '9' {
			return 0, errors.New("非日期")
		}
		v = v*10 + int64(s[i]-'0')
		digits++
	}
	if digits != 8 {
		return 0, errors.New("位数不对")
	}
	*out = v
	return 1, nil
}
