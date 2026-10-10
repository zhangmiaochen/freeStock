package volprice

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
)

// 对应 `research_service._collect_signal_rows`（:6620-6669）与
// `_build_signal_history_row`（:6686-6719）。
//
// 这是"回测产出的 daily_signals" → "signal_history 表行"的最后一段转换，
// 也是**信号是不是真的进了表**的分界点。上游 `RunVolumePriceBacktest` 已经
// 逐字段对齐过原版，这里再对齐一次筛选/去重规则与字段映射。

// SignalHistoryRow 对应 `signal_history` 的一行（21 列）。
//
// 时间字段用字符串，因为原项目是 `TIMESTAMP`，Go 侧存 RFC3339 文本。
type SignalHistoryRow struct {
	TsCode              string
	Name                string
	TradeDate           int64
	ModelID             string
	ModelName           string
	MarketState         string
	MarketStateLabel    string
	SignalType          string
	SignalLevel         string
	SignalLevelLabel    string
	SignalSubtype       string
	SignalSubtypeLabel  string
	EntryPrice          *float64
	StopPrice           *float64
	TargetPrice         *float64
	Score               float64
	RiskLevel           string
	BehaviorQualityJSON string
	ReasonJSON          string
	CreatedAt           string
	UpdatedAt           string
}

// CollectSignalOptions 对应 `_collect_signal_rows` 的关键字参数。
type CollectSignalOptions struct {
	TsCode string
	Name   string
	// OnlyModelIDs 为空时对应原实现的 `MODEL_IDS`（即全部模型）。
	OnlyModelIDs []string
	StartDate    string
	EndDate      string
	// MinTradeDateExclusive 对应 `min_trade_date_exclusive`：
	// 严格大于该日期的信号才会被收下（增量刷新用）。
	MinTradeDateExclusive string
	// MarketStateCache 对应原实现的 `market_cache`（按交易日给出市场状态）。
	MarketStateCache map[string]MarketStateInfo
	// Now 对应 `_now_ts()`；由调用方传入以保证同一次刷新时间戳一致。
	Now string
}

// CollectSignalRows 对应 `_collect_signal_rows`。
//
// 关键规则（照抄原实现）：
//   - 只收 `signal_type in {"buy","candidate_buy"}`；
//   - `trade_date` 必须是合法的 8 位日期，且严格大于 `MinTradeDateExclusive`；
//   - 去重键是 `(ts_code, trade_date, model_id, signal_type)` —— **含 model_id，
//     所以不同模型在同一天同一票上的信号会各留一条**，不是每票每天只留一条；
//   - 每个模型单独跑一次回测，`required_market_state` 取该模型定义里的市场状态。
func CollectSignalRows(bars []Bar, opts CollectSignalOptions) []SignalHistoryRow {
	models := opts.OnlyModelIDs
	if len(models) == 0 {
		models = make([]string, 0, len(ModelDefinitions))
		for _, def := range ModelDefinitions {
			models = append(models, def.ID)
		}
	}
	seen := map[string]bool{}
	rows := make([]SignalHistoryRow, 0, 16)
	for _, rawModel := range models {
		modelID := NormalizeModelID(rawModel)
		result := RunVolumePriceBacktest(bars, modelID, BacktestOptions{
			StartDate:           opts.StartDate,
			EndDate:             opts.EndDate,
			MarketStateCache:    opts.MarketStateCache,
			RequiredMarketState: ModelRequiredStates[modelID],
		})
		if !result.OK {
			continue
		}
		for _, signal := range result.DailySignals {
			signalType := strings.TrimSpace(signal.SignalType)
			if signalType != "buy" && signalType != "candidate_buy" {
				continue
			}
			tradeDate, ok := ParseTradeDate(signal.SignalDate)
			if !ok {
				continue
			}
			if opts.MinTradeDateExclusive != "" {
				if bound, boundOK := ParseTradeDate(opts.MinTradeDateExclusive); boundOK && tradeDate <= bound {
					continue
				}
			}
			effectiveModel := NormalizeModelID(signal.ModelID)
			if strings.TrimSpace(signal.ModelID) == "" {
				effectiveModel = modelID
			}
			key := signalKeyFor(effectiveModel, opts.TsCode, tradeDate, signalType)
			if seen[key] {
				continue
			}
			seen[key] = true
			row, ok := BuildSignalHistoryRow(signal, opts.TsCode, opts.Name, opts.Now)
			if !ok {
				continue
			}
			rows = append(rows, row)
		}
	}
	return rows
}

