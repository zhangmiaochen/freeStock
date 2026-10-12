package service

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"freestock/internal/cne"
	"freestock/internal/config"
)

// CneUpdateService 暴露 cne 数据更新的手动入口与设置读写。
//
// 对应方案 docs/cne数据更新方案.md §7。**第 1 阶段只做手动更新闭环**：
// 手动发起 / 看进度 / 取消 / 读写设置。自动更新（计划任务）与漏日补跑
// 是第 2、3 阶段，方法留在这里但暂不实现，避免前端拿到一个假的可调用接口。
type CneUpdateService struct {
	store *config.Store

	mu sync.Mutex
	r  *cne.Runner
}

// NewCneUpdateService 构造服务。
func NewCneUpdateService(store *config.Store) *CneUpdateService {
	return &CneUpdateService{store: store}
}

// runnerOptions 把设置翻译成 Runner 的选项。
func (s *CneUpdateService) runnerOptions() cne.Options {
	prefs, _ := s.store.UpdatePrefs()
	return cne.Options{
		CNEPath:    prefs.CNEPath,
		ProjectDir: prefs.ProjectDir,
	}
}

// runner 取（或惰性构造）Runner。
func (s *CneUpdateService) runner() *cne.Runner {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.r == nil {
		s.r = cne.NewRunner(s.runnerOptions())
	}
	// 每次都用最新设置刷新 —— 用户可能刚改过 cne 路径/工程目录
	s.r.SetOptions(s.runnerOptions())
	return s.r
}

// JobInfo 是手动更新发起后立刻返回的信息。
type JobInfo struct {
	JobID   string `json:"jobId"`
	Kind    string `json:"kind"`
	Args    string `json:"args"`
	LogPath string `json:"logPath"`
	Message string `json:"message"`
}

// ManualUpdate 发起一次手动更新，**立即返回**（不等任务跑完）。
//
// kind：`full` 更新到今天 / `stale` 补抓落后 / `date` 补指定交易日。
// tradeDate 仅在 kind=date 时使用，格式 YYYY-MM-DD。
//
// 方案 §2.2/§2.3：**"补抓落后"和"补某一天"是两种不同操作** ——
// 实测 `--stale-only` 不覆盖日行情，非交易日跑普通日更会跳过行情组，
// 所以 UI 必须给三个入口，不能只给一个按钮。
func (s *CneUpdateService) ManualUpdate(kind string, tradeDate string) (*JobInfo, error) {
	if s.store == nil {
		return nil, errors.New("缺少配置存储，无法启动更新")
	}
	kind = strings.TrimSpace(strings.ToLower(kind))
	if kind == "" {
		kind = cne.KindFull
	}
	// 允许前端/文档里的几种写法：trade_date / trade-date / by-date 都归到 date
	switch strings.ReplaceAll(kind, "_", "-") {
	case "trade-date", "by-date", "tradedate":
		kind = cne.KindDate
	}
	switch kind {
	case cne.KindFull, cne.KindStale, cne.KindDate:
	default:
		return nil, fmt.Errorf("未知的更新类型 %q（可选 full / stale / date）", kind)
	}

	runner := s.runner()
	job, err := runner.Start(kind, strings.TrimSpace(tradeDate))
	if err != nil {
		// ErrBusy 与"已有外部 cne 进程"都要原样带给用户 —— 方案 §5.4
		// 要求"被拒时提示明确，不是静默排队"。
		return nil, err
	}

	slog.Info("手动数据更新已发起", "kind", kind, "job", job.ID, "log", job.LogPath)
	return &JobInfo{
		JobID:   job.ID,
		Kind:    job.Kind,
		Args:    strings.Join(job.Args, " "),
		LogPath: job.LogPath,
		Message: describeKind(kind, tradeDate),
	}, nil
}

