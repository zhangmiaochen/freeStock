package portfolio

import (
	"database/sql"
	"errors"
	"fmt"
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

		// 硬编码 clamp 到 3（原实现如此）
		target := action.TargetLayers
		if target > maxApplyLayers {
			target = maxApplyLayers
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
					Price:       action.Price,
					LayersDelta: -currentLayers,
					WeightDelta: -previous.PositionWeight,
					Reason:      action.Reason, SourceSignalID: "apply_rotation",
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
				WeightDelta: round(weight, 4) - previous.PositionWeight,
				Reason:      action.Reason, SourceSignalID: "apply_rotation",
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
