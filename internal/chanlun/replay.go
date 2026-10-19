package chanlun

// 无未来数据回放与审计（方案 §9）。
//
// 回放的做法不是"把图画一遍再按日期截断"，而是**对每个历史时点重新完整分析**：
// 只用 [0..i] 的 K 线重跑包含处理 → 分型 → 笔 → 中枢 → 背驰 → 信号。
// 这样每一步输出的都是"当时真正能确认的信息"，未来数据根本不参与计算。
//
// `Audit` 用同一机制反向校验：对每条已触发的信号，回到它自己的触发时点重算，
// 必须能算出同类型、同触发时间的信号。算不出来就说明某个环节用到了未来数据。

// ReplayStep 是回放的一个快照。
type ReplayStep struct {
	Index       int       `json:"index"`
	Date        int       `json:"date"`
	State       StateInfo `json:"state"`
	Trend       Trend     `json:"trend"`
	Position    Position  `json:"position"`
	Pivots      int       `json:"pivots"`
	Strokes     int       `json:"strokes"`
	Divergences int       `json:"divergences"`
	Signals     []Signal  `json:"signals"`
	Note        string    `json:"note"`
}

// Replay 是一次回放的完整结果。
type Replay struct {
	Code       string       `json:"code"`
	Level      Level        `json:"level"`
	LevelCN    string       `json:"levelCN"`
	Steps      []ReplayStep `json:"steps"`
	Step       int          `json:"step"`
	Notes      []string     `json:"notes"`
	Disclaimer string       `json:"disclaimer"`
}

// BuildReplay 从"最小可用数据点"开始逐根推进，输出每个时点的状态快照。
func BuildReplay(bars []Bar, level Level, cfg Config, info AdaptInfo) *Replay {
	cfg = cfg.Sanitize()
	bars = normalizeBars(bars)
	replay := &Replay{
		Level:      level,
		LevelCN:    LevelCN(level),
		Disclaimer: Disclaimer,
	}
	if len(bars) == 0 {
		replay.Notes = append(replay.Notes, "没有可回放的 K 线")
		return replay
	}

	start := cfg.MinBars
	if start >= len(bars) {
		replay.Notes = append(replay.Notes, "K 线不足，无法回放（至少需要 "+
			itoa(cfg.MinBars)+" 根）")
		return replay
	}

	// 限制快照数量：步长按需要放大，但不会小于配置的 ReplayStep
	step := cfg.ReplayStep
	if step < 1 {
		step = 1
	}
	available := len(bars) - start
	if cfg.ReplayMaxSteps > 0 && available/step > cfg.ReplayMaxSteps {
		step = available / cfg.ReplayMaxSteps
		if step < 1 {
			step = 1
		}
	}
	replay.Step = step

	for i := start - 1; i < len(bars); i += step {
		prefix := bars[:i+1]
		analysis := Analyze(prefix, level, cfg, info)
		stepInfo := ReplayStep{
			Index:       i,
			Date:        bars[i].Date,
			State:       analysis.State,
			Trend:       analysis.Trend,
			Position:    analysis.Position,
			Pivots:      len(analysis.Pivots),
			Strokes:     len(analysis.Strokes),
			Divergences: len(analysis.Divergences),
			Signals:     analysis.Signals,
			Note:        "当时只有 " + itoa(i+1) + " 根 K 线；之后的数据未参与计算",
		}
		replay.Steps = append(replay.Steps, stepInfo)
	}

	replay.Notes = append(replay.Notes,
		"回放对每个时点单独重算结构，未来 K 线不参与计算",
		"步长 "+itoa(step)+" 根，共 "+itoa(len(replay.Steps))+" 个快照")
	return replay
}

// AuditIssue 是一条"可能出现未来数据"的审计问题。
type AuditIssue struct {
	SignalType  SignalType `json:"signalType"`
	Level       Level      `json:"level"`
	TriggerDate int        `json:"triggerDate"`
	Message     string     `json:"message"`
}

// Audit 校验历史信号在"当时"是否确实成立（无未来数据审计）。
//
// 返回空切片表示通过。这个函数主要给单元测试用，也可以作为诊断接口暴露。
func Audit(bars []Bar, level Level, cfg Config, info AdaptInfo) []AuditIssue {
	cfg = cfg.Sanitize()
	bars = normalizeBars(bars)
	final := Analyze(bars, level, cfg, info)
	issues := make([]AuditIssue, 0)

	for _, sig := range final.Signals {
		if sig.TriggerIndex < 0 || sig.TriggerIndex >= len(bars) {
			continue
		}
		prefix := bars[:sig.TriggerIndex+1]
		asOf := Analyze(prefix, level, cfg, info)

		found := false
		for _, candidate := range asOf.Signals {
			if candidate.Type != sig.Type {
				continue
			}
			// 同一信号的触发时间必须一致：当时就算得出来，且不会等到未来才出现
			if candidate.TriggerDate == sig.TriggerDate {
				found = true
				break
			}
			if candidate.FormDate == sig.FormDate {
				found = true
				break
			}
		}
		if !found {
			issues = append(issues, AuditIssue{
				SignalType:  sig.Type,
				Level:       sig.Level,
				TriggerDate: sig.TriggerDate,
				Message:     "该信号在触发当日重算时不存在：说明结构或信号用到了未来数据",
			})
		}
	}
	return issues
}