// ManualBackfill 发起一次**历史区间回补**：`cne backfill daily_bars --start X --end Y`。
//
// ⚠ 为什么单独一个方法，而不是给 `ManualUpdate` 加参数：
//
//   - 前三种 kind 都走 `cne run daily`（日更 pipeline），**只认单日 `--trade-date`**；
//     补历史走的是另一个子命令 `cne backfill`。这是"手动更新只能补交易日、
//     不能补区间"的根本原因 —— 不是缺输入框，是缺一条命令路径。
//   - 成本模型不同：`cne backfill --help` 说"成本按源的计费单位算，不按窗口算。
//     daily_bars 是逐标的抓取，所以 --start D --end D 和多年窗口一样要扫一遍
//     全市场"。**窗口宽窄几乎不影响成本**，所以文案里不该暗示"补一年更贵"，
//     也不该让用户按天循环去补一整年。
//
// 互斥（同进程 / 跨进程）由 Runner 统一处理，与日更共用同一把闸 —— backfill
// 和计划任务里的日更必须串行，否则会撞上 cne 自己的 ingest 锁。
func (s *CneUpdateService) ManualBackfill(start, end string) (*JobInfo, error) {
	if s.store == nil {
		return nil, errors.New("缺少配置存储，无法启动更新")
	}
	start, end = strings.TrimSpace(start), strings.TrimSpace(end)
	if start == "" || end == "" {
		return nil, errors.New("补历史区间需要填写起止日期（YYYY-MM-DD）")
	}

	job, err := s.runner().StartBackfill(start, end)
	if err != nil {
		return nil, err
	}

	slog.Info("手动历史回补已发起",
		"start", start, "end", end, "job", job.ID, "log", job.LogPath)
	return &JobInfo{
		JobID:   job.ID,
		Kind:    job.Kind,
		Args:    strings.Join(job.Args, " "),
		LogPath: job.LogPath,
		Message: fmt.Sprintf("已开始回补 %s ~ %s 的日线。"+
			"⚠ 成本按**标的**算、与窗口宽窄几乎无关，所以不要按天循环补 —— "+
			"一次给足区间即可。全市场回补通常要几十分钟以上，"+
			"进度看 cne status，完成后数据湖会先校验再发布。", start, end),
	}, nil
}

func describeKind(kind, tradeDate string) string {
	switch kind {
	case cne.KindStale:
		return "已开始「补抓落后」。它会补齐各数据集落后的部分 —— " +
			"注意它**不覆盖日行情**，行情缺口请用「补指定交易日」。"
	case cne.KindDate:
		return fmt.Sprintf("已开始补跑 %s。非交易日补某一天必须走这个入口"+
			"（普通日更在非交易日会跳过行情组）。", tradeDate)
	default:
		return "已开始「更新到今天」。单次通常需要 8~60 分钟，可以关掉这个页面。"
	}
}

// CheckFreshness 检查数据湖里关键数据集的新鲜度（方案 §4.3）。
//
// 这是**漏日补跑**的判断依据：只看 `--stale-only` 的结论会以为"没落后"，
// 因为它的落后清单里根本没有 daily_bars（方案 §2.2 实测），
// 而行情可能确实缺了一天。
func (s *CneUpdateService) CheckFreshness() (*cne.FreshnessReport, error) {
	if s.store == nil {
		return nil, errors.New("缺少配置存储")
	}
	prefs, _ := s.store.UpdatePrefs()
	if prefs.ProjectDir == "" {
		return nil, errors.New("还没设置 cne 工程目录，无法找到数据湖")
	}
	report, err := cne.Check(prefs.ProjectDir, time.Now())
	if err != nil {
		return nil, err
	}
	slog.Info("数据新鲜度检查完成",
		"lastTrading", report.LastTrading, "behind", len(report.BehindDays),
		"summary", report.Summary)
	return report, nil
}

// ScheduleState 读回两个计划任务的状态（**只读**）。
//
// 界面靠它显示"确实排上了"，而不是只给用户一个开关（方案 §8.1 第 4 点）。
//
// ⚠ **没有提供 ApplySchedule**（创建/修改/删除计划任务）。
//
// 方案 §5.1 要求 freeStock 接管那两个任务，但那会**改动用户机器上的计划任务**，
// 且有明确的踩坑风险（§2.5：cne 自带调度器在这台机器上会自我暂停，
// 若与它双跑会重复取数 + 抢写入锁）。
// 这类操作应当连同"如何确认 CNEquity 侧的调度器已关闭、如何避免双跑"
// 一起设计，不该和"把读写能力做好"混在一起。现在先把**观测**做扎实，
// 用户在 `schtasks` 里能看到的，这里也能看到。
func (s *CneUpdateService) ScheduleState() (*cne.ScheduleState, error) {
	return cne.QuerySchedule()
}

// CatchUpInfo 是补跑的发起结果。
type CatchUpInfo struct {
	Started bool             `json:"started"`
	Dates   []int64          `json:"dates"`
	Message string           `json:"message"`
	State   cne.CatchUpState `json:"state"`
}

