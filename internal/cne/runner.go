package cne

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 任务类型 —— 对应方案 §2.2/§2.3：**"补抓落后"和"补某一天"是两种不同操作**，
// UI 上不能只给一个按钮。
const (
	KindFull  = "full"  // 更新到今天（普通日更）
	KindStale = "stale" // 补抓落后（--stale-only）
	KindDate  = "date"  // 补指定交易日（--trade-date YYYY-MM-DD）
	// KindBackfill 补**历史区间**（cne backfill daily_bars --start X --end Y）。
	//
	// ⚠ 与前三种**不是同一条命令**：前三种都走 `cne run daily`（日更 pipeline，
	// 只认单日 `--trade-date`），而补历史要走另一个子命令 `cne backfill`。
	// 这就是"手动更新只能补交易日、不能补区间"的原因 —— 不是缺输入框，
	// 是缺一整条命令路径。
	//
	// 成本模型也不同：`cne backfill --help` 原文说"成本按源的计费单位算，
	// 不按窗口算。daily_bars 是逐标的抓取，所以 --start D --end D 和多年窗口
	// 一样要扫一遍全市场" —— 窗口宽窄几乎不影响成本，所以 UI 上不该暗示
	// "补一年 = 补一天的成本"，也不该让用户按天循环。
	KindBackfill = "backfill"
)

// BackfillDataset 是"补历史区间"能选的数据集。**不要写死单个数据集。**
//
// 教训（实机踩过）：先只支持 `daily_bars`，用户补完个股日线后仍然只有 728 个交易日 ——
// 因为**指数在另一个数据集** `index_bars` 里，而交易日历取自它：
//
//	`PortfolioService.tradingDates` → `loadBenchmark(000300.SH)` → `index_bars`
//
// 指数只覆盖 2023-10-09 起时，`tradingDates(20221012, 20261012)` 只返回 728 天，
// 之前的日子**根本不在日历里**，于是"补了数据却还是算不出那一年"。
//
// 两个数据集的**分区粒度也不同**：`daily_bars` 按日（trade_date=2023-10-09），
// `index_bars` 按年（trade_date=2023）—— 读取侧 `internal/cnlake/rows.go` 已处理。
type BackfillDataset struct {
	Name  string // cne 的数据集名
	Label string // 界面上给人看的名字
}

// BackfillDatasets 是允许回补的数据集白名单。
//
// 校验它的意义：数据集名拼错时 cne 的报错很难懂，而且**不会**在起进程前发现；
// 白名单能让错误立刻、明确地暴露在 UI 上。
var BackfillDatasets = []BackfillDataset{
	{Name: "daily_bars", Label: "个股日线 daily_bars"},
	{Name: "index_bars", Label: "指数日线 index_bars（交易日历/基准/低吸指数闸门都依赖它）"},
}

// BackfillDatasetNames 返回白名单里的数据集名（给前端/校验用）。
func BackfillDatasetNames() []string {
	out := make([]string, 0, len(BackfillDatasets))
	for _, d := range BackfillDatasets {
		out = append(out, d.Name)
	}
	return out
}

// IsBackfillDataset 判断数据集名是否在白名单内。
func IsBackfillDataset(name string) bool {
	for _, d := range BackfillDatasets {
		if d.Name == name {
			return true
		}
	}
	return false
}

// ErrBusy 表示已有更新在进行。
//
// 方案 §5.4 要求"再点更新要被拒，并说明原因"，而不是静默排队。
var ErrBusy = errors.New("已有一个数据更新在进行中")

// Options 是 Runner 的构造参数。
//
// 刻意不用 config.UpdatePrefs：本包不该依赖设置层的形状，
// 由 service 层把值取出来传进来即可。
type Options struct {
	// CNEPath 是 cne 可执行文件；空则按 PATH 找，再兜底常见绝对路径。
	CNEPath string
	// ProjectDir 是 cne 的工作目录，必须含 configs/cnequity.toml。
	ProjectDir string
	// LogDir 是日志目录；空则用 <ProjectDir>\data\cnequity\logs。
	LogDir string
	// OnFinish 在每次任务结束后回调（err 为 nil 表示进程正常退出）。
	//
	// 用途：数据湖刚落盘完，派生缓存（`cache/bars-by-symbol`）里的东西
	// 可能已经过期了 —— 调用方在这里立刻让它失效重建，而不是等下一次查询
	// 才发现源头变了。**在后台 goroutine 里调用**，不要阻塞太久。
	OnFinish func(job *Job, err error)
}

