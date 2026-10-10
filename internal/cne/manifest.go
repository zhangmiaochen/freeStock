package cne

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite"
)

// ManifestPath 是 cne 的运行清单（SQLite）。
//
// ⚠ **这不是可有可无的**：方案 §2.1 实测**退出码不能当成功判据** ——
// 一次 `--stale-only` 补跑 exit=1、manifest 里 run 状态 failed，
// 而 8 个步骤里 5 个成功、写了 25,159 行；反过来 degraded 的运行会 exit=0。
// 所以"这次更新到底成没成"只能读这个库。
func ManifestPath(projectDir string) string {
	return filepath.Join(projectDir, "data", "cnequity", "meta", "manifest.db")
}

// RunSummary 是一次运行。
type RunSummary struct {
	RunID           string `json:"runId"`
	JobName         string `json:"jobName"`
	Status          string `json:"status"`
	StartedAt       string `json:"startedAt"`
	FinishedAt      string `json:"finishedAt"`
	RowsWritten     int64  `json:"rowsWritten"`
	ExecutionStatus string `json:"executionStatus"`
	CoverageStatus  string `json:"coverageStatus"`
	ErrorMessage    string `json:"errorMessage,omitempty"`
}

// DatasetResult 是某次运行里一个数据集的结果。
type DatasetResult struct {
	Dataset  string `json:"dataset"`
	Stage    string `json:"stage"`
	Status   string `json:"status"`
	Critical string `json:"criticality"`
	// RowsWritten 是**真正写进去的行数** —— 判断"有没有真的更新"看它
	RowsWritten int64 `json:"rowsWritten"`
	// UsableResult 表示这个结果是否可用
	UsableResult bool   `json:"usableResult"`
	ErrorCode    string `json:"errorCode,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
	ReasonCode   string `json:"reasonCode,omitempty"`
}

// IsUpstreamUnavailable 判断是不是"上游没有数据"这类不该归咎于本地的失败。
//
// 方案 §9 要求：上游熔断时**不显示"可重试"**，而是说明"明天自动重试"。
func (d DatasetResult) IsUpstreamUnavailable() bool {
	text := strings.ToLower(d.ReasonCode + " " + d.ErrorCode + " " + d.ErrorMessage + " " + d.Status)
	// ⚠ `source_transient` / `source_payload_invalid` 是**实测看到的** reason_code：
	// 前者是上游暂时不可用（明天重试即可），后者是上游给的数据有问题 ——
	// 两者都不是本地的错，按方案 §9 都不该显示成"可重试"让人去点。
	for _, marker := range []string{
		"source unavailable", "unavailable", "upstream",
		"source_transient", "source_payload_invalid",
		"no_data", "not_available",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// WroteRows 表示这个数据集这次确实写进了数据。
func (d DatasetResult) WroteRows() bool { return d.RowsWritten > 0 }

// RunDetail 是一次运行的完整结果。
type RunDetail struct {
	Run      RunSummary      `json:"run"`
	Datasets []DatasetResult `json:"datasets"`
	// Succeeded / Failed / Unavailable 是三类数据集的名字
	Succeeded   []string `json:"succeeded"`
	Failed      []string `json:"failed"`
	Unavailable []string `json:"unavailable"`
	// RowsWritten 是所有数据集写入行数之和
	RowsWritten int64 `json:"rowsWritten"`
	// Conclusion 是**人类可读的结论**，措辞刻意区分"进程退出"和"数据更新"
	Conclusion string `json:"conclusion"`
}

// RecentRuns 读最近的运行记录（倒序）。
func RecentRuns(projectDir string, limit int) ([]RunSummary, error) {
	db, err := openManifest(projectDir)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	rows, err := db.Query(`
		SELECT run_id, COALESCE(job_name,''), COALESCE(status,''),
		       COALESCE(started_at,''), COALESCE(finished_at,''),
		       COALESCE(rows_written,0),
		       COALESCE(execution_status,''), COALESCE(coverage_status,''),
		       COALESCE(error_message,'')
		FROM ingestion_runs
		ORDER BY COALESCE(finished_at, started_at, '') DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("读 ingestion_runs 失败：%w", err)
	}
	defer rows.Close()

	out := make([]RunSummary, 0, limit)
	for rows.Next() {
		var r RunSummary
		if err := rows.Scan(&r.RunID, &r.JobName, &r.Status, &r.StartedAt,
			&r.FinishedAt, &r.RowsWritten, &r.ExecutionStatus,
			&r.CoverageStatus, &r.ErrorMessage); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LatestRun 返回最近一次运行的详细结果。
//
// ⚠ 结论**不看退出码**（方案 §2.1），而是看：
//   - 每个数据集有没有真的写行（rows_written > 0）
//   - 失败的是不是"上游没有数据"（那不该算本地失败，明天会自动重试）
func LatestRun(projectDir string) (*RunDetail, error) {
	runs, err := RecentRuns(projectDir, 1)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return RunDetailFor(projectDir, runs[0])
}

// RunDetailFor 读某一次运行的详细结果。
func RunDetailFor(projectDir string, run RunSummary) (*RunDetail, error) {
	db, err := openManifest(projectDir)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT COALESCE(dataset,''), COALESCE(stage,''), COALESCE(status,''),
		       COALESCE(criticality,''), COALESCE(rows_written,0),
		       COALESCE(usable_result,0),
		       COALESCE(error_code,''), COALESCE(error_message,''), COALESCE(reason_code,'')
		FROM dataset_results
		WHERE run_id = ?
		ORDER BY dataset`, run.RunID)
	if err != nil {
		return nil, fmt.Errorf("读 dataset_results 失败：%w", err)
	}
	defer rows.Close()

	// ⚠ **必须按数据集聚合**：`dataset_results` 是**按 (数据集, stage) 一行**的，
	// 一个数据集会有多行。实测最近一次运行里 northbound_holdings 有 3 行、
	// 每行都写 3933 行 —— 直接累加会把它算成 11799 行，
	// 而且"数据集个数"也会虚高（10 个其实是 6 个）。
	//
	// 聚合口径：
	//   - 行数取**各 stage 的最大值**（同一份数据在多个 stage 里会重复记），
	//     不是求和；
	//   - 只要**任一 stage 写了行**就算这个数据集有数据；
	//   - 状态取"最能说明问题"的那个：有行就算成功，否则取第一个失败状态。
	detail := &RunDetail{Run: run}
	agg := map[string]*DatasetResult{}
	order := []string{}
	for rows.Next() {
		var d DatasetResult
		var usable int
		if err := rows.Scan(&d.Dataset, &d.Stage, &d.Status, &d.Critical,
			&d.RowsWritten, &usable, &d.ErrorCode, &d.ErrorMessage, &d.ReasonCode); err != nil {
			continue
		}
		d.UsableResult = usable != 0

		cur, ok := agg[d.Dataset]
		if !ok {
			copyRow := d
			agg[d.Dataset] = &copyRow
			order = append(order, d.Dataset)
			continue
		}
		if d.RowsWritten > cur.RowsWritten {
			cur.RowsWritten = d.RowsWritten
		}
		cur.UsableResult = cur.UsableResult || d.UsableResult
		// 保留"更严重"的那个状态与原因：有行 > 上游无数据 > 失败
		if !cur.WroteRows() && !d.WroteRows() {
			if cur.IsUpstreamUnavailable() && !d.IsUpstreamUnavailable() {
				cur.Status, cur.ErrorCode, cur.ErrorMessage, cur.ReasonCode =
					d.Status, d.ErrorCode, d.ErrorMessage, d.ReasonCode
			}
		}
	}

	sort.Strings(order)
	for _, name := range order {
		d := *agg[name]
		if d.Stage == "" {
			d.Stage = "aggregated"
		}
		detail.Datasets = append(detail.Datasets, d)
		detail.RowsWritten += d.RowsWritten

		switch {
		case d.WroteRows():
			detail.Succeeded = append(detail.Succeeded, d.Dataset)
		case d.IsUpstreamUnavailable():
			detail.Unavailable = append(detail.Unavailable, d.Dataset)
		default:
			detail.Failed = append(detail.Failed, d.Dataset)
		}
	}
	sort.Strings(detail.Succeeded)
	sort.Strings(detail.Failed)
	sort.Strings(detail.Unavailable)
	// 同一数据集的多条 stage 记录已合并，结论里不会重复出现
	detail.Conclusion = conclude(detail)
	return detail, nil
}

// conclude 给出**刻意区分"进程退出"与"数据更新"**的结论。
//
// 方案 §2.1：一次 exit=1 的补跑里 5 个步骤成功、写了 25,159 行，
// 而 degraded 的运行 exit=0。所以说"成功/失败"必须落到数据集粒度。
func conclude(d *RunDetail) string {
	total := len(d.Datasets)
	if total == 0 {
		return fmt.Sprintf("这次运行没有数据集结果记录（run 状态 %s）—— 无法判断数据是否更新", d.Run.Status)
	}
	parts := []string{
		fmt.Sprintf("本次运行共 %d 个数据集，写入 %d 行", total, d.RowsWritten),
	}
	if len(d.Succeeded) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个有数据", len(d.Succeeded)))
	}
	if len(d.Failed) > 0 {
		parts = append(parts, fmt.Sprintf("**%d 个失败**（%s）",
			len(d.Failed), strings.Join(d.Failed, "、")))
	}
	if len(d.Unavailable) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个上游暂无数据（%s）—— 这类明天会自动重试，不用手动补",
			len(d.Unavailable), strings.Join(d.Unavailable, "、")))
	}
	text := strings.Join(parts, "；") + "。"

	// 进程状态单独说，避免被当成数据结论
	if d.Run.Status != "" {
		text += fmt.Sprintf("（cne 进程状态：%s）", d.Run.Status)
	}
	return text
}

func openManifest(projectDir string) (*sql.DB, error) {
	if strings.TrimSpace(projectDir) == "" {
		return nil, fmt.Errorf("缺少 cne 工程目录")
	}
	path := ManifestPath(projectDir)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("找不到运行清单 %s：%w", path, err)
	}
	// ⚠ 只读打开：更新期间 cne 可能正在写这个库，
	// 用 immutable/ro 避免干扰它（本项目只读 cne 的东西，不改）。
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
