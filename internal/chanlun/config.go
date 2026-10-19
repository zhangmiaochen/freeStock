package chanlun

import "math"

// LevelWeight 是一个级别的权重（方案 §8.1、§8.2）。
type LevelWeight struct {
	Level  Level   `json:"level"`
	Weight float64 `json:"weight"`
}

// TypeWeight 是一个买卖点类型的权重。
type TypeWeight struct {
	Type   SignalType `json:"type"`
	Weight float64    `json:"weight"`
}

// DivergenceConfig 是背驰力度衡量方式（方案 §6.3，权重可配）。
type DivergenceConfig struct {
	// StructureWeight 结构力度权重：级别、是否创新高/新低、是否被前段包含
	StructureWeight float64 `json:"structureWeight"`
	// MacdWeight MACD 力度权重：柱面积、黄白线高度
	MacdWeight float64 `json:"macdWeight"`
	// SlopeWeight 价格斜率权重：单位时间价格变化、K 线实体
	SlopeWeight float64 `json:"slopeWeight"`
	// VolumeWeight 成交量权重：仅辅助
	VolumeWeight float64 `json:"volumeWeight"`
	// Threshold 背驰阈值：strengthB/strengthA 小于它才判背驰
	Threshold float64 `json:"threshold"`
	// MinLegBars 参与比较的离开段最少根数（太短的段不参与）
	MinLegBars int `json:"minLegBars"`
}

// Config 是缠论引擎配置。
//
// 默认值来自方案 §8.2 的示例权重（年 1.0 / 周 0.8 / 日 0.6 / 日线笔 0.4，
// 一买 0.7 / 二买 0.85 / 三买 0.9）。
type Config struct {
	// MinLevel 最小可用级别：只有日 K 时应为 day-stroke（方案 §3.2）
	MinLevel Level `json:"minLevel"`
	// OperLevel 操作级别：个股图与选股默认分析的级别
	OperLevel Level `json:"operLevel"`

	LevelWeights []LevelWeight `json:"levelWeights"`
	TypeWeights  []TypeWeight  `json:"typeWeights"`

	Divergence DivergenceConfig `json:"divergence"`

	// VolumeAux 量价辅助开关：关掉后量能维度权重归零（方案 §11）
	VolumeAux bool `json:"volumeAux"`

	// ScoreThreshold 综合分阈值：低于它的信号仍会算出，但排序下沉
	ScoreThreshold float64 `json:"scoreThreshold"`
	// ShowCandidate 是否展示候选信号
	ShowCandidate bool `json:"showCandidate"`
	// ShowConflict 是否展示结构矛盾提示
	ShowConflict bool `json:"showConflict"`

	// ExpiryBars 信号触发后多少根 K 线内未确认则过期
	ExpiryBars int `json:"expiryBars"`
	// InvalidTolerance 失效容差（例如 0.005 = 跌破 0.5% 才判失效）
	InvalidTolerance float64 `json:"invalidTolerance"`

	// MaxSignalsPerType 每种买卖点最多保留多少条"已失效/已过期"的历史信号。
	//
	// 活跃信号（候选/触发/确认）不受限制；这条限制只用来避免把五年前的失效信号
	// 全铺在图上与面板里。设为 0 时使用默认值。
	MaxSignalsPerType int `json:"maxSignalsPerType"`

	// LookbackDays 分析窗口（日 K 根数）；<=0 表示用全部历史
	LookbackDays int `json:"lookbackDays"`

	// ReplayStep 回放步长（每次推进多少根 K 线）
	ReplayStep int `json:"replayStep"`
	// ReplayMaxSteps 回放最多输出多少个快照
	ReplayMaxSteps int `json:"replayMaxSteps"`

	// MinBars 最少 K 线根数，低于它判"数据不足"
	MinBars int `json:"minBars"`
}

// DefaultConfig 返回默认配置（与方案 §8.2 的示例一致）。
func DefaultConfig() Config {
	return Config{
		MinLevel:  LevelDayStroke,
		OperLevel: LevelDay,
		LevelWeights: []LevelWeight{
			{Level: LevelDayStroke, Weight: 0.4},
			{Level: LevelDay, Weight: 0.6},
			{Level: LevelWeek, Weight: 0.8},
			{Level: LevelMonth, Weight: 0.9},
			{Level: LevelYear, Weight: 1.0},
		},
		TypeWeights: []TypeWeight{
			{Type: SignalBuy1, Weight: 0.7},
			{Type: SignalBuy2, Weight: 0.85},
			{Type: SignalBuy3, Weight: 0.9},
			{Type: SignalSell1, Weight: 0.7},
			{Type: SignalSell2, Weight: 0.85},
			{Type: SignalSell3, Weight: 0.9},
		},
		Divergence: DivergenceConfig{
			StructureWeight: 0.30,
			MacdWeight:      0.40,
			SlopeWeight:     0.20,
			VolumeWeight:    0.10,
			Threshold:       0.90,
			MinLegBars:      3,
		},
		VolumeAux:         true,
		ScoreThreshold:    45,
		ShowCandidate:     true,
		ShowConflict:      true,
		ExpiryBars:        30,
		InvalidTolerance:  0.005,
		MaxSignalsPerType: 3,
		LookbackDays:      0,
		ReplayStep:        1,
		ReplayMaxSteps:    400,
		MinBars:           30,
	}
}