// Job 是一次更新任务。
type Job struct {
	ID        string
	Kind      string
	Args      []string
	StartedAt time.Time
	LogPath   string

	cmd      *exec.Cmd
	done     chan struct{}
	err      error
	canceled bool
}

// Finished 返回任务是否已结束。
func (j *Job) Finished() bool {
	if j == nil {
		return true
	}
	select {
	case <-j.done:
		return true
	default:
		return false
	}
}

// Runner 负责启动/观察/取消 cne 更新子进程。
//
// **同时只允许一个任务**（方案 §5.4 第 2 道闸）。
type Runner struct {
	mu      sync.Mutex
	opts    Options
	current *Job
	last    *Job

	// buildArgs 是给测试留的接缝：默认拼 `run daily ... --config ...`。
	buildArgs func(kind string, extra []string) ([]string, error)

	// catchUp 是漏日补跑的编排状态（见 catchup.go）
	catchUp catchUp

	// foreignRunning 同样是接缝：默认扫系统进程表。
	// 本机跑测试时可能**真的有 cne 在跑**（计划任务或上次残留），
	// 那时用例会被正当拦住 —— 所以测试里替换成"总说没有"。
	foreignRunning func() (int, bool)
}

// NewRunner 构造 Runner。
func NewRunner(opts Options) *Runner {
	r := &Runner{opts: opts}
	r.buildArgs = r.defaultArgs
	r.foreignRunning = foreignCneRunning
	return r
}

// defaultArgs 拼出 `cne run daily <args...> --config <配置文件>`。
//
// 命令形态固定（方案 §4.2）：一套执行器覆盖 full / stale / --trade-date / --group。
// 配置文件位置由 ConfigPath 解析：`<工程>/configs/cnequity.toml` 或 `<工程>/cnequity.toml`
// （后者是 Linux 上 ~/.config/cnequity 这种布局）。
//
// ⚠ `KindBackfill` 是**唯一**不走 `run daily` 的类型：补历史区间要用另一个子命令
// `cne backfill daily_bars --start X --end Y`（`extra` 由 `StartBackfill` 备好）。
func (r *Runner) defaultArgs(kind string, extra []string) ([]string, error) {
	configPath, err := ConfigPath(r.opts.ProjectDir)
	if err != nil {
		return nil, fmt.Errorf("找不到 cne 配置：%w", err)
	}
	var args []string
	if kind == KindBackfill {
		args = []string{"backfill"}
	} else {
		args = []string{"run", "daily"}
	}
	switch kind {
	case KindStale:
		args = append(args, "--stale-only")
	case KindDate:
		// extra[0] 是交易日（YYYY-MM-DD）
	case KindBackfill:
		// extra 是 ["<dataset>", "--start", X, "--end", Y]
	default:
		// full：不加额外参数
	}
	args = append(args, extra...)
	// --config 放最后，便于人眼核对
	args = append(args, "--config", configPath)
	return args, nil
}

// executable 决定用哪个 cne。
//
// 具体解析规则（目录 / 可执行文件 / 裸命令名 / 留空）集中在 ResolveCNE，
// 这里只做委托 —— 设置校验和诊断也要用同一套规则，不能各写一遍。
func (r *Runner) executable() (string, error) {
	return ResolveCNE(r.opts.CNEPath)
}

// SetOptions 更新 Runner 的选项（设置改了之后由 service 层调用）。
//
// 只改配置，不影响正在跑的任务 —— 新选项对**下一次** Start 生效。
func (r *Runner) SetOptions(opts Options) {
	r.mu.Lock()
	r.opts = opts
	r.mu.Unlock()
}

// CurrentLogPath 返回当前（或最近一次）任务的日志路径，没有则空串。
func (r *Runner) CurrentLogPath() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current != nil {
		return r.current.LogPath
	}
	if r.last != nil {
		return r.last.LogPath
	}
	return ""
}

// Start 启动一次更新并**立即返回**（方案 §2.6：单次 8~60 分钟，绝不能同步等待）。
//
// tradeDate 仅在 kind == KindDate 时使用（YYYY-MM-DD）。
func (r *Runner) Start(kind string, tradeDate string) (*Job, error) {
	var extra []string
	switch kind {
	case KindStale:
		// defaultArgs 会补 --stale-only
	case KindDate:
		if strings.TrimSpace(tradeDate) == "" {
			return nil, errors.New("补指定交易日需要提供日期（YYYY-MM-DD）")
		}
		extra = []string{"--trade-date", tradeDate}
	case KindFull:
		// 不加额外参数
	default:
		return nil, fmt.Errorf("未知的更新类型 %q", kind)
	}
	return r.start(kind, extra)
}

