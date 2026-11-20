package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"freestock/internal/score"
)

// BackfillJobState 是"历史回填"后台任务的可见状态。
//
// 为什么要有它：回填一整年（2600+ 交易日）实测要 **10 分钟**，而前端是一个
// 同步 HTTP 请求 —— 只要中间任何一层（代理 / 浏览器 / 长连接）掐断，
// 页面就会永远停在"回填中…"（服务器其实已经跑完并记了"历史回填完成"）。
// 改成"起任务 + 轮询状态"之后，请求是毫秒级的，页面刷新/关掉也不影响任务。
type BackfillJobState struct {
	Status     string `json:"status"` // running / succeeded / failed / interrupted / idle
	StrategyID string `json:"strategyId"`
	Strategy   string `json:"strategyLabel"`
	StartDate  int    `json:"startDate"`
	EndDate    int    `json:"endDate"`
	Limit      int    `json:"limit"`
	Force      bool   `json:"force"`
	// 进度：按交易日
	DaysDone   int `json:"daysDone"`
	DaysTotal  int `json:"daysTotal"`
	CurrentDay int `json:"currentDay"`
	// 结果摘要
	Days            int    `json:"days"`
	Scored          int    `json:"scored"`
	Reused          int    `json:"reused"`
	ScoredFromCache int    `json:"scoredFromCache"`
	SkippedDays     int    `json:"skippedDays"`
	Elapsed         string `json:"elapsed"`
	Error           string `json:"error,omitempty"`

	StartedAt  string `json:"startedAt,omitempty"`
	UpdatedAt  string `json:"updatedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

var (
	backfillJobMu     sync.Mutex
	backfillJobActive atomic.Bool
)

// backfillStaleAfter 是判定"running 已成孤儿"的时间窗。
const backfillStaleAfter = 3 * time.Minute

func (s *PortfolioService) backfillStatePath() string {
	// 与迁移状态文件同目录（数据目录）
	return filepath.Join(filepath.Dir(s.migrationStatePath()), "backfill-state.json")
}

// GetBackfillJob 返回当前回填任务状态；陈旧 running 会报 interrupted。
func (s *PortfolioService) GetBackfillJob() BackfillJobState {
	state := s.readBackfillState()
	if state.Status == "running" && !backfillJobActive.Load() {
		if updated, err := time.Parse(time.RFC3339, state.UpdatedAt); err != nil || time.Since(updated) > backfillStaleAfter {
			state.Status = "interrupted"
		}
	}
	return state
}

// StartBackfill 在后台启动一次历史回填（已有任务在跑时不排队，直接返回当前状态）。
func (s *PortfolioService) StartBackfill(strategyID string, startDate, endDate, limit int, force bool) (BackfillJobState, error) {
	if !backfillJobActive.CompareAndSwap(false, true) {
		return s.GetBackfillJob(), fmt.Errorf("回填已在进行中，请等待完成（可刷新页面查看进度）")
	}
	if strategyID == "" {
		strategyID = score.StrategyTrendGrowth
	}
	state := BackfillJobState{
		Status: "running", StrategyID: strategyID, Strategy: strategyLabelOf(strategyID),
		StartDate: startDate, EndDate: endDate, Limit: limit, Force: force,
		StartedAt: time.Now().Format(time.RFC3339),
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	if err := s.writeBackfillState(state); err != nil {
		backfillJobActive.Store(false)
		return state, err
	}
	slog.Info("回填任务已启动（后台执行，页面可离开）",
		"strategy", strategyID, "from", startDate, "to", endDate, "limit", limit, "force", force)
	go func() {
		defer backfillJobActive.Store(false)
		result, err := s.refreshBackfill(strategyID, startDate, endDate, limit, force,
			func(done, total, date int) {
				job := s.readBackfillState()
				if job.Status != "running" {
					job = state
				}
				job.DaysDone, job.DaysTotal, job.CurrentDay = done, total, date
				job.UpdatedAt = time.Now().Format(time.RFC3339)
				_ = s.writeBackfillState(job)
			})
		now := time.Now()
		if err != nil {
			state.Status = "failed"
			state.Error = err.Error()
			slog.Warn("回填任务失败", "err", err)
		} else {
			state.Status = "succeeded"
			state.Days = result.Days
			state.Scored = result.Scored
			state.Reused = result.Reused
			state.ScoredFromCache = result.ScoredFromCache
			state.SkippedDays = len(result.Skipped)
			state.Elapsed = result.Elapsed
			if len(result.Dates) > 0 {
				state.DaysTotal = len(result.Dates)
				state.DaysDone = len(result.Dates)
				state.CurrentDay = result.Dates[len(result.Dates)-1]
			}
			slog.Info("回填任务完成", "scored", result.Scored, "elapsed", result.Elapsed)
		}
		state.UpdatedAt = now.Format(time.RFC3339)
		state.FinishedAt = now.Format(time.RFC3339)
		_ = s.writeBackfillState(state)
	}()
	return state, nil
}

func (s *PortfolioService) readBackfillState() BackfillJobState {
	var state BackfillJobState
	path := s.backfillStatePath()
	if path == "" {
		return BackfillJobState{Status: "idle"}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return BackfillJobState{Status: "idle"}
	}
	if err := json.Unmarshal(raw, &state); err != nil || state.Status == "" {
		return BackfillJobState{Status: "idle"}
	}
	// 启动时文件里是 running、但本进程没有任务 → 交给 GetBackfillJob 判 interrupted
	return state
}

func (s *PortfolioService) writeBackfillState(state BackfillJobState) error {
	path := s.backfillStatePath()
	if path == "" {
		return fmt.Errorf("无法确定数据目录")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
