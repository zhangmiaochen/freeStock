package portfolio

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// 一键应用调仓建议。
//
// 1:1 移植自 portfolio_service.apply_rotation（:855-980）。
//
// **如实照搬的几处坑**（不要"顺手改好"）：
//
//  1. `target_layers` 被**硬编码 clamp 到 3**。这与 `add_position` 的 [1,2]、
//     低吸策略的 `total_layers(=10)` 三者互不一致 —— 参考实现就是这样。
//  2. 应用是"**整表 DELETE 再 INSERT**"，不是逐条 UPDATE。行会被重写，
//     但 `created_at` 显式沿用旧值。
//  3. 加层的成本用**加权平均**，且分母是 `target_layers`（不是
//     `current_layers + layer_delta`）—— 当 target 被 clamp 过时两者不等，
//     原实现如此。
//  4. 减层**不改成本价**，也不改建仓日期。
//  5. 手动组合直接拒绝（"不支持一键应用建议"）。
const maxApplyLayers = 3

// ApplyAction 是一条要应用的动作。
type ApplyAction struct {
	Code         string
	Name         string
	Action       string
	TargetLayers int
	// Price 是最新收盘价（用于新建仓与加层摊薄）
	Price  float64
	Reason string
}

// ApplyResult 是一次应用的结果。
type ApplyResult struct {
	PositionCount int `json:"positionCount"`
	Added         int `json:"added"`
	Reduced       int `json:"reduced"`
	Cleared       int `json:"cleared"`
	Unchanged     int `json:"unchanged"`
	// Skipped 列出被跳过的动作及原因
	Skipped []string `json:"skipped"`
}

// IsApplyExecutable 判断动作能否被"一键应用"执行。
func IsApplyExecutable(action string) bool {
	switch action {
	case "clear", "replace_out", "reduce", "add", "add_layer", "replace_in":
		return true
	}
	return false
}

// wouldChangeLayers 判断这批动作里**是否存在真实的层数变化**。
//
// 对应原实现的 `changed_count`（只累加 `layer_delta != 0` 的行）。
// 判据必须是"clamp 之后目标层数 ≠ 当前层数"，而不是"动作名可执行"。
func wouldChangeLayers(actions []ApplyAction, existing map[string]Position, totalLayers int) bool {
	limit := maxApplyLayers
	if totalLayers > 10 {
		limit = totalLayers
	}
	for _, action := range actions {
		code := BareCode(action.Code)
		if code == "" {
			continue
		}
		previous, existed := existing[code]
		if !IsApplyExecutable(action.Action) && !existed {
			continue
		}
		target := action.TargetLayers
		if target > limit {
			target = limit
		}
		if target < 0 {
			target = 0
		}
		currentLayers := 0
		if existed {
			currentLayers = previous.PositionLayers
		}
		if target != currentLayers {
			return true
		}
	}
	return false
}

// applyTxnReason 是"一键应用调仓建议"写流水时**固定**的 reason。
//
// 原实现 portfolio_service.py:922 是硬编码的 `reason="一键应用调仓建议"`，
// 不写计划行的原因。此前我写的是 `action.Reason`（计划行原因），与原版不同。
const applyTxnReason = "一键应用调仓建议"

// applySourceSignalID 是应用调仓时流水的来源标识。
//
// 原实现 portfolio_service.py:923：`f"{pid}:{latest_trade_date}:{code}"`。
// 此前写的是常量 "apply_rotation" —— 逐笔无法追溯到"哪次应用的哪只"。
func applySourceSignalID(portfolioID, tradeDate, code string) string {
	return portfolioID + ":" + tradeDate + ":" + code
}