// StartBackfill 补**历史区间**：`cne backfill <dataset> --start X --end Y`。
//
// 与 `Start` 走的是不同子命令（`backfill` 而不是 `run daily`），所以单独一个入口。
// 互斥保护（同进程 ErrBusy / 跨进程 foreignRunning）由 `start` 统一处理 ——
// backfill 与计划任务里的日更**必须**互斥，否则会撞上 cne 自己的 ingest 锁。
//
// dataset 必须在 `BackfillDatasets` 白名单内：**个股日线（daily_bars）与指数日线
// （index_bars）是两个数据集**，只补前者会留下"交易日历没有那一年"的空档。
func (r *Runner) StartBackfill(dataset, start, end string) (*Job, error) {
	dataset = strings.TrimSpace(dataset)
	start, end = strings.TrimSpace(start), strings.TrimSpace(end)
	if dataset == "" {
		dataset = "daily_bars"
	}
	if !IsBackfillDataset(dataset) {
		return nil, fmt.Errorf("不支持回补数据集 %q（可选：%s）",
			dataset, strings.Join(BackfillDatasetNames(), " / "))
	}
	if start == "" || end == "" {
		return nil, errors.New("补历史区间需要提供起止日期（YYYY-MM-DD）")
	}
	// 这里只做**粗校验**：只比字符串，因为 YYYY-MM-DD 定长且字典序 == 日期序。
	// 具体合法性交给 cne（它会给出更准确的来源/范围报错）。
	if start > end {
		return nil, fmt.Errorf("区间起点 %s 晚于终点 %s", start, end)
	}
	return r.start(KindBackfill, []string{dataset, "--start", start, "--end", end})
}

// start 是 Start / StartBackfill 的公共实现（互斥 → 校验 → 跨进程检测 → 起进程）。
func (r *Runner) start(kind string, extra []string) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.current != nil && !r.current.Finished() {
		return nil, fmt.Errorf("%w（类型 %s，已运行 %s）",
			ErrBusy, r.current.Kind, time.Since(r.current.StartedAt).Round(time.Second))
	}

	// ⚠ **先校验参数，再查环境**。
	//
	// 次序反了会给出误导性的错误：当机器上已有 cne 在跑（很常见 —— 计划任务
	// 或用户自己起的），一个"补指定交易日但漏填日期"的请求会报
	// "已有 cne 进程在运行"，用户于是去等那个进程，而真正的问题是参数没填。
	// 参数错误是**请求本身有问题**，与机器状态无关，应当先报。
	// （参数校验在 Start / StartBackfill 里做，这里只做环境与启动。）
	exe, err := r.executable()
	if err != nil {
		return nil, err
	}

	args, err := r.buildArgs(kind, extra)
	if err != nil {
		return nil, err
	}

	// 第 3 道闸：跨进程检测。cne 自己有 ingest 锁，第二个任务会失败或空等，
	// 提前拒绝体验更好（方案 §5.4）。
	if pid, found := r.foreignRunning(); found {
		return nil, fmt.Errorf("检测到已有 cne 进程在运行（PID %d）—— 请等它结束，或先结束它", pid)
	}

	logDir := r.opts.LogDir
	if logDir == "" {
		logDir = filepath.Join(r.opts.ProjectDir, "data", "cnequity", "logs")
	}
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建日志目录失败：%w", err)
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("freestock-update-%s.log",
		time.Now().Format("20060102-150405")))
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("创建日志文件失败：%w", err)
	}

	cmd := hiddenCommand(exe, args...)
	cmd.Dir = r.opts.ProjectDir // cne 按相对路径找配置（方案 §4.2）
	cmd.Env = BuildEnv()        // §2.4
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// 不弹控制台窗口 + 独立进程组（见 setupHiddenProcess 的说明）
	setupHiddenProcess(cmd)

	job := &Job{
		ID:        fmt.Sprintf("cne-%d", time.Now().UnixNano()),
		Kind:      kind,
		Args:      args,
		StartedAt: time.Now(),
		LogPath:   logPath,
		cmd:       cmd,
		done:      make(chan struct{}),
	}

	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, fmt.Errorf("启动 cne 失败：%w", err)
	}
	slog.Info("cne 数据更新已启动",
		"kind", kind, "pid", cmd.Process.Pid, "args", strings.Join(args, " "), "log", logPath)

	r.current = job
	// 后台等结束：**这里不是同步等待**，Start 已经返回了
	go func() {
		waitErr := cmd.Wait()
		logFile.Close()
		r.mu.Lock()
		job.err = waitErr
		r.last = job
		onFinish := r.opts.OnFinish
		r.mu.Unlock()
		close(job.done)
		slog.Info("cne 数据更新结束",
			"kind", kind, "err", waitErr, "elapsed", time.Since(job.StartedAt).Round(time.Second))
		if onFinish != nil {
			onFinish(job, waitErr)
		}
	}()

	return job, nil
}