func signalKeyFor(modelID, code string, tradeDate int64, signalType string) string {
	var b strings.Builder
	b.WriteString(modelID)
	b.WriteByte('\x00')
	b.WriteString(code)
	b.WriteByte('\x00')
	b.WriteString(strconv.FormatInt(tradeDate, 10))
	b.WriteByte('\x00')
	b.WriteString(signalType)
	return b.String()
}

// BuildSignalHistoryRow 对应 `_build_signal_history_row`。
//
// 返回 false 表示该信号没有合法交易日，原实现里它会被写进一行 `trade_date=""`
// 的脏数据（`_normalize_date` 返回空串），所以这里直接拒绝。
func BuildSignalHistoryRow(signal Signal, tsCode, name, now string) (SignalHistoryRow, bool) {
	tradeDate, ok := ParseTradeDate(signal.SignalDate)
	if !ok {
		return SignalHistoryRow{}, false
	}
	modelID := NormalizeModelID(signal.ModelID)
	modelName := signal.ModelName
	if strings.TrimSpace(modelName) == "" {
		modelName = ModelDefOf(modelID).Name
	}
	return SignalHistoryRow{
		TsCode: tsCode, Name: name, TradeDate: tradeDate,
		ModelID: modelID, ModelName: modelName,
		MarketState:      NormalizeMarketState(signal.MarketState, "range"),
		MarketStateLabel: signal.MarketStateLabel,
		SignalType:       signal.SignalType,
		SignalLevel:      signal.SignalLevel,
		SignalLevelLabel: signal.SignalLevelLabel,
		SignalSubtype:    signal.SignalSubtype, SignalSubtypeLabel: signal.SignalSubtypeLabel,
		EntryPrice:          roundPtrOrNil(signal.EntryPrice),
		StopPrice:           roundPtrOrNil(signal.StopPrice),
		TargetPrice:         roundPtrOrNil(signal.TargetPrice),
		Score:               roundOrNil(signal.Score, 2),
		RiskLevel:           signal.RiskLevel,
		BehaviorQualityJSON: encodeBehaviorQualityJSON(signal.BehaviorQuality),
		ReasonJSON:          encodeReasonJSON(signal),
		CreatedAt:           now, UpdatedAt: now,
	}, true
}

