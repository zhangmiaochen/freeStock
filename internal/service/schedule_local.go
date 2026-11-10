//go:build !windows

package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"freestock/internal/cne"
	"freestock/internal/config"
)

// 非 Windows 平台（Linux / NAS / Docker）的"自动更新"实现。
//
// 设计要点：
//
//  1. **状态就是 config.json 里的四个字段**（autoDaily/dailyTime/
//     autoStale/staleTime）。不另建一套设置，否则界面上的开关与
//     updatePrefs 会各说各话。
//  2. **触发靠进程内 goroutine**：容器/NAS 上应用是常驻的，到点由自己拉起
//     `cne run daily`。没有 cron 依赖，也不需要 root 改宿主机配置 ——
//     这正是 NAS 上最省事的做法。
//  3. **错过补跑**：应用重启后若发现"今天还没跑、而计划时间已过"，
//     第一次 tick 就会补跑。等价于 Windows 的 StartWhenAvailable=true。
//  4. "跑过没有"记在一个小 JSON 里（与 config.json 同目录），
//     避免重启后重复跑今天的更新。

const scheduleTickInterval = 30 * time.Second

// scheduleStateFile 记录每个任务"上一次真正发起的日期"。
//
// 单独一个文件而不是塞进 config.json：cne 调度的运行痕迹属于运行时状态，
// 不该混进用户设置里被界面读写。
const scheduleStateFile = "cne-schedule-state.json"

// scheduleRunState 是落盘的运行痕迹。
type scheduleRunState struct {
	// Daily / Stale 是 "YYYY-MM-DD"（本地时区）
	Daily string `json:"daily,omitempty"`
	Stale string `json:"stale,omitempty"`
}

// localScheduler 把 config.UpdatePrefs 适配成 cne.LocalScheduler。
//
// 它是 CneUpdateService 的一部分（不是独立对象）：需要同一个 store
// 与同一个 runner，才能保证"到点跑"和"手动跑"共用互斥闸。
type localScheduler struct {
	svc *CneUpdateService

	mu       sync.Mutex
	runState scheduleRunState
	stopCh   chan struct{}
	stopOnce sync.Once
	started  bool
	// lastErr 用于去重日志：同一个错误不每 30 秒刷一次
	lastErr map[string]string
}

func newLocalScheduler(svc *CneUpdateService) *localScheduler {
	s := &localScheduler{svc: svc, stopCh: make(chan struct{}), lastErr: map[string]string{}}
	s.loadRunState()
	return s
}

// ---------------------------------------------------------------------------
// cne.LocalScheduler 实现
// ---------------------------------------------------------------------------

// Query 返回单个任务的状态。
func (l *localScheduler) Query(name string) (*cne.TaskInfo, error) {
	prefs, _ := l.prefs()
	info := &cne.TaskInfo{
		Name:       name,
		Registered: true, // 进程内调度器不需要"注册"
		// 错过补跑是这个实现的内建语义，不是可选项
		StartWhenAvailable:      true,
		StartWhenAvailableKnown: true,
	}

	var enabled bool
	var at string
	switch name {
	case cne.TaskDaily:
		enabled, at = prefs.AutoDaily, prefs.DailyTime
	case cne.TaskStale:
		enabled, at = prefs.AutoStale, prefs.StaleTime
	default:
		return nil, fmt.Errorf("未知的计划任务 %q（可选：%s / %s）",
			name, cne.TaskDaily, cne.TaskStale)
	}

	info.Enabled = enabled
	// 配置里的时间**与启用状态无关**，总是报出去：
	// 界面上"把 17:30 改成 18:00"要知道当前值，而 NextRun 在禁用时是空的。
	if validClockLocal(at) {
		info.ConfiguredTime = strings.TrimSpace(at)
	}
	info.ScheduledState = "Enabled"
	if !enabled {
		info.ScheduledState = "Disabled"
	}
	info.Status = "Ready"
	if !enabled {
		info.Status = "Disabled"
	}
	if next, ok := nextRunText(at, time.Now()); ok && enabled {
		info.NextRun = next
	}
	// Command 在 Windows 上是"任务实际执行的命令行"；这里如实说明
	// 是谁在跑，避免用户以为还有一条外部命令。
	info.Command = "（进程内调度器）应用运行时自动执行"
	if last := l.lastRun(name); last != "" {
		info.LastRun = last
	}

	return info, nil
}