// StartCatchUp 按新鲜度检查的结果**逐日串行**补跑缺失的交易日。
//
// ⚠ **这是显式入口，不接自动启动**。
//
// 方案 §5.2 希望"启动时落后 1~3 天自动补"。但那意味着**每次开机都可能
// 跑几小时、并真的改动数据湖** —— 那是个独立的决定，不该和"把补跑编排写好"
// 混在一起。现在由用户点按钮触发；要接自动启动，应作为单独一步，
// 连同"如何提示、如何避免每次开机都跑"一起设计。
//
// 另外：这里会**先跑一次新鲜度检查**，用它的结果决定补哪些天 ——
// 不听前端传来的日期，避免界面过期导致补错。
func (s *CneUpdateService) StartCatchUp() (*CatchUpInfo, error) {
	if s.store == nil {
		return nil, errors.New("缺少配置存储")
	}
	prefs, _ := s.store.UpdatePrefs()
	if prefs.ProjectDir == "" {
		return nil, errors.New("还没设置 cne 工程目录，无法找到数据湖")
	}
	report, err := cne.Check(prefs.ProjectDir, time.Now())
	if err != nil {
		return nil, err
	}
	if len(report.BehindDays) == 0 {
		return &CatchUpInfo{
			Started: false,
			Message: report.Summary,
			State:   s.runner().CatchUpState(),
		}, nil
	}

	if err := s.runner().StartCatchUp(report.BehindDays); err != nil {
		return nil, err
	}
	return &CatchUpInfo{
		Started: true,
		Dates:   report.BehindDays,
		Message: fmt.Sprintf("已开始逐日补跑 %d 个交易日：%v。"+
			"补跑是串行的（cne 有写入锁），可能需要较久；"+
			"每天跑完可以再查新鲜度确认。", len(report.BehindDays), report.BehindDays),
		State: s.runner().CatchUpState(),
	}, nil
}

// CatchUpState 返回补跑进度。
func (s *CneUpdateService) CatchUpState() cne.CatchUpState {
	if s.store == nil {
		return cne.CatchUpState{}
	}
	return s.runner().CatchUpState()
}

// CancelCatchUp 请求停止补跑。
//
// 语义是"**当前这一天跑完后不再继续**"，不中途掐断正在跑的那天 ——
// 补一半的状态比多等一会儿更糟。要立刻停用 CancelUpdate()。
func (s *CneUpdateService) CancelCatchUp() error {
	if s.store == nil {
		return errors.New("缺少配置存储")
	}
	return s.runner().CancelCatchUp()
}

// DiagnoseSchedule 从**应用进程自身**实测一组事实，用于定位
// "系统找不到指定的路径"（ERROR_PATH_NOT_FOUND）。
//
// ⚠ 这个方法的存在理由：同一个调用在 `go test`（有控制台）里永远成功，
// 说明差异在应用进程的环境 —— 只有从应用里实测才看得到。
// 返回纯文本，用户可以直接贴出来。
func (s *CneUpdateService) DiagnoseSchedule() string {
	return cne.Diagnose()
}

// AutoUpdateStatus 是"自动更新"的整体状态。
type AutoUpdateStatus struct {
	// Enabled 表示两个任务是否都启用了
	Enabled bool `json:"enabled"`
	// DailyEnabled / StaleEnabled 分别表示各自的状态
	DailyEnabled bool `json:"dailyEnabled"`
	StaleEnabled bool `json:"staleEnabled"`
	// StartWhenAvailable 表示"错过时间后是否补跑"（两个任务都满足才为 true）
	StartWhenAvailable bool `json:"startWhenAvailable"`
	// Summary 是人类可读的一句话
	Summary string `json:"summary"`
	// Warning 是需要提醒用户的问题（例如"错过不会补跑"）
	Warning string `json:"warning,omitempty"`
}

// AutoUpdateStatus 报告自动更新当前是否真的会生效。
//
// ⚠ 这里刻意查两件事，而不只是"任务存不存在"：
//   - 任务是否 **Enabled**（禁用则不跑）
//   - **StartWhenAvailable** 是否为 true
//
// 实测本机两个任务 `Enabled=true`、时间正确，但
// `StartWhenAvailable=false` —— 意味着 17:30/21:00 时机器关着就
// **永远跳过那天**，而界面看上去完全正常。这是"自动更新看起来配好了、
// 实际不生效"的最常见原因。
func (s *CneUpdateService) AutoUpdateStatus() (*AutoUpdateStatus, error) {
	state, err := cne.QuerySchedule()
	if err != nil {
		return nil, err
	}
	st := &AutoUpdateStatus{
		DailyEnabled: state.Daily.Registered && state.Daily.Enabled,
		StaleEnabled: state.Stale.Registered && state.Stale.Enabled,
	}
	st.Enabled = st.DailyEnabled || st.StaleEnabled
	st.StartWhenAvailable = state.Daily.StartWhenAvailable && state.Stale.StartWhenAvailable

	switch {
	case !state.Daily.Registered && !state.Stale.Registered:
		st.Summary = "自动更新未配置：两个计划任务都不存在"
		st.Warning = "没有计划任务，应用关着时不会自动更新。"
	case !st.Enabled:
		st.Summary = "自动更新已关闭"
		st.Warning = "任务存在但被禁用，到点不会跑。"
	case !st.StartWhenAvailable:
		st.Summary = "自动更新已开启，但**错过时间后不会补跑**"
		st.Warning = fmt.Sprintf(
			"两个任务都启用了，但「错过计划开始时间后尽快启动」没打开。"+
				"如果 %s 时电脑关着或休眠，那一天会被**直接跳过**，"+
				"而任务列表看上去一切正常。建议点下面的按钮打开它。",
			firstClock(state))
	default:
		st.Summary = "自动更新已开启，且错过时间后会补跑"
	}
	return st, nil
}

