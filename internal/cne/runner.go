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
)

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

// defaultArgs 拼出 `cne run daily <args...> --config <ProjectDir>\configs\cnequity.toml`。
//
// 命令形态固定（方案 §4.2）：一套执行器覆盖 full / stale / --trade-date / --group。
func (r *Runner) defaultArgs(kind string, extra []string) ([]string, error) {
	configPath := filepath.Join(r.opts.ProjectDir, "configs", "cnequity.toml")
	if _, err := os.Stat(configPath); err != nil {
		return nil, fmt.Errorf("找不到 cne 配置 %s：%w", configPath, err)
	}
	args := []string{"run", "daily"}
	switch kind {
	case KindStale:
		args = append(args, "--stale-only")
	case KindDate:
		// extra[0] 是交易日（YYYY-MM-DD）
	default:
		// full：不加额外参数
	}
	args = append(args, extra...)
	// --config 放最后，便于人眼核对
	args = append(args, "--config", configPath)
	return args, nil
}

// executable 决定用哪个 cne。
func (r *Runner) executable() (string, error) {
	if p := strings.TrimSpace(r.opts.CNEPath); p != "" {
		// 有路径分隔符 → 当路径校验；否则当命令名去 PATH 里找。
		// （只看 os.Stat 会把 "cne.exe" 这种裸命令名误判成不可用。）
		if strings.ContainsAny(p, `\/`) {
			if _, err := os.Stat(p); err != nil {
				return "", fmt.Errorf("配置的 cne 路径不可用（%s）：%w", p, err)
			}
			return p, nil
		}
		if found, err := exec.LookPath(p); err == nil {
			return found, nil
		}
		return "", fmt.Errorf("在 PATH 上找不到 %q", p)
	}
	if p, err := exec.LookPath("cne"); err == nil {
		return p, nil
	}
	// 兜底常见位置（实测这台机器 cne.exe 在这里）
	for _, cand := range []string{
		`D:\softInstal\python3.11.3\Scripts\cne.exe`,
	} {
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	return "", errors.New("找不到 cne 可执行文件：请在设置里指定 CNEPath，或把它放进 PATH")
}

// Start 启动一次更新并**立即返回**（方案 §2.6：单次 8~60 分钟，绝不能同步等待）。
//
// tradeDate 仅在 kind == KindDate 时使用（YYYY-MM-DD）。
func (r *Runner) Start(kind string, tradeDate string) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.current != nil && !r.current.Finished() {
		return nil, fmt.Errorf("%w（类型 %s，已运行 %s）",
			ErrBusy, r.current.Kind, time.Since(r.current.StartedAt).Round(time.Second))
	}

	// 第 3 道闸：跨进程检测。cne 自己有 ingest 锁，第二个任务会失败或空等，
	// 提前拒绝体验更好（方案 §5.4）。
	if pid, found := r.foreignRunning(); found {
		return nil, fmt.Errorf("检测到已有 cne 进程在运行（PID %d）—— 请等它结束，或先结束它", pid)
	}

	exe, err := r.executable()
	if err != nil {
		return nil, err
	}

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

	args, err := r.buildArgs(kind, extra)
	if err != nil {
		return nil, err
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

	cmd := exec.Command(exe, args...)
	cmd.Dir = r.opts.ProjectDir // cne 按相对路径找配置（方案 §4.2）
	cmd.Env = BuildEnv()        // §2.4
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Windows：独立进程组，取消时才能把整棵进程树带走
	setupProcessGroup(cmd)

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
		r.mu.Unlock()
		close(job.done)
		slog.Info("cne 数据更新结束",
			"kind", kind, "err", waitErr, "elapsed", time.Since(job.StartedAt).Round(time.Second))
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

// parseSteps 从日志尾部粗解析步骤状态，仅用于进度展示。
//
// 方案明确：**日志只用来做进度**，成功与否要看 manifest。
func parseSteps(lines []string) []StepInfo {
	seen := map[string]int{}
	out := []StepInfo{}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		// 形如 `fund_flow failed after 342.6s` / `hot_rank success 1.2s 100 rows`
		name := fields[0]
		if strings.Contains(name, ":") || strings.HasPrefix(name, "[") {
			continue
		}
		state := ""
		switch fields[1] {
		case "success", "failed", "running", "starting":
			state = fields[1]
		}
		if state == "" {
			continue
		}
		step := StepInfo{Name: name, State: state}
		if len(fields) > 2 {
			step.Elapsed = fields[2]
		}
		if idx, ok := seen[name]; ok {
			out[idx] = step // 同名步骤取最后一次
		} else {
			seen[name] = len(out)
			out = append(out, step)
		}
	}
	return out
}

// 保留 context 的引用，避免未使用告警（后续如需超时控制会用到）
var _ = context.Background
