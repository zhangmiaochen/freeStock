// Package config 负责应用配置的读取与保存。
package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DataSource 描述一个数据源。
type DataSource struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	URL  string `json:"url"`
	Kind string `json:"kind"` // local / remote / custom
}

// 数据源类型。
const (
	// KindStockDB 表示行情通过 HTTP 访问 free-stockdb 服务
	KindStockDB = "stockdb"
	// KindCnLake 表示行情直接读 cnequity 本地数据湖（parquet）
	KindCnLake = "cnlake"
)

// Presets 是内置的数据源预设。
var Presets = []DataSource{
	{
		Key:  "local",
		Name: "本地数据（本机 stockdb）",
		URL:  "http://127.0.0.1:7899",
		Kind: KindStockDB,
	},
	{
		Key:  "remote",
		Name: "远程数据（公共测试节点）",
		URL:  "http://8.138.149.215:7899",
		Kind: KindStockDB,
	},
	{
		// 数据湖没有 URL：留空表示自动探测数据湖位置，也可以填具体根目录
		Key:  "cnlake",
		Name: "本地数据湖（cnequity）",
		URL:  "",
		Kind: KindCnLake,
	},
}

// ---------------------------------------------------------------------------
// K 线页偏好
//
// 指标算法在前端，后端不做指标语义校验，只负责持久化。
// 但会做一次 Sanitize：手工改坏配置文件不该让界面崩掉。
// ---------------------------------------------------------------------------

// MaPrefs 一条均线的配置。
type MaPrefs struct {
	Period  int    `json:"period"`
	Color   string `json:"color"`
	Visible bool   `json:"visible"`
}

// BollPrefs 布林线参数。
type BollPrefs struct {
	Period int     `json:"period"`
	Mult   float64 `json:"mult"`
}

// MacdPrefs MACD 参数。
type MacdPrefs struct {
	Fast   int `json:"fast"`
	Slow   int `json:"slow"`
	Signal int `json:"signal"`
}

// KlinePrefs 是 K 线页的偏好设置，由前端整份读写。
type KlinePrefs struct {
	Period     string    `json:"period"` // day / week / month / year
	Adjust     string    `json:"adjust"` // qfq / hfq / none
	ShowBoll   bool      `json:"showBoll"`
	ShowMacd   bool      `json:"showMacd"`
	ShowVolume bool      `json:"showVolume"`
	ShowFlow   bool      `json:"showFlow"`
	Boll       BollPrefs `json:"boll"`
	Macd       MacdPrefs `json:"macd"`
	MaConfigs  []MaPrefs `json:"maConfigs"`
}

// DefaultKlinePrefs 返回首次使用时的默认值（与界面默认一致）。
func DefaultKlinePrefs() KlinePrefs {
	colors := []string{
		"#f5c542", "#ec4899", "#38bdf8", "#a78bfa", "#34d399",
		"#fb923c", "#f472b6", "#4ade80", "#60a5fa", "#facc15",
	}
	periods := []int{5, 20, 60, 120, 250}
	mas := make([]MaPrefs, 0, len(periods))
	for i, period := range periods {
		mas = append(mas, MaPrefs{Period: period, Color: colors[i%len(colors)], Visible: true})
	}
	return KlinePrefs{
		Period:     "day",
		Adjust:     "qfq",
		ShowBoll:   false,
		ShowMacd:   true,
		ShowVolume: true,
		ShowFlow:   true,
		Boll:       BollPrefs{Period: 20, Mult: 2},
		Macd:       MacdPrefs{Fast: 12, Slow: 26, Signal: 9},
		MaConfigs:  mas,
	}
}

func clampInt(value, min, max, fallback int) int {
	if value < min || value > max {
		if value == 0 {
			return fallback
		}
		if value < min {
			return min
		}
		return max
	}
	return value
}