// appendMissingHoldings 把"库里有、但 actions 里没提到"的持仓补成保留项。
//
// 对应原实现 `final_codes = 持仓 ∪ 建议行`（portfolio_service.py:888）：
// 建议行缺失时 `action=""`、`target_layers = current_layers`，重写后是等价的无操作。
//
// 没有这一步的话，整表 DELETE + 按 actions INSERT 会把漏传的持仓**静默删掉**，
// 而且不留任何流水 —— 这是本模块唯一有数据丢失风险的路径。
func appendMissingHoldings(actions []ApplyAction, existing map[string]Position) []ApplyAction {
	if len(existing) == 0 {
		return actions
	}
	mentioned := make(map[string]bool, len(actions))
	for _, action := range actions {
		if code := BareCode(action.Code); code != "" {
			mentioned[code] = true
		}
	}
	missing := make([]string, 0)
	for code := range existing {
		if !mentioned[code] {
			missing = append(missing, code)
		}
	}
	// 排序保证结果稳定（原实现用 sorted(...)）
	sort.Strings(missing)
	for _, code := range missing {
		position := existing[code]
		actions = append(actions, ApplyAction{
			Code: code, Name: position.Name,
			// 空 action = 无操作；层数保持当前值 → 重写后完全等价
			Action:       "",
			TargetLayers: position.PositionLayers,
			Price:        position.EntryPrice,
			Reason:       "建议缺失，保留原持仓",
		})
	}
	return actions
}