// Sanitize 把配置收敛到合法范围。**永不报错，只纠正**（与 config.KlinePrefs 同一约定）。
func (c Config) Sanitize() Config {
	def := DefaultConfig()

	if !ValidLevel(c.MinLevel) {
		c.MinLevel = def.MinLevel
	}
	if !ValidLevel(c.OperLevel) {
		c.OperLevel = def.OperLevel
	}
	// 操作级别不能低于最小级别
	if LevelRank(c.OperLevel) < LevelRank(c.MinLevel) {
		c.OperLevel = c.MinLevel
	}

	c.LevelWeights = sanitizeLevelWeights(c.LevelWeights, def.LevelWeights)
	c.TypeWeights = sanitizeTypeWeights(c.TypeWeights, def.TypeWeights)

	d := c.Divergence
	d.StructureWeight = clampFloat(d.StructureWeight, 0, 1, def.Divergence.StructureWeight)
	d.MacdWeight = clampFloat(d.MacdWeight, 0, 1, def.Divergence.MacdWeight)
	d.SlopeWeight = clampFloat(d.SlopeWeight, 0, 1, def.Divergence.SlopeWeight)
	d.VolumeWeight = clampFloat(d.VolumeWeight, 0, 1, def.Divergence.VolumeWeight)
	if !c.VolumeAux {
		d.VolumeWeight = 0
	}
	// 权重全为 0 会让力度恒为 0，退回默认
	total := d.StructureWeight + d.MacdWeight + d.SlopeWeight + d.VolumeWeight
	if total <= 0 {
		d = def.Divergence
		if !c.VolumeAux {
			d.VolumeWeight = 0
		}
	}
	d.Threshold = clampFloat(d.Threshold, 0.1, 0.99, def.Divergence.Threshold)
	d.MinLegBars = clampIntRange(d.MinLegBars, 1, 60, def.Divergence.MinLegBars)
	c.Divergence = d

	c.ScoreThreshold = clampFloat(c.ScoreThreshold, 0, 100, def.ScoreThreshold)
	c.ExpiryBars = clampIntRange(c.ExpiryBars, 3, 500, def.ExpiryBars)
	c.InvalidTolerance = clampFloat(c.InvalidTolerance, 0, 0.5, def.InvalidTolerance)
	c.MaxSignalsPerType = clampIntRange(c.MaxSignalsPerType, 1, 50, def.MaxSignalsPerType)
	c.LookbackDays = clampIntRange(c.LookbackDays, 0, 20000, def.LookbackDays)
	c.ReplayStep = clampIntRange(c.ReplayStep, 1, 100, def.ReplayStep)
	c.ReplayMaxSteps = clampIntRange(c.ReplayMaxSteps, 10, 5000, def.ReplayMaxSteps)
	c.MinBars = clampIntRange(c.MinBars, 10, 5000, def.MinBars)

	return c
}

func sanitizeLevelWeights(items []LevelWeight, def []LevelWeight) []LevelWeight {
	seen := make(map[Level]float64, len(items))
	for _, item := range items {
		if !ValidLevel(item.Level) {
			continue
		}
		if item.Weight < 0 || item.Weight > 2 || math.IsNaN(item.Weight) {
			continue
		}
		seen[item.Level] = item.Weight
	}
	out := make([]LevelWeight, 0, len(def))
	for _, item := range def {
		weight, ok := seen[item.Level]
		if !ok {
			weight = item.Weight
		}
		out = append(out, LevelWeight{Level: item.Level, Weight: weight})
	}
	return out
}

func sanitizeTypeWeights(items []TypeWeight, def []TypeWeight) []TypeWeight {
	valid := map[SignalType]bool{
		SignalBuy1: true, SignalBuy2: true, SignalBuy3: true,
		SignalSell1: true, SignalSell2: true, SignalSell3: true,
	}
	seen := make(map[SignalType]float64, len(items))
	for _, item := range items {
		if !valid[item.Type] {
			continue
		}
		if item.Weight < 0 || item.Weight > 2 || math.IsNaN(item.Weight) {
			continue
		}
		seen[item.Type] = item.Weight
	}
	out := make([]TypeWeight, 0, len(def))
	for _, item := range def {
		weight, ok := seen[item.Type]
		if !ok {
			weight = item.Weight
		}
		out = append(out, TypeWeight{Type: item.Type, Weight: weight})
	}
	return out
}

// LevelWeight 返回某级别的权重（缺失时用默认值）。
func (c Config) LevelWeight(level Level) float64 {
	def := DefaultConfig()
	for _, item := range c.LevelWeights {
		if item.Level == level {
			return item.Weight
		}
	}
	for _, item := range def.LevelWeights {
		if item.Level == level {
			return item.Weight
		}
	}
	return 0.5
}

// TypeWeight 返回某买卖点类型的权重（缺失时用默认值）。
func (c Config) TypeWeight(t SignalType) float64 {
	def := DefaultConfig()
	for _, item := range c.TypeWeights {
		if item.Type == t {
			return item.Weight
		}
	}
	for _, item := range def.TypeWeights {
		if item.Type == t {
			return item.Weight
		}
	}
	return 0.8
}

func clampFloat(value, min, max, fallback float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return fallback
	}
	if value == 0 {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func clampIntRange(value, min, max, fallback int) int {
	if value == 0 {
		return fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