// QuerySchedule 返回两个任务的整体状态。
func (l *localScheduler) QuerySchedule() (*cne.ScheduleState, error) {
	daily, err := l.Query(cne.TaskDaily)
	if err != nil {
		return nil, err
	}
	stale, err := l.Query(cne.TaskStale)
	if err != nil {
		return nil, err
	}
	state := &cne.ScheduleState{Daily: *daily, Stale: *stale}

	switch {
	case state.Daily.Enabled && state.Stale.Enabled:
		state.Summary = fmt.Sprintf("两个任务都已启用，由应用内生调度器执行：主日更 %s，补抓落后 %s",
			state.Daily.NextRun, state.Stale.NextRun)
	case state.Daily.Enabled:
		state.Summary = fmt.Sprintf("只启用了主日更（下次 %s）；补抓落后未启用",
			state.Daily.NextRun)
	case state.Stale.Enabled:
		state.Summary = fmt.Sprintf("只启用了补抓落后（下次 %s）；主日更未启用",
			state.Stale.NextRun)
	default:
		state.Summary = "两个任务都未启用 —— 应用运行时不会自动更新"
	}

	state.Note = "当前是 Linux / NAS / 容器部署：定时任务由**应用进程内**的调度器执行，" +
		"不依赖宿主机 cron。应用（容器）停止期间不会执行；重启后若发现今天还没跑过、" +
		"而计划时间已过，会自动补跑一次。"
	return state, nil
}

// CreateTask 在进程内调度器里等价于"设置时间并启用"。
func (l *localScheduler) CreateTask(name, scriptPath, at string) error {
	if !validClockLocal(at) {
		return fmt.Errorf("时间格式应为 HH:MM，实际 %q", at)
	}
	if err := l.ChangeTaskTime(name, at); err != nil {
		return err
	}
	return l.SetTaskEnabled(name, true)
}

// ChangeTaskTime 改时间：直接写回 config.json 的 dailyTime/staleTime。
func (l *localScheduler) ChangeTaskTime(name, at string) error {
	if !validClockLocal(at) {
		return fmt.Errorf("时间格式应为 HH:MM，实际 %q", at)
	}
	prefs, _ := l.prefs()
	switch name {
	case cne.TaskDaily:
		prefs.DailyTime = at
	case cne.TaskStale:
		prefs.StaleTime = at
	default:
		return fmt.Errorf("未知的计划任务 %q", name)
	}
	l.svc.store.SetUpdatePrefs(prefs)
	slog.Info("进程内调度器时间已更新", "task", name, "at", at)
	return nil
}

// DeleteTask 等价于关闭任务（进程内调度器没有可删除的外部实体）。
func (l *localScheduler) DeleteTask(name string) error {
	return l.SetTaskEnabled(name, false)
}

// SetTaskEnabled 打开/关闭自动更新（写回 autoDaily/autoStale）。
func (l *localScheduler) SetTaskEnabled(name string, enabled bool) error {
	prefs, _ := l.prefs()
	switch name {
	case cne.TaskDaily:
		prefs.AutoDaily = enabled
	case cne.TaskStale:
		prefs.AutoStale = enabled
	default:
		return fmt.Errorf("未知的计划任务 %q", name)
	}
	l.svc.store.SetUpdatePrefs(prefs)
	slog.Info("进程内调度器开关已切换", "task", name, "enabled", enabled)
	return nil
}

// TaskEnabled 读回任务开关。
func (l *localScheduler) TaskEnabled(name string) (bool, error) {
	prefs, _ := l.prefs()
	switch name {
	case cne.TaskDaily:
		return prefs.AutoDaily, nil
	case cne.TaskStale:
		return prefs.AutoStale, nil
	default:
		return false, fmt.Errorf("未知的计划任务 %q", name)
	}
}

// ApplyRecommendedSettings：进程内调度器天然补跑，无需改任何设置。
func (l *localScheduler) ApplyRecommendedSettings(name string) error {
	if name != cne.TaskDaily && name != cne.TaskStale {
		return fmt.Errorf("只允许改本项目的两个任务，收到 %q", name)
	}
	// 顺带把"启动补跑"打开 —— 这正是用户点这个按钮想要的效果。
	prefs, _ := l.prefs()
	if !prefs.CatchUpOnStart {
		prefs.CatchUpOnStart = true
		l.svc.store.SetUpdatePrefs(prefs)
	}
	return nil
}

// StartWhenAvailable 恒为已开启且确定。
func (l *localScheduler) StartWhenAvailable(name string) (bool, bool) {
	return true, true
}

// ---------------------------------------------------------------------------
// 调度循环
// ---------------------------------------------------------------------------

// start 启动 ticker（幂等）。
func (l *localScheduler) start() {
	l.mu.Lock()
	if l.started {
		l.mu.Unlock()
		return
	}
	l.started = true
	l.mu.Unlock()
	go l.loop()
}

// stop 停掉 ticker（满足 scheduleBackend 接口）。
func (l *localScheduler) stop() {
	l.stopOnce.Do(func() { close(l.stopCh) })
}

// newScheduleBackend 是 Linux / NAS / Docker 上的工厂函数。
//
// ⚠ **在这里注册**（而不是让 main 或 service 构造器去调）：
// cne.QuerySchedule 等函数在 ServiceStartup 之前也可能被调用
// （比如界面首屏就拉一次状态）。注册得越早，"未初始化"的错误窗口越小。
func newScheduleBackend(s *CneUpdateService) scheduleBackend {
	l := newLocalScheduler(s)
	cne.RegisterLocalScheduler(l)
	return l
}