func firstClock(state *cne.ScheduleState) string {
	if state.Daily.Registered && state.Daily.NextRun != "" {
		return "每天 17:30 / 21:00"
	}
	return "计划时间"
}

// SetAutoUpdate 开启/关闭自动更新（启用或禁用两个计划任务）。
//
// 关闭后应用不运行时也不会自动更新 —— 这就是"自动更新开关"。
// **只改启用状态，不改时间、不改要执行的命令。**
func (s *CneUpdateService) SetAutoUpdate(enabled bool) (*AutoUpdateStatus, error) {
	for _, name := range []string{cne.TaskDaily, cne.TaskStale} {
		if err := cne.SetTaskEnabled(name, enabled); err != nil {
			// 一个失败也要把另一个的状态报出来，别让用户以为"全没成"
			st, _ := s.AutoUpdateStatus()
			return st, err
		}
	}
	slog.Info("自动更新开关已切换", "enabled", enabled)
	return s.AutoUpdateStatus()
}

// FixScheduleSettings 把两个任务改成"错过时间后也能补跑"。
//
// ⚠ 只改四项设置（StartWhenAvailable / 电池两项 / 多实例策略），
// **不动触发器（时间）、不动要执行的命令** —— 遵守"只改现有两个任务"的边界。
func (s *CneUpdateService) FixScheduleSettings() (*AutoUpdateStatus, error) {
	for _, name := range []string{cne.TaskDaily, cne.TaskStale} {
		if err := cne.ApplyRecommendedSettings(name); err != nil {
			st, _ := s.AutoUpdateStatus()
			return st, err
		}
	}
	slog.Info("计划任务设置已调整为「错过也补跑」")
	return s.AutoUpdateStatus()
}

// PreviewSchedule 算出把两个计划任务改到目标时间需要做什么，**不执行任何操作**。
//
// 用户明确要求"改动前弹确认"，所以必须分成两步：先预览（含原始 schtasks
// 命令），用户确认后才调 ApplySchedule。
func (s *CneUpdateService) PreviewSchedule(dailyAt, staleAt string) (*cne.SchedulePlan, error) {
	return cne.PreviewSchedule(dailyAt, staleAt)
}

// ApplySchedule 执行计划任务的改动。
//
// ⚠ **必须 confirmed=true**。界面要把 PreviewSchedule 的结果（含原始命令）
// 展示给用户，确认后才带 confirmed=true 调这里。
//
// **只改现有的两个任务，不新建、不删除**：
//   - 新建 → 与 CNEquity 侧双跑、抢 cne 的写入锁（方案 §5.1）
//   - 删除 → 破坏 CNEquity 自己的用法
func (s *CneUpdateService) ApplySchedule(plan *cne.SchedulePlan, confirmed bool) (*cne.SchedulePlan, error) {
	if !confirmed {
		return nil, errors.New("未经确认：请先展示改动预览，确认后再执行")
	}
	return cne.ApplySchedule(plan, true)
}

// RecentRuns 读最近的运行记录（来自 cne 的 manifest.db）。
//
// ⚠ **这是判断"这次更新到底成没成"的唯一可靠方式**。
// 方案 §2.1 实测：一次 `--stale-only` 补跑 exit=1、run 状态 failed，
// 而 8 个步骤里 5 个成功、写了 25,159 行；反过来 degraded 的运行 exit=0。
// **退出码不能当成功判据。**
func (s *CneUpdateService) RecentRuns(limit int) ([]cne.RunSummary, error) {
	dir, err := s.projectDir()
	if err != nil {
		return nil, err
	}
	return cne.RecentRuns(dir, limit)
}