// Sanitize 把配置修到合法范围；非法值回落到默认值。
func (p KlinePrefs) Sanitize() KlinePrefs {
	def := DefaultKlinePrefs()

	switch p.Period {
	case "day", "week", "month", "year":
	default:
		p.Period = def.Period
	}
	switch p.Adjust {
	case "qfq", "hfq", "none":
	default:
		p.Adjust = def.Adjust
	}

	p.Boll.Period = clampInt(p.Boll.Period, 2, 250, def.Boll.Period)
	if p.Boll.Mult < 0.5 || p.Boll.Mult > 10 {
		p.Boll.Mult = def.Boll.Mult
	}

	p.Macd.Fast = clampInt(p.Macd.Fast, 2, 200, def.Macd.Fast)
	p.Macd.Slow = clampInt(p.Macd.Slow, 3, 400, def.Macd.Slow)
	p.Macd.Signal = clampInt(p.Macd.Signal, 1, 100, def.Macd.Signal)
	// 慢线必须大于快线，否则 MACD 没有意义
	if p.Macd.Slow <= p.Macd.Fast {
		p.Macd.Slow = p.Macd.Fast + 1
	}

	if len(p.MaConfigs) == 0 {
		p.MaConfigs = def.MaConfigs
	} else {
		cleaned := make([]MaPrefs, 0, len(p.MaConfigs))
		for i, ma := range p.MaConfigs {
			ma.Period = clampInt(ma.Period, 1, 1000, def.MaConfigs[i%len(def.MaConfigs)].Period)
			if len(ma.Color) != 7 || ma.Color[0] != '#' {
				ma.Color = def.MaConfigs[i%len(def.MaConfigs)].Color
			}
			cleaned = append(cleaned, ma)
		}
		p.MaConfigs = cleaned
	}
	return p
}

type fileData struct {
	DataSourceURL string `json:"dataSourceUrl"`
	// DataSourceKind 区分行情来自 free-stockdb 还是本地数据湖
	DataSourceKind string `json:"dataSourceKind"`
	// CnLakeRoot 是 cnequity 数据湖根目录（可选）。本地数据集没有财报，
	// 配了这个就能从数据湖的 curated parquet 里读 financial_statement_items 等。
	CnLakeRoot string `json:"cnLakeRoot"`
	// KlinePrefs 是 K 线页的指标参数与显示开关
	KlinePrefs *KlinePrefs `json:"klinePrefs,omitempty"`
	// ScreenPrefs 是条件选股的筛选条件。
	//
	// 这里存 RawMessage 而不是具体结构：条件类型由 service 包定义，
	// config 不该反向依赖它（会形成循环）。由 service 负责编解码。
	ScreenPrefs json.RawMessage `json:"screenPrefs,omitempty"`
}

// Store 管理配置文件。
type Store struct {
	mu   sync.Mutex
	path string
	data fileData
}

// NewStore 创建配置存储。
//
// 路径优先放在**程序所在目录**（绿色软件的习惯：配置跟着程序走，
// 拷走整个文件夹就能带走设置）。目录不可写时退回用户配置目录。
func NewStore() *Store {
	s := &Store{path: ResolvePath(executableDir())}
	if migrated := migrateLegacy(s.path, legacyConfigPath()); migrated {
		log.Printf("[FreeStock] 已把配置从 %s 迁移到 %s", legacyConfigPath(), s.path)
	}
	s.load()
	return s
}

// NewStoreAt 在指定路径创建配置存储，供测试注入隔离的配置文件。
func NewStoreAt(path string) *Store {
	s := &Store{path: path}
	s.load()
	return s
}

// executableDir 返回程序所在目录；取不到时返回空字符串。
func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(exe)
}

// legacyConfigPath 是旧版本的配置位置（用户配置目录）。
func legacyConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "FreeStock", "config.json")
}

// ResolvePath 按优先级挑一个可写的配置文件路径。
//
//  1. 程序目录（exeDir 为空则跳过）
//  2. 用户配置目录
//
// 会真的写一个探测文件来确认目录可写 —— 装在 Program Files 下的场景
// 目录是只读的，直接在那里建配置会失败。
func ResolvePath(exeDir string) string {
	if exeDir != "" && dirWritable(exeDir) {
		return filepath.Join(exeDir, "config.json")
	}
	if legacy := legacyConfigPath(); legacy != "" {
		if dir := filepath.Dir(legacy); dirWritable(dir) {
			return legacy
		}
	}
	// 都不行也得返回一个路径：写入失败时内存里的设置仍然生效
	return "config.json"
}

