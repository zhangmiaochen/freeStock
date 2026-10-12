package score

import (
	"sort"
	"strings"
)

// 对应 `portfolio_service._attach_signal_candidates`（:1490-1608）与
// `_load_signal_candidate_frame`（:1428-1488）。
//
// 这是"信号表 → 评分物化行"的唯一通道：把每只票**回看 29 个交易日内最近的
// 一条 buy/candidate_buy 信号**贴到该票当天的行上。原实现用
// `pd.merge_asof(direction="backward", by="ts_code", tolerance=29)`，
// 这里用"按票分组 + 二分会话"实现同一语义。

// SignalCandidateLookbackTradeDays 对应 `SIGNAL_CANDIDATE_LOOKBACK_TRADE_DAYS`。
//
// ⚠ merge_asof 的 tolerance 是 `max(0, 该值 - 1)` = **29**，不是 30。
const SignalCandidateLookbackTradeDays = 30

// SignalCandidateRow 是 attach 的输入信号（对应 `_load_signal_candidate_frame`
// 选出的 10 列 + 去重前条数）。
type SignalCandidateRow struct {
	TradeDate        int64
	Code             string
	ModelID          string
	ModelName        string
	MarketState      string
	MarketStateLabel string
	SignalType       string
	SignalLevel      string
	SignalLevelLabel string
	Score            float64
	// SignalCount 是**去重前**同 `(trade_date, ts_code)` 的条数。
	SignalCount int
}

// SignalAttachOptions 对应 `_attach_signal_candidates` 的参数。
type SignalAttachOptions struct {
	// StartDate / EndDate 为空时取 `rows` 里的最小/最大交易日
	//（原实现 `start_date or work.trade_date.min()`）。
	StartDate int64
	EndDate   int64
	// LookbackTradeDays 默认 SignalCandidateLookbackTradeDays。
	LookbackTradeDays int
}

// SignalAttachDiagnostics 记录 attach 过程中的可观测事实。
//
// 原实现会**静默丢掉**交易日不在交易日历里的行（`work[work["trade_idx"].notna()]`），
// 这是个很容易被忽略的副作用，所以把丢弃条数暴露出来。
type SignalAttachDiagnostics struct {
	InputRows        int
	OutputRows       int
	DroppedRows      int
	MatchedRows      int
	CandidateCount   int
	CalendarSize     int
	LookbackTradeDay int
}