// Status 返回当前/最近任务的状态快照。
func (r *Runner) Status() *Status {
	r.mu.Lock()
	current, last := r.current, r.last
	r.mu.Unlock()

	job := current
	if job != nil && job.Finished() {
		job = nil // 已经结束，归到"最近一次"
	}
	if job == nil && last != nil {
		job = last
	}
	if job == nil {
		return &Status{}
	}

	st := &Status{
		ID:         job.ID,
		Kind:       job.Kind,
		Running:    !job.Finished(),
		StartedAt:  job.StartedAt,
		ElapsedSec: int(time.Since(job.StartedAt).Seconds()),
		LogPath:    job.LogPath,
		CanCancel:  !job.Finished(),
	}
	if job.Finished() {
		st.ElapsedSec = int(job.StartedAt.Sub(job.StartedAt).Seconds())
		if r.current == job {
			st.ElapsedSec = int(time.Since(job.StartedAt).Seconds())
		}
	}
	st.LogTail = tailFile(job.LogPath, 40)
	st.Steps = parseSteps(st.LogTail)
	// ⚠ 成功与否**不看退出码**（方案 §2.1：exit=1 时可能有 5 个步骤成功，
	// 而 degraded 的运行反而 exit=0）。这里只给"进程是否正常退出"，
	// 真正的结论要结合 manifest 的 rows_written 由上层判断。
	st.Outcome = outcomeOf(job)
	return st
}

// Cancel 取消当前任务（**杀整棵进程树**）。
//
// cne 会派生子进程，只杀父进程会留下孤儿 python（方案 §4.2、§11）。
func (r *Runner) Cancel() error {
	r.mu.Lock()
	job := r.current
	r.mu.Unlock()
	if job == nil || job.Finished() {
		return errors.New("当前没有正在运行的更新")
	}
	if job.cmd == nil || job.cmd.Process == nil {
		return errors.New("任务进程不可用")
	}
	pid := job.cmd.Process.Pid
	if err := killProcessTree(pid); err != nil {
		return fmt.Errorf("结束进程树失败：%w", err)
	}
	job.canceled = true
	slog.Info("已取消 cne 数据更新", "pid", pid)
	return nil
}

// Status 是给前端的进度快照。
type Status struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	Running    bool       `json:"running"`
	CanCancel  bool       `json:"canCancel"`
	StartedAt  time.Time  `json:"startedAt"`
	ElapsedSec int        `json:"elapsedSec"`
	LogPath    string     `json:"logPath"`
	LogTail    []string   `json:"logTail"`
	Steps      []StepInfo `json:"steps"`
	Outcome    string     `json:"outcome"`
}

// StepInfo 是从日志里解析出的一个步骤。
//
// cne 会打 `still working: fund_flow 2m50s (no output for 1m00s)`，
// 可以用来判断"卡住没卡住"（方案 §8.1）。
type StepInfo struct {
	Name    string `json:"name"`
	State   string `json:"state"` // running / success / failed
	Elapsed string `json:"elapsed,omitempty"`
	Rows    string `json:"rows,omitempty"`
}

func outcomeOf(job *Job) string {
	if !job.Finished() {
		return "进行中"
	}
	if job.canceled {
		return "已取消"
	}
	if job.err == nil {
		// ⚠ 这**不等于"数据都更新成功"** —— 方案 §2.1：degraded 的运行会 exit=0。
		// 真正的结论要结合 manifest 里每个数据集是否写了行。
		return "进程正常退出（数据完整性需看最近结果）"
	}
	return "进程异常退出：" + job.err.Error()
}