// ApplyRotation 把动作应用到持仓。
//
// tradeDate 用于新建仓的建仓日期（YYYYMMDD）。
func (s *Store) ApplyRotation(portfolioID string, tradeDate string, actions []ApplyAction) (*ApplyResult, error) {
	item, err := s.Get(portfolioID)
	if err != nil {
		return nil, err
	}
	// 手动组合不支持一键应用（参考实现同样直接抛错）
	if !item.AllowApplyRotation {
		return nil, fmt.Errorf(
			"「%s」是%s，仅生成分析提醒，不支持一键应用建议",
			item.Name, item.TypeLabel)
	}

	existing, err := s.ListPositions(portfolioID)
	if err != nil {
		return nil, err
	}
	existingByCode := map[string]Position{}
	for _, position := range existing {
		existingByCode[position.Code] = position
	}

	result := &ApplyResult{}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("开启应用事务失败：%w", err)
	}
	defer tx.Rollback()

	// 整表 DELETE，再按计算结果 INSERT（参考实现如此）
	if _, err := tx.Exec(`DELETE FROM portfolio_position WHERE portfolio_id=?`, portfolioID); err != nil {
		return nil, fmt.Errorf("清理旧持仓失败：%w", err)
	}

	// ---- U9 安全兜底：**持仓 ∪ 建议行** ----
	//
	// 原实现 portfolio_service.py:888：
	//
	//	final_codes = sorted(set(pos_map.keys()) | set(row_map.keys()))
	//	# 建议里没有的代码 → action=""、target_layers = current_layers → 原样重写
	//
	// 也就是说**建议行丢了也不会丢持仓**。这里必须补上这一步：
	// 整表已经 DELETE 过，只要调用方漏传一只（将来在 service 层加一行过滤就会发生），
	// 那只持仓就会被**静默删除且不留流水**。
	//
	// 这个不变量不能外包给调用方的契约 —— 放在这里成本几乎为零。
	actions = appendMissingHoldings(actions, existingByCode)

	// ---- 与原实现一致：**没有任何实际层数变化时拒绝应用** ----
	//
	// 原实现把 `changed_count` 累加在 `layer_delta != 0` 上，最后
	// `if changed_count <= 0: raise PortfolioDataUnavailableError("当前组合暂无可执行的层级调仓建议")`，
	// 而且这个判断发生在**删除持仓之前**（portfolio_service.py:964-967）。
	//
	// 我此前是在 service 层按**动作名**判（`IsApplyExecutable`），
	// 于是"计划说要加层、但目标层数已经等于当前层数"（例如已在上限 3 层）
	// 会被当成可执行 → 整表重写却没改任何东西 → 与原版的"直接拒绝"行为不同。
	if !wouldChangeLayers(actions, existingByCode, item.TotalLayers) {
		return nil, fmt.Errorf("当前组合暂无可执行的层级调仓建议")
	}

	now := nowText()
	for _, action := range actions {
		// ⚠ 这里**不跳过**不可执行的动作（keep / observe）。
		//
		// 因为整表已经 DELETE 过了：跳过它们 = 把没变化的持仓静默删掉。
		// 参考实现是把 rotation_signal.action 原样透传的，keep / observe
		// 的 target_layers 等于当前层数，重写后是等价的无操作。
		// IsApplyExecutable 只用来判断"这算不算一次真实变更"（计数与流水语义）。
		code := BareCode(action.Code)
		if code == "" {
			result.Skipped = append(result.Skipped, "空代码的动作被跳过")
			continue
		}
		realChange := IsApplyExecutable(action.Action)

		previous, existed := existingByCode[code]

		// 不可执行的动作，只有当它对应**已存在的持仓**时才有保留意义
		// （keep / observe 的层数等于当前层数，重写后是无操作）。
		// 对不存在的持仓，observe 之类没有意义，不能凭它建仓。
		if !realChange && !existed {
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s 的 %s 不是可执行动作且原本无持仓，已跳过", code, action.Action))
			continue
		}

		// 层数上限：10 层制（含）以下沿用参考实现的硬编码 3；
		// 超过 10 层的组合（例如质量成长的 100 层制）按总层数放行，
		// 否则 15% 的目标会被砍成 3%。
		limit := maxApplyLayers
		if item.TotalLayers > 10 {
			limit = item.TotalLayers
		}
		target := action.TargetLayers
		if target > limit {
			target = limit
		}
		if target < 0 {
			target = 0
		}

		currentLayers := 0
		if existed {
			currentLayers = previous.PositionLayers
		}

		// 目标 0 层 = 清仓：整表重建时不需要插入，只记流水
		if target == 0 {
			if existed {
				// 只有可执行的动作才算"真实清仓"；keep/observe 带着 0 层
				// 传入是调用方的异常，按无操作处理不计数
				if realChange {
					result.Cleared++
				} else {
					result.Unchanged++
				}
				if err := insertTxnTx(tx, Transaction{
					PortfolioID: portfolioID, Code: code,
					Name:      firstNonEmpty(action.Name, previous.Name, code),
					TradeDate: tradeDate, Action: txnAction(action.Action, "clear"),
					Price:       round(action.Price, 4),
					LayersDelta: -currentLayers,
					// ⚠ 由层数**现算**，不减库里的历史 `position_weight`
					//（原实现 :909-910/:922 `_weight_from_layers(current_layers, total)`）——
					// 两者不一致时（手动传过 weight、或 total_layers 被改过）会算错。
					WeightDelta: round(-weightFromLayers(currentLayers, item.TotalLayers), 4),
					// U10：流水字段按原实现写死 —— reason 固定、source_signal_id 是 `组合:日期:代码`
					Reason: applyTxnReason, SourceSignalID: applySourceSignalID(portfolioID, tradeDate, code),
				}); err != nil {
					return nil, err
				}
			}
			continue
		}

		// 成本与建仓日期
		entryPrice := action.Price
		entryDate := tradeDate
		name := firstNonEmpty(action.Name, previous.Name, code)

		switch {
		case currentLayers <= 0:
			// 新开仓
			result.Added++

		case target > currentLayers:
			// 加层：加权平均，**分母是 target**（原实现如此 ——
			// target 被 clamp 过时与 current+delta 不等）
			layerDelta := target - currentLayers
			if existed && previous.EntryPrice > 0 && action.Price > 0 {
				entryPrice = (previous.EntryPrice*float64(currentLayers) +
					action.Price*float64(layerDelta)) / float64(target)
			}
			// 建仓日期保持最早的那个
			entryDate = firstNonEmpty(previous.EntryDate, tradeDate)
			if realChange {
				result.Added++
			} else {
				result.Unchanged++
			}

		case target < currentLayers:
			// 减层：成本不变、日期不变
			if existed {
				entryPrice = previous.EntryPrice
				entryDate = firstNonEmpty(previous.EntryDate, tradeDate)
			}
			if realChange {
				result.Reduced++
			} else {
				result.Unchanged++
			}

		default:
			// 层数不变：沿用旧成本与日期
			if existed {
				entryPrice = previous.EntryPrice
				entryDate = firstNonEmpty(previous.EntryDate, tradeDate)
			}
			result.Unchanged++
		}

		weight := weightFromLayers(target, item.TotalLayers)
		createdAt := now
		if existed && previous.CreatedAt != "" {
			createdAt = previous.CreatedAt // 显式沿用，行被重写但创建时间不变
		}

		// last_action = 本次动作（原实现 :958 `"last_action": action or "keep"`）。
		// 新建仓时 action 就是 add / replace_in，**不是 "apply_rotation"**。
		lastAction := action.Action
		if lastAction == "" {
			lastAction = "keep"
		}

		// source_type（原实现 :955
		// `current.source_type or ("system" if current_layers <= 0 else "manual")`）：
		// 新建仓 → "system"；已有持仓 → 沿用原值，没有则 "manual"。
		sourceType := previous.SourceType
		if sourceType == "" {
			if currentLayers <= 0 {
				sourceType = SourceSystem
			} else {
				sourceType = SourceManual
			}
		}

		if _, err := tx.Exec(`
			INSERT INTO portfolio_position (
				portfolio_id, ts_code, name, entry_price, entry_date, position_layers, position_weight,
				source_type, holding_type, strategy_tag, last_action, created_at, updated_at
			) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			portfolioID, code, name, round(entryPrice, 4), entryDate,
			target, round(weight, 4),
			sourceType,
			previous.HoldingType, previous.StrategyTag,
			lastAction, createdAt, now,
		); err != nil {
			return nil, fmt.Errorf("写入持仓失败（%s）：%w", code, err)
		}

		// **只有层数真的变了才写流水**（原实现 :912-925 的 `if layer_delta != 0:`）。
		// 否则 keep / observe 会堆积大量 0 变化的空流水。
		layerDelta := target - currentLayers
		if layerDelta != 0 {
			if err := insertTxnTx(tx, Transaction{
				PortfolioID: portfolioID, Code: code, Name: name,
				TradeDate: tradeDate, Action: txnAction(action.Action, "add"),
				// **price 用最新收盘价**，不是加权后的成本价（原实现 :918）
				Price:       round(action.Price, 4),
				LayersDelta: layerDelta,
				// 同样由**层数现算**（原实现 :909-910/:922），不减库里的历史值
				WeightDelta: round(weightFromLayers(target, item.TotalLayers)-
					weightFromLayers(currentLayers, item.TotalLayers), 4),
				Reason: applyTxnReason, SourceSignalID: applySourceSignalID(portfolioID, tradeDate, code),
			}); err != nil {
				return nil, err
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("提交应用失败：%w", err)
	}

	count, err := s.countPositions(portfolioID)
	if err != nil {
		return nil, err
	}
	result.PositionCount = count
	return result, nil
}

// txnAction 归一化流水里的动作名。
//
// 参考实现的 apply_rotation 会把 rotation_signal.action **原样透传**
// （包括 keep/observe/reduce/add_layer/replace_in/replace_out），
// 只有拿不到动作时才退化成 add/clear。
func txnAction(action, fallback string) string {
	if action == "" {
		return fallback
	}
	return action
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// insertTxnTx 在事务里写一条流水。
func insertTxnTx(tx *sql.Tx, txn Transaction) error {
	txn.ID = newTxnID()
	txn.CreatedAt = nowText()
	_, err := tx.Exec(`
		INSERT INTO portfolio_position_txn (
			txn_id, portfolio_id, ts_code, name, trade_date, action, price,
			layers_delta, weight_delta, reason, source_signal_id, created_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		txn.ID, txn.PortfolioID, txn.Code, txn.Name, txn.TradeDate, txn.Action, txn.Price,
		txn.LayersDelta, txn.WeightDelta, txn.Reason, txn.SourceSignalID, txn.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("写持仓流水失败：%w", err)
	}
	return nil
}

func (s *Store) countPositions(portfolioID string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM portfolio_position WHERE portfolio_id=?`, portfolioID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("统计持仓失败：%w", err)
	}
	return count, nil
}

// ErrManualPortfolio 表示手动组合不支持一键应用。
var ErrManualPortfolio = errors.New("手动组合不支持一键应用建议")
