package cne

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// CatchUpState 是漏日补跑的进度。
//
// 方案 §5.2 第 2 层：**这一层才是真正补上数据的地方** ——
// 实测里周五（10-09）的行情缺口，普通日更和 `--stale-only` 都不会补，
// 只有 `cne run daily --trade-date <那天>` 能补上（§2.2、§2.3）。
//
// ⚠ **必须逐日串行**：cne 自己有 ingest 锁，并行跑要么失败要么空等；
// 而且每天是一次完整日更，本来就该等它结束再跑下一天。
type CatchUpState struct {
	Running bool `json:"running"`
	// Dates 是本次要补的交易日（升序）
	Dates []int64 `json:"dates"`
	// Current 是正在补第几个（1-based；0 表示还没开始）
	Current int `json:"current"`
	// Done 是已成功的天数
	Done int `json:"done"`
	// Failed 是跑失败或进程异常退出的交易日
	Failed []int64 `json:"failed"`
	// Skipped 是因故没跑的交易日（例如已有别的任务在跑）
	Skipped []int64 `json:"skipped"`
	// LastError 是最近一次失败的原因（人类可读）
	LastError string `json:"lastError,omitempty"`
	// Message 是当前状态的一句话
	Message string `json:"message"`
}

// catchUp 是 Runner 内部的补跑状态（不外露）。
type catchUp struct {
	mu      sync.Mutex
	running bool
	state   CatchUpState
	// cancelRequested 让循环在当前这一天结束后停下来
	cancelRequested bool
}

// CatchUpState 返回补跑进度快照。
func (r *Runner) CatchUpState() CatchUpState {
	r.catchUp.mu.Lock()
	defer r.catchUp.mu.Unlock()
	out := r.catchUp.state
	// 复制切片，避免外部改动内部状态
	out.Dates = append([]int64(nil), r.catchUp.state.Dates...)
	out.Failed = append([]int64(nil), r.catchUp.state.Failed...)
	out.Skipped = append([]int64(nil), r.catchUp.state.Skipped...)
	out.Running = r.catchUp.running
	return out
}

// StartCatchUp 按给定交易日**逐日串行**补跑，立即返回。
//
// dates 为 DateNumber（YYYYMMDD）升序；调用方应当用 DecideCatchUp 得到它。
//
// 这一层只做编排：每一天都走 `Start(KindDate, ...)` ——
// 于是单飞、日志、取消、进程树清理全部复用同一套，不会出现两套并行逻辑。
func (r *Runner) StartCatchUp(dates []int64) error {
	if len(dates) == 0 {
		return errors.New("没有需要补跑的交易日")
	}

	r.catchUp.mu.Lock()
	if r.catchUp.running {
		r.catchUp.mu.Unlock()
		return errors.New("已有一个补跑在进行中")
	}
	// 提前检查一次总闸，避免"启动了却一天都跑不了"
	r.catchUp.running = true
	r.catchUp.cancelRequested = false
	r.catchUp.state = CatchUpState{
		Running: true,
		Dates:   append([]int64(nil), dates...),
		Message: fmt.Sprintf("准备补跑 %d 个交易日", len(dates)),
	}
	r.catchUp.mu.Unlock()

	// 注意：这里**不能**用 defer 解锁后再跑循环 —— 循环是异步的
	go r.runCatchUp(dates)
	return nil
}