// tailFile 读文件末尾的 n 行。
func tailFile(path string, n int) []string {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	// 末尾常有空行
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// parseSteps 从日志尾部解析出步骤状态，**仅用于进度展示**。
//
// 方案明确：成功与否要看 manifest 的 rows_written，日志只用来做进度。
//
// ⚠ **格式是照着真实日志写的，不是照着文档示例**。
// 方案 §2.1 里给的示例是 `hot_rank success 1.2s 100 rows`，但真实日志
// 完全不是那个样子 —— 第一版解析器按"fields[0] 是步骤名、fields[1] 是状态"
// 写，结果对真实日志**一条都匹配不上**，卡片上的徽章一直是空的。
//
// 真实日志（取自 data/cnequity/logs/freestock-update-*.log）：
//
//	调度组 core：skipped_non_trading_day                       <- 无时间戳、中文
//	调度组 derivatives：跳过（它要抓的数据集全部处于关闭状态）
//	2026-10-10 12:39:19,788 INFO ...: Wave events:disclosures: ['a','b'] (parallel=True)
//	2026-10-10 12:39:19,805 INFO ...: Step announcement_index starting
//	2026-10-10 12:40:24,645 INFO cnequity.progress: still working: announcement_index 1m04s (no output for 1m04s)
//
// 所以改成**按出现的关键子串识别**，不依赖字段位置 —— 行首可能是时间戳，
// 也可能直接是中文。
func parseSteps(lines []string) []StepInfo {
	seen := map[string]int{}
	out := []StepInfo{}

	upsert := func(name, state, elapsed, note string) {
		if name == "" {
			return
		}
		step := StepInfo{Name: name, State: state, Elapsed: elapsed, Rows: note}
		if idx, ok := seen[name]; ok {
			// 同名取最后一次；但已经 success 的不被后面的 running 覆盖
			if out[idx].State == "success" {
				return
			}
			out[idx] = step
			return
		}
		seen[name] = len(out)
		out = append(out, step)
	}

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// ① `still working: <步骤> <用时> (no output for ...)`
		//    心跳 —— 用来判断"卡住没卡住"（方案 §8.1）
		if i := strings.Index(line, "still working:"); i >= 0 {
			rest := strings.TrimSpace(line[i+len("still working:"):])
			fields := strings.Fields(rest)
			if len(fields) >= 2 {
				// 去掉 "(no output for ...)" 部分
				elapsed := fields[1]
				upsert(fields[0], "running", elapsed, "")
				continue
			}
		}

		// ② `Step <步骤> starting`
		if i := strings.Index(line, "Step "); i >= 0 {
			rest := strings.TrimSpace(line[i+len("Step "):])
			fields := strings.Fields(rest)
			if len(fields) >= 2 && (fields[1] == "starting" || fields[1] == "start") {
				upsert(fields[0], "running", "", "")
				continue
			}
			// 也认 `<步骤> success/failed ...`（某些阶段会这么打）
			if len(fields) >= 2 && (fields[1] == "success" || fields[1] == "failed") {
				elapsed := ""
				if len(fields) > 2 {
					elapsed = fields[2]
				}
				upsert(fields[0], fields[1], elapsed, "")
				continue
			}
		}

		// ③ `调度组 <组名>：skipped_non_trading_day` 或 `调度组 <组名>：跳过（原因）`
		//    ⚠ 这条对用户最重要：它解释了"为什么这次没更新行情"
		if i := strings.Index(line, "调度组 "); i >= 0 {
			rest := strings.TrimSpace(line[i+len("调度组 "):])
			name, reason := splitGroupLine(rest)
			if name != "" {
				upsert(name, "skipped", "", reason)
				continue
			}
		}

		// ④ `Wave <阶段>:<名>: [数据集...]` —— 记成一条"进行中"的信息
		if i := strings.Index(line, "Wave "); i >= 0 {
			rest := strings.TrimSpace(line[i+len("Wave "):])
			if idx := strings.Index(rest, ":"); idx > 0 {
				name := strings.TrimSpace(rest[:idx])
				if name != "" {
					upsert("wave:"+name, "running", "", "")
				}
			}
		}
	}
	return out
}

// splitGroupLine 拆开 `core：skipped_non_trading_day` 这类行。
//
// 分隔符可能是中文全角冒号或英文冒号；原因可能是 `skipped_*` 码，
// 也可能是 `跳过（...）`。
func splitGroupLine(rest string) (name, reason string) {
	idx := strings.IndexAny(rest, "：:")
	if idx <= 0 {
		return "", ""
	}
	name = strings.TrimSpace(rest[:idx])
	reason = strings.TrimSpace(rest[idx+1:])
	// 去掉 `跳过（...）` 的外层
	if strings.HasPrefix(reason, "跳过") {
		if a := strings.IndexAny(reason, "（("); a >= 0 {
			inner := strings.Trim(reason[a:], "（）()")
			reason = "skipped: " + strings.TrimSpace(inner)
		} else {
			reason = "skipped"
		}
	}
	return name, reason
}

// 保留 context 的引用，避免未使用告警（后续如需超时控制会用到）
var _ = context.Background