// AttachSignalCandidates 把候选信号按 merge_asof 语义贴到评分行上。
//
// `rowDates` 与 `rows` 一一对应（评分行本身不带交易日 —— 交易日属于"某一天的一次
// 评分"，由调用方从物化行带来）。
//
// 返回的切片**可能比入参短**：交易日不在 `calendar` 里的行会被丢弃
// （与原实现一致，见 SignalAttachDiagnostics.DroppedRows）。
func AttachSignalCandidates(
	rows []Row, rowDates []int64, candidates []SignalCandidateRow, calendar []int64,
	opts SignalAttachOptions,
) ([]Row, SignalAttachDiagnostics) {
	lookback := opts.LookbackTradeDays
	if lookback <= 0 {
		lookback = SignalCandidateLookbackTradeDays
	}
	tolerance := int64(lookback - 1)
	if tolerance < 0 {
		tolerance = 0
	}
	diagnostics := SignalAttachDiagnostics{
		InputRows: len(rows), CandidateCount: len(candidates),
		CalendarSize: len(calendar), LookbackTradeDay: lookback,
	}
	if len(rows) == 0 {
		return nil, diagnostics
	}
	if len(rowDates) != len(rows) {
		// 这类错配必须立刻暴露：静默按较短的一边来会悄悄丢行。
		panic("AttachSignalCandidates: rowDates 与 rows 长度不一致")
	}

	tradeIndex := make(map[int64]int64, len(calendar))
	for idx, date := range calendar {
		if _, exists := tradeIndex[date]; !exists {
			tradeIndex[date] = int64(idx)
		}
	}

	// 右侧（信号）按票分组，组内按交易日升序；组内再按
	// (signal_type 升序, score 降序, model_id 升序) —— 同一天同票已去重，
	// 这个次序只是为了让"万一有重复"时的选择与原实现一致。
	byCode := map[string][]signalPoint{}
	for _, item := range candidates {
		idx, ok := tradeIndex[item.TradeDate]
		if !ok {
			continue
		}
		byCode[item.Code] = append(byCode[item.Code], signalPoint{idx: idx, item: item})
	}
	for code := range byCode {
		points := byCode[code]
		sort.SliceStable(points, func(i, j int) bool {
			if points[i].idx != points[j].idx {
				return points[i].idx < points[j].idx
			}
			if points[i].item.SignalType != points[j].item.SignalType {
				return points[i].item.SignalType < points[j].item.SignalType
			}
			if points[i].item.Score != points[j].item.Score {
				return points[i].item.Score > points[j].item.Score
			}
			return points[i].item.ModelID < points[j].item.ModelID
		})
		byCode[code] = points
	}

	out := make([]Row, 0, len(rows))
	for i, row := range rows {
		idx, ok := tradeIndex[rowDates[i]]
		if !ok {
			diagnostics.DroppedRows++
			continue
		}
		row.SignalType = ""
		row.SignalScore = 0.0
		row.SignalModelID = ""
		row.SignalModelName = ""
		row.SignalLevel = ""
		row.SignalLevelLabel = ""
		row.SignalTradeDate = 0
		row.SignalAge = 999
		row.SignalCount = 0

		if point, matched := matchSignalPoint(byCode[row.Code], idx, tolerance); matched {
			// ⚠ **必须小写化**：原实现在 `prepare_strength_frame` 里对
			// `active_signal_type` 做了 `.str.lower()`（portfolio_rotation_logic.py:387），
			// 而下游处处按字面量比较（`signal_type == "buy"`、`hasFreshSignal`、
			// `overflow_candidate_quality_ok` 等）。Go 以前原样透传，
			// 于是库里若出现 `"BUY"`，原版仍算有信号、Go 会当没信号
			// —— 熊市闸门/加层/建仓层数全部静默走另一条分支。
			// 由 oracle 逐例比对抓出（黄金值里有 `signal_type="BUY"` 一例）。
			row.SignalType = strings.ToLower(point.item.SignalType)
			row.SignalScore = point.item.Score
			row.SignalModelID = point.item.ModelID
			row.SignalModelName = point.item.ModelName
			row.SignalLevel = point.item.SignalLevel
			row.SignalLevelLabel = point.item.SignalLevelLabel
			row.SignalTradeDate = point.item.TradeDate
			row.SignalAge = int(idx - point.idx)
			row.SignalCount = point.item.SignalCount
			diagnostics.MatchedRows++
		}
		out = append(out, row)
	}
	diagnostics.OutputRows = len(out)
	return out, diagnostics
}

type signalPoint struct {
	idx  int64
	item SignalCandidateRow
}

// matchSignalPoint 找 `idx` 之前（含）最近的一个信号点，超出 tolerance 视为未命中。
//
// 对应 merge_asof(direction="backward", tolerance=tolerance)：
// `0 <= idx - signal_idx <= tolerance`。
func matchSignalPoint(points []signalPoint, idx, tolerance int64) (signalPoint, bool) {
	if len(points) == 0 {
		return signalPoint{}, false
	}
	// 第一个 signal_idx > idx 的位置；它左边就是"最近的、不晚于 idx"的那个。
	position := sort.Search(len(points), func(i int) bool { return points[i].idx > idx })
	if position == 0 {
		return signalPoint{}, false
	}
	candidate := points[position-1]
	if idx-candidate.idx > tolerance {
		return signalPoint{}, false
	}
	return candidate, true
}

// SignalCodeAllowed 判断 A 股代码是否在信号表的取用范围内。
//
// 对应原实现的正则
// `^(?:600|601|603|605|688|689|000|001|002|003|300|301)\d{3}\.(?:SH|SZ)$`。
// 指数、北交所、老八股等都会被这条规则挡在信号之外。
func SignalCodeAllowed(code string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(code))
	if len(normalized) != 9 || normalized[6] != '.' {
		return false
	}
	suffix := normalized[7:]
	if suffix != "SH" && suffix != "SZ" {
		return false
	}
	for i := 3; i < 6; i++ {
		if normalized[i] < '0' || normalized[i] > '9' {
			return false
		}
	}
	switch normalized[:3] {
	case "600", "601", "603", "605", "688", "689", "000", "001", "002", "003", "300", "301":
		return true
	}
	return false
}