// LatestRunResult 返回最近一次运行的详细结果：每个数据集写了多少行、
// 是失败还是"上游暂无数据"。
func (s *CneUpdateService) LatestRunResult() (*cne.RunDetail, error) {
	dir, err := s.projectDir()
	if err != nil {
		return nil, err
	}
	return cne.LatestRun(dir)
}

func (s *CneUpdateService) projectDir() (string, error) {
	if s.store == nil {
		return "", errors.New("缺少配置存储")
	}
	prefs, _ := s.store.UpdatePrefs()
	if strings.TrimSpace(prefs.ProjectDir) == "" {
		return "", errors.New("还没设置 cne 工程目录")
	}
	return prefs.ProjectDir, nil
}

// UpdateStatus 返回当前/最近任务的进度快照。
func (s *CneUpdateService) UpdateStatus() (*cne.Status, error) {
	if s.store == nil {
		return &cne.Status{}, nil
	}
	return s.runner().Status(), nil
}

// CancelUpdate 取消当前任务（杀整棵进程树，不留孤儿 python）。
func (s *CneUpdateService) CancelUpdate() error {
	if s.store == nil {
		return errors.New("缺少配置存储")
	}
	return s.runner().Cancel()
}

// GetUpdatePrefs 读设置；第二个返回值表示是否保存过。
type UpdatePrefsInfo struct {
	Prefs      config.UpdatePrefs `json:"prefs"`
	Saved      bool               `json:"saved"`
	ConfigPath string             `json:"configPath"`
	// Warnings 是设置的可用性问题（目录不存在、配置缺失等）。
	// **不阻断保存**，但必须让界面显示出来 —— 否则用户保存成功、
	// 点更新时才失败，不知道问题在哪。
	Warnings []string `json:"warnings,omitempty"`
}

// GetUpdatePrefs 返回当前设置（未保存过时给默认值）。
func (s *CneUpdateService) GetUpdatePrefs() UpdatePrefsInfo {
	if s.store == nil {
		return UpdatePrefsInfo{Prefs: config.DefaultUpdatePrefs()}
	}
	prefs, saved := s.store.UpdatePrefs()
	return UpdatePrefsInfo{Prefs: prefs, Saved: saved, ConfigPath: s.store.Path()}
}

// SetUpdatePrefs 保存设置（写入前自动 Sanitize）。
//
// ⚠ **不只写配置**：同时校验 cne 可执行文件与工程目录能不能用，
// 否则用户会保存成功、点更新时才失败。校验结果通过 Message 返回，
// **不阻断保存**（用户可能想先把设置填好、稍后再装 cne）。
func (s *CneUpdateService) SetUpdatePrefs(prefs config.UpdatePrefs) (UpdatePrefsInfo, error) {
	if s.store == nil {
		return UpdatePrefsInfo{}, errors.New("缺少配置存储")
	}
	stored := s.store.SetUpdatePrefs(prefs)
	// 让 Runner 立刻用上新设置
	s.mu.Lock()
	if s.r != nil {
		s.r.SetOptions(cne.Options{CNEPath: stored.CNEPath, ProjectDir: stored.ProjectDir})
	}
	s.mu.Unlock()

	warnings := s.validate(stored)
	info := UpdatePrefsInfo{
		Prefs: stored, Saved: true, ConfigPath: s.store.Path(), Warnings: warnings,
	}
	for _, msg := range warnings {
		slog.Warn("数据更新设置有问题", "msg", msg)
	}
	return info, nil
}

// validate 返回设置里的可用性问题（人类可读）。
func (s *CneUpdateService) validate(prefs config.UpdatePrefs) []string {
	var out []string
	if prefs.ProjectDir == "" {
		out = append(out, "工程目录为空")
	} else if _, err := os.Stat(prefs.ProjectDir); err != nil {
		out = append(out, fmt.Sprintf("工程目录不存在：%s", prefs.ProjectDir))
	} else {
		cfgPath := prefs.ProjectDir + string(os.PathSeparator) + "configs" +
			string(os.PathSeparator) + "cnequity.toml"
		if _, err := os.Stat(cfgPath); err != nil {
			out = append(out, fmt.Sprintf("工程目录里找不到 %s", cfgPath))
		}
	}
	if prefs.CNEPath != "" {
		if _, err := os.Stat(prefs.CNEPath); err != nil &&
			!strings.ContainsAny(prefs.CNEPath, `\/`) {
			// 裸命令名 —— 交给 PATH 解析，不算问题
		} else if err != nil {
			out = append(out, fmt.Sprintf("cne 路径不存在：%s", prefs.CNEPath))
		}
	}
	return out
}