func (l *localScheduler) loop() {
	ticker := time.NewTicker(scheduleTickInterval)
	defer ticker.Stop()
	slog.Info("进程内调度器已启动", "interval", scheduleTickInterval.String())

	// 立刻检查一次：应用重启后可能要补跑今天错过的那次
	l.tick()

	for {
		select {
		case <-l.stopCh:
			slog.Info("进程内调度器已停止")
			return
		case <-ticker.C:
			l.tick()
		}
	}
}

// tick 检查两个任务是否到点。
func (l *localScheduler) tick() {
	prefs, _ := l.prefs()
	now := time.Now()

	l.maybeRun(cne.TaskDaily, prefs.AutoDaily, prefs.DailyTime, now, func() error {
		_, err := l.svc.runner().Start(cne.KindFull, "")
		return err
	})
	l.maybeRun(cne.TaskStale, prefs.AutoStale, prefs.StaleTime, now, func() error {
		_, err := l.svc.runner().Start(cne.KindStale, "")
		return err
	})
}

// maybeRun 判断某个任务今天是否该跑，并在该跑时发起。
//
// 判据由 dueNow（schedule_clock.go）给出，这里只负责"发起 + 记账"。
//
// ⚠ 只有 Start **成功**才记日期。被 ErrBusy 拦下时保持"今天没跑"，
// 下一个 tick 会重试 —— 不能因为"手动更新正占着闸"就丢掉今天的计划。
func (l *localScheduler) maybeRun(task string, enabled bool, at string, now time.Time, run func() error) {
	if !validClockLocal(at) {
		if enabled {
			l.logOnce(task, fmt.Sprintf("时间格式不是 HH:MM：%q，任务已跳过", at))
		}
		return
	}
	if !dueNow(enabled, at, l.lastRunDate(task), now) {
		return
	}

	scheduled, _ := scheduledToday(at, now)
	if err := run(); err != nil {
		l.logOnce(task, fmt.Sprintf("到点发起失败：%v", err))
		return
	}
	l.markRan(task, now)
	slog.Info("进程内调度器已发起更新", "task", task, "at", at,
		"scheduled", scheduled.Format(time.RFC3339))
}

// logOnce 同一个错误只记一次，避免每 30 秒刷屏。
func (l *localScheduler) logOnce(task, msg string) {
	l.mu.Lock()
	prev := l.lastErr[task]
	l.lastErr[task] = msg
	l.mu.Unlock()
	if prev != msg {
		slog.Warn("进程内调度器", "task", task, "msg", msg)
	}
}

// ---------------------------------------------------------------------------
// 运行痕迹落盘
// ---------------------------------------------------------------------------

func (l *localScheduler) statePath() string {
	cfg := ""
	if l.svc != nil && l.svc.store != nil {
		cfg = l.svc.store.Path()
	}
	if cfg == "" {
		return ""
	}
	dir := filepath.Dir(cfg)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, scheduleStateFile)
}

func (l *localScheduler) loadRunState() {
	path := l.statePath()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return // 首次运行没有这个文件，正常
	}
	var st scheduleRunState
	if err := json.Unmarshal(raw, &st); err != nil {
		slog.Warn("调度状态文件解析失败，按未运行处理", "path", path, "err", err)
		return
	}
	l.mu.Lock()
	l.runState = st
	l.mu.Unlock()
}

func (l *localScheduler) saveRunState() {
	path := l.statePath()
	if path == "" {
		return
	}
	l.mu.Lock()
	payload, err := json.MarshalIndent(l.runState, "", "  ")
	l.mu.Unlock()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("创建调度状态目录失败", "err", err)
		return
	}
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		slog.Warn("写调度状态失败", "path", path, "err", err)
	}
}

func (l *localScheduler) lastRunDate(task string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if task == cne.TaskStale {
		return l.runState.Stale
	}
	return l.runState.Daily
}

// lastRun 返回人类可读的"上次发起日期"，没有则空串。
func (l *localScheduler) lastRun(task string) string {
	if d := l.lastRunDate(task); d != "" {
		return d + "（进程内调度器发起）"
	}
	return ""
}

func (l *localScheduler) markRan(task string, now time.Time) {
	l.mu.Lock()
	today := now.Format("2006-01-02")
	if task == cne.TaskStale {
		l.runState.Stale = today
	} else {
		l.runState.Daily = today
	}
	l.mu.Unlock()
	l.saveRunState()
}

// prefs 读当前设置（store 缺失时给默认值，保证 tick 不会 nil panic）。
func (l *localScheduler) prefs() (config.UpdatePrefs, bool) {
	if l.svc == nil || l.svc.store == nil {
		return config.DefaultUpdatePrefs(), false
	}
	return l.svc.store.UpdatePrefs()
}

// 时间相关的纯函数在 schedule_clock.go（不带 build tag，便于在 Windows 上单测）。