// dirWritable 通过实际写一个探测文件判断目录可写（目录不存在则尝试创建）。
func dirWritable(dir string) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false
	}
	probe, err := os.CreateTemp(dir, ".write-probe-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return true
}

// migrateLegacy 把旧位置的配置复制到新位置。
//
// 只在「新位置没有配置」且「旧位置有」时做，避免覆盖用户当前的设置。
// 迁移后不删旧文件 —— 留一份备份，万一新位置后来不可写还能退回去。
func migrateLegacy(newPath, oldPath string) bool {
	if newPath == "" || oldPath == "" || newPath == oldPath {
		return false
	}
	if _, err := os.Stat(newPath); err == nil {
		return false // 新位置已有配置
	}
	raw, err := os.ReadFile(oldPath)
	if err != nil {
		return false // 旧位置没有，属正常首次运行
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		return false
	}
	return os.WriteFile(newPath, raw, 0o644) == nil
}

// Path 返回配置文件路径（可能为空）。
func (s *Store) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

func (s *Store) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var parsed fileData
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return
	}
	s.data = parsed
}

// DataSourceURL 返回当前配置的数据源地址，未配置时返回空字符串。
func (s *Store) DataSourceURL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.data.DataSourceURL)
}

// DataSourceKind 返回当前配置的数据源类型；未配置时按 stockdb 处理。
func (s *Store) DataSourceKind() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	kind := strings.TrimSpace(s.data.DataSourceKind)
	if kind == "" {
		return KindStockDB
	}
	return kind
}

// SetDataSource 保存数据源类型与地址。
func (s *Store) SetDataSource(url, kind string) {
	if kind == "" {
		kind = KindStockDB
	}
	s.mu.Lock()
	s.data.DataSourceURL = strings.TrimSpace(url)
	s.data.DataSourceKind = kind
	path := s.path
	payload, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	s.write(path, payload, err)
}

// SetDataSourceURL 只保存地址（兼容旧调用）。
func (s *Store) SetDataSourceURL(url string) {
	s.SetDataSource(url, KindStockDB)
}

func (s *Store) write(path string, payload []byte, err error) {
	if err != nil || path == "" {
		return
	}
	if mkErr := os.MkdirAll(filepath.Dir(path), 0o755); mkErr != nil {
		return
	}
	_ = os.WriteFile(path, payload, 0o644)
}

// CnLakeRoot 返回配置的 cnequity 数据湖根目录，未配置时返回空字符串（由 cnlake 自动探测）。
func (s *Store) CnLakeRoot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.data.CnLakeRoot)
}

// SetCnLakeRoot 保存数据湖根目录。
func (s *Store) SetCnLakeRoot(root string) {
	s.mu.Lock()
	s.data.CnLakeRoot = strings.TrimSpace(root)
	path := s.path
	payload, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	s.write(path, payload, err)
}

// KlinePrefs 返回已保存的 K 线偏好。
// 第二个返回值为 false 表示从未保存过（首次使用），调用方可以用默认值。
func (s *Store) KlinePrefs() (KlinePrefs, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.KlinePrefs == nil {
		return DefaultKlinePrefs(), false
	}
	return s.data.KlinePrefs.Sanitize(), true
}

// SetKlinePrefs 保存 K 线偏好。写入失败会被忽略（内存中仍生效）。
func (s *Store) SetKlinePrefs(prefs KlinePrefs) {
	sanitized := prefs.Sanitize()
	s.mu.Lock()
	s.data.KlinePrefs = &sanitized
	path := s.path
	payload, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	s.write(path, payload, err)
}

// ScreenPrefs 返回已保存的筛选条件原始 JSON；nil 表示从未保存过。
func (s *Store) ScreenPrefs() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data.ScreenPrefs) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(s.data.ScreenPrefs))
	copy(out, s.data.ScreenPrefs)
	return out
}

// SetScreenPrefs 保存筛选条件；raw 由 service 包序列化。
func (s *Store) SetScreenPrefs(raw json.RawMessage) {
	s.mu.Lock()
	s.data.ScreenPrefs = raw
	path := s.path
	payload, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	s.write(path, payload, err)
}
