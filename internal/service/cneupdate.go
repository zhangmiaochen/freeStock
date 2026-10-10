package service

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

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