// ParseTradeDate 对应 `_normalize_date`：去掉横线，必须是 8 位数字。
func ParseTradeDate(value string) (int64, bool) {
	text := strings.TrimSpace(strings.ReplaceAll(value, "-", ""))
	if len(text) != 8 {
		return 0, false
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// roundPtrOrNil 对应 `_round_or_none(value, 4)`。
func roundPtrOrNil(value *float64) *float64 {
	if value == nil || math.IsNaN(*value) {
		return nil
	}
	rounded := round4(*value)
	return &rounded
}

// roundOrNil 对应 `_round_or_none(value, digits)`：None 或 NaN 都返回 0（列是 REAL NOT NULL DEFAULT 0）。
func roundOrNil(value float64, digits int) float64 {
	if math.IsNaN(value) {
		return 0
	}
	factor := math.Pow10(digits)
	return math.Round(value*factor) / factor
}

// encodeReasonJSON 对应
// `_json_text({...explanation, condition_*, confirmation_*, quality_block_reasons})`。
//
// ⚠ 用 Python `json.dumps` 的默认分隔符（`, ` 与 `: `），保证与原版**逐字节**一致 ——
// 这张表是要和 Python 侧对照的，用 Go 的紧凑格式会让文本比对全部失败。
func encodeReasonJSON(signal Signal) string {
	var b strings.Builder
	b.WriteString("{")
	writeJSONField(&b, "explanation", signal.Explanation, true)
	writeJSONArray(&b, "condition_hits", signal.ConditionHits)
	writeJSONArray(&b, "condition_misses", signal.ConditionMisses)
	writeJSONArray(&b, "confirmation_hits", signal.ConfirmationHits)
	writeJSONArray(&b, "confirmation_misses", signal.ConfirmationMiss)
	writeJSONArray(&b, "quality_block_reasons", signal.QualityBlockReasons)
	b.WriteString("}")
	return b.String()
}

// encodeBehaviorQualityJSON 对应 `_json_text(behavior_quality)`。
//
// 键顺序：先原 `_behavior_quality` 的 8 个键（顺序照抄），再补箱体/超跌合并进来的
// 额外键（按名字升序排列）。
//
// ⚠ 已知的**外观差异**：原实现里额外键的顺序来自字典 update 的插入序，
// Go 的 map 没有序，所以这里固定按名字升序。**键集合与取值完全一致**
// （`quality_parity_test.go` 已逐键比对），只是文本里键的先后可能不同。
// 消费者是解析 JSON，不受影响。
func encodeBehaviorQualityJSON(quality BehaviorQuality) string {
	var b strings.Builder
	b.WriteString("{")
	writeJSONField(&b, "volume_behavior", quality.VolumeBehavior, true)
	writeJSONField(&b, "volume_behavior_label", quality.VolumeBehaviorLabel, false)
	writeJSONField(&b, "acceptance_quality", quality.AcceptanceQuality, false)
	writeJSONField(&b, "acceptance_quality_label", quality.AcceptanceQualityLabel, false)
	writeJSONField(&b, "upper_shadow_behavior", quality.UpperShadowBehavior, false)
	writeJSONField(&b, "upper_shadow_behavior_label", quality.UpperShadowBehaviorLbl, false)
	writeJSONFloat(&b, "score", quality.Score)
	writeJSONBool(&b, "death_volume_risk", quality.DeathVolumeRisk)
	keys := make([]string, 0, len(quality.Extras))
	for key := range quality.Extras {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeJSONFloat(&b, key, quality.Extras[key])
	}
	b.WriteString("}")
	return b.String()
}

func writeJSONField(b *strings.Builder, key, value string, first bool) {
	if !first {
		b.WriteString(", ")
	}
	b.WriteString(quoteJSON(key))
	b.WriteString(": ")
	b.WriteString(quoteJSON(value))
}

func writeJSONArray(b *strings.Builder, key string, values []string) {
	b.WriteString(", ")
	b.WriteString(quoteJSON(key))
	b.WriteString(": [")
	for i, value := range values {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(quoteJSON(value))
	}
	b.WriteString("]")
}

func writeJSONFloat(b *strings.Builder, key string, value float64) {
	b.WriteString(", ")
	b.WriteString(quoteJSON(key))
	b.WriteString(": ")
	b.WriteString(formatJSONNumber(value))
}

func writeJSONBool(b *strings.Builder, key string, value bool) {
	b.WriteString(", ")
	b.WriteString(quoteJSON(key))
	b.WriteString(": ")
	if value {
		b.WriteString("true")
	} else {
		b.WriteString("false")
	}
}

// quoteJSON 用 Go 的编码器做转义，但不加 HTML 转义（对应 `ensure_ascii=False`）。
func quoteJSON(value string) string {
	var b strings.Builder
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return `""`
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatJSONNumber 复刻 Python `json.dumps(float)` 的输出（整数值输出 `x.0`）。
func formatJSONNumber(value float64) string {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "null"
	}
	if value == math.Trunc(value) && math.Abs(value) < 1e15 {
		return strconv.FormatFloat(value, 'f', 1, 64)
	}
	return strconv.FormatFloat(value, 'g', -1, 64)
}