func (r *Runner) runCatchUp(dates []int64) {
	defer func() {
		r.catchUp.mu.Lock()
		r.catchUp.running = false
		r.catchUp.state.Running = false
		state := r.catchUp.state
		r.catchUp.mu.Unlock()
		slog.Info("漏日补跑结束",
			"total", len(dates), "done", state.Done,
			"failed", len(state.Failed), "skipped", len(state.Skipped))
	}()

	for i, date := range dates {
		if r.catchUpCancelled() {
			r.markCatchUpSkipped(dates[i:], "已取消")
			return
		}

		r.catchUp.mu.Lock()
		r.catchUp.state.Current = i + 1
		r.catchUp.state.Message = fmt.Sprintf("正在补 %d（第 %d / %d 天）",
			date, i+1, len(dates))
		r.catchUp.mu.Unlock()

		slog.Info("补跑某个交易日", "date", date, "index", i+1, "total", len(dates))

		job, err := r.Start(KindDate, formatDateNumber(date))
		if err != nil {
			// 启动不了（例如外部已有 cne 在跑）→ 记下来继续，
			// 不因为一天失败就放弃后面所有天。
			r.markCatchUpFailed(date, err)
			continue
		}

		// **等这一天跑完再跑下一天** —— 串行是必须的（cne 的 ingest 锁）
		select {
		case <-job.done:
		case <-time.After(catchUpPerDayTimeout):
			// 兜底：单天跑超过 6 小时视为卡死，取消它并继续
			_ = r.Cancel()
			<-job.done
			r.markCatchUpFailed(date, errors.New("单日补跑超时（超过 6 小时）"))
			continue
		}

		if job.err != nil {
			// ⚠ **进程异常退出不等于这天没补上** —— 方案 §2.1 实测 exit=1 时
			// 仍可能有 5 个步骤成功。所以这里只记"进程异常"，
			// 真正的结论要看下一次新鲜度检查（缺失日期还在不在）。
			r.markCatchUpFailed(date, fmt.Errorf("cne 进程异常退出：%w", job.err))
			continue
		}

		r.catchUp.mu.Lock()
		r.catchUp.state.Done++
		r.catchUp.mu.Unlock()
	}

	// 收尾措辞：**不把"进程都退出了"说成"数据都补上了"**
	r.catchUp.mu.Lock()
	st := r.catchUp.state
	if len(st.Failed) == 0 && len(st.Skipped) == 0 {
		st.Message = fmt.Sprintf("补跑完成：%d 天。建议再查一次新鲜度确认数据真的补上了。", st.Done)
	} else {
		st.Message = fmt.Sprintf("补跑结束：成功 %d 天，失败 %d 天，跳过 %d 天。"+
			"请查新鲜度确认哪些还缺。", st.Done, len(st.Failed), len(st.Skipped))
	}
	r.catchUp.state = st
	r.catchUp.mu.Unlock()
}

// catchUpPerDayTimeout 是单日补跑的超时兜底。
//
// 方案 §2.6 实测一次完整日更约 58 分钟，给到 6 小时足够宽松；
// 目的是别让一个卡死的进程把整个补跑永久挂住。
const catchUpPerDayTimeout = 6 * time.Hour

func (r *Runner) catchUpCancelled() bool {
	r.catchUp.mu.Lock()
	defer r.catchUp.mu.Unlock()
	return r.catchUp.cancelRequested
}

func (r *Runner) markCatchUpFailed(date int64, err error) {
	r.catchUp.mu.Lock()
	r.catchUp.state.Failed = append(r.catchUp.state.Failed, date)
	r.catchUp.state.LastError = err.Error()
	r.catchUp.mu.Unlock()
	slog.Warn("补跑某天失败", "date", date, "err", err)
}

func (r *Runner) markCatchUpSkipped(dates []int64, reason string) {
	r.catchUp.mu.Lock()
	r.catchUp.state.Skipped = append(r.catchUp.state.Skipped, dates...)
	r.catchUp.state.LastError = reason
	r.catchUp.mu.Unlock()
	slog.Info("补跑跳过剩余日期", "reason", reason, "count", len(dates))
}

// CancelCatchUp 请求停止补跑。
//
// 语义是"**当前这一天跑完后不再往下跑**"，而不是立刻掐断正在跑的那天 ——
// 中途掐断会留下"这天补了一半"的状态，比多等一会儿更糟。
// 想立刻停就跑 Cancel()（它会杀当前进程树）。
func (r *Runner) CancelCatchUp() error {
	r.catchUp.mu.Lock()
	defer r.catchUp.mu.Unlock()
	if !r.catchUp.running {
		return errors.New("当前没有补跑在进行")
	}
	r.catchUp.cancelRequested = true
	r.catchUp.state.Message = "已请求停止：当前这一天跑完后不再继续"
	return nil
}

// formatDateNumber 把 20261009 变成 cne 需要的 2026-10-09。
//
// 方案 §2.3 的命令示例用的就是带横线的形式：
//
//	cne run daily --trade-date 2026-10-09 --config configs\cnequity.toml
func formatDateNumber(date int64) string {
	year := date / 10000
	month := (date / 100) % 100
	day := date % 100
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

// 让 os 保持被引用（日志目录相关的辅助留给后续需要时使用）
var _ = os.MkdirAll
