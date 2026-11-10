// Package config 负责应用配置的读取与保存。
package config

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"freestock/internal/chanlun"
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
	Period     string `json:"period"` // day / week / month / year
	Adjust     string `json:"adjust"` // qfq / hfq / none
	ShowBoll   bool   `json:"showBoll"`
	ShowMacd   bool   `json:"showMacd"`
	ShowVolume bool   `json:"showVolume"`
	ShowFlow   bool   `json:"showFlow"`
	// ShowFibonacci 是斐波那契黄金分割辅助线的开关。
	//
	// 分割位由前端按**当前可见区间**的最高/最低价动态计算（缩放、平移都会重算），
	// 所以后端只需要记住这个开关。
	ShowFibonacci bool `json:"showFibonacci"`
	// ShowRangeSelect 是「框选统计」的开关：在图上拖一个矩形，
	// 统计这一段区间的涨跌幅、最高/最低、最大回撤等。
	//
	// 与 ShowFibonacci 是**两套不同的区间读法**（那个跟着缩放走、这个由用户划），
	// 所以是两个独立的开关，可以同时开。统计全部在前端算（见 lib/rangeStats），
	// 后端同样只需要记住这个开关。
	ShowRangeSelect bool      `json:"showRangeSelect"`
	// ShowChanlun 是 K 线页「缠论」叠加层的开关；ChanlunLevel 是它的分析级别。
	//
	// 缠论结构全部在后端算（internal/chanlun），前端只消费标注数据；
	// 这里只记住"要不要画"和"画哪个级别"。
	ShowChanlun   bool      `json:"showChanlun"`
	ChanlunLevel  string    `json:"chanlunLevel"`
	Boll          BollPrefs `json:"boll"`
	Macd          MacdPrefs `json:"macd"`
	MaConfigs     []MaPrefs `json:"maConfigs"`
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
		// 斐波那契默认关闭：它是一条"读位置"的辅助线，不是每个人都要看
		ShowFibonacci: false,
		// 框选统计默认关闭：开启后左键拖拽变成框选（不再平移），
		// 会改变默认操作手感，必须由用户显式打开
		ShowRangeSelect: false,
		ShowChanlun:     false,
		ChanlunLevel:    string(chanlun.LevelDay),
		Boll:            BollPrefs{Period: 20, Mult: 2},
		Macd:            MacdPrefs{Fast: 12, Slow: 26, Signal: 9},
		MaConfigs:       mas,
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

	// 缠论级别：只允许引擎认识的级别，非法值回落到日线
	if !chanlun.ValidLevel(chanlun.Level(p.ChanlunLevel)) {
		p.ChanlunLevel = def.ChanlunLevel
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
	// UpdatePrefs 是 cne 数据更新的设置（手动/自动）。
	UpdatePrefs *UpdatePrefs `json:"updatePrefs,omitempty"`
	// EmailPrefs 是组合调仓日报的邮件设置。
	EmailPrefs *EmailPrefs `json:"emailPrefs,omitempty"`
	// ChanlunPrefs 是缠论引擎的配置（级别权重、类型权重、背驰力度、阈值等）。
	//
	// 直接存引擎的 Config：它是纯值结构、自带 Sanitize，没有反向依赖
	//（chanlun 包不 import config / service），所以不需要再写一层映射。
	ChanlunPrefs *chanlun.Config `json:"chanlunPrefs,omitempty"`
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
//  0. 环境变量 FREESTOCK_DATA_DIR（**仅容器 / NAS 部署用**，见下）
//  1. 程序目录（exeDir 为空则跳过）
//  2. 用户配置目录
//
// 会真的写一个探测文件来确认目录可写 —— 装在 Program Files 下的场景
// 目录是只读的，直接在那里建配置会失败。
//
// ⚠ `FREESTOCK_DATA_DIR` 是**纯增量**分支，Windows 桌面版不设这个变量，
// 走的还是原来的"exe 同目录优先"，行为完全不变。
// 容器里必须用它：镜像里的 /app 通常只读（非 root 用户），
// 而配置、组合库、缓存都该落在挂载卷上，否则重建容器就全丢。
func ResolvePath(exeDir string) string {
	if dir := strings.TrimSpace(os.Getenv("FREESTOCK_DATA_DIR")); dir != "" {
		if dirWritable(dir) {
			return filepath.Join(dir, "config.json")
		}
		log.Printf("[FreeStock] FREESTOCK_DATA_DIR=%s 不可写，回退默认路径", dir)
	}
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

// ChanlunPrefs 返回已保存的缠论配置。
//
// 第二个返回值为 false 表示从未保存过（首次使用），调用方用引擎默认值。
// 读到的手工改坏的配置会被 Sanitize 纠正，不会让界面崩掉。
func (s *Store) ChanlunPrefs() (chanlun.Config, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.ChanlunPrefs == nil {
		return chanlun.DefaultConfig(), false
	}
	return s.data.ChanlunPrefs.Sanitize(), true
}

// SetChanlunPrefs 保存缠论配置。
func (s *Store) SetChanlunPrefs(prefs chanlun.Config) {
	sanitized := prefs.Sanitize()
	s.mu.Lock()
	s.data.ChanlunPrefs = &sanitized
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

// ---------------------------------------------------------------------------
// cne 数据更新设置
// ---------------------------------------------------------------------------

// UpdatePrefs 是 cne 数据更新的设置。
//
// 对应方案 docs/cne数据更新方案.md §3。**默认全部关闭自动行为** ——
// 注册计划任务会改动用户机器，必须是用户显式开启。
type UpdatePrefs struct {
	AutoDaily      bool     `json:"autoDaily"`            // 是否启用每日自动更新（计划任务）
	DailyTime      string   `json:"dailyTime"`            // "17:30"，盘后主日更
	AutoStale      bool     `json:"autoStale"`            // 是否启用"补抓落后"（计划任务）
	StaleTime      string   `json:"staleTime"`            // "21:00"
	CatchUpOnStart bool     `json:"catchUpOnStart"`       // 启动时发现落后就补（§5.2 第 2 层）
	Groups         []string `json:"groups,omitempty"`     // 限定调度组；空=全部
	CNEPath        string   `json:"cnePath,omitempty"`    // cne 可执行文件；空=按 PATH 找
	ProjectDir     string   `json:"projectDir,omitempty"` // cne 工作目录（须含 configs/cnequity.toml）
}

// DefaultUpdatePrefs 返回默认设置。
//
// 两个自动开关**默认关**：注册/修改计划任务会动到用户机器上的东西，
// 不能因为装了个软件就悄悄改。启动补跑默认开（只读判断 + 明确提示）。
func DefaultUpdatePrefs() UpdatePrefs {
	return UpdatePrefs{
		AutoDaily:      false,
		DailyTime:      "17:30",
		AutoStale:      false,
		StaleTime:      "21:00",
		CatchUpOnStart: true,
		ProjectDir:     `D:\workspace\stockTool\CNEquity`,
	}
}

// Sanitize 收敛非法值 —— 与 KlinePrefs.Sanitize 一样"永不报错，只纠正"。
//
// 时间必须能被计划任务接受：统一成 HH:MM，非法就回落默认。
func (p UpdatePrefs) Sanitize() UpdatePrefs {
	def := DefaultUpdatePrefs()

	p.DailyTime = sanitizeClock(p.DailyTime, def.DailyTime)
	p.StaleTime = sanitizeClock(p.StaleTime, def.StaleTime)

	if p.ProjectDir == "" {
		p.ProjectDir = def.ProjectDir
	}
	// 去重 + 去空的分组
	if len(p.Groups) > 0 {
		seen := make(map[string]bool, len(p.Groups))
		out := make([]string, 0, len(p.Groups))
		for _, g := range p.Groups {
			g = strings.TrimSpace(g)
			if g == "" || seen[g] {
				continue
			}
			seen[g] = true
			out = append(out, g)
		}
		p.Groups = out
	}
	p.CNEPath = strings.TrimSpace(p.CNEPath)
	return p
}

// sanitizeClock 把 "17:30" / "7:5" / "1730" 这类输入收敛成 HH:MM。
//
// 计划任务对时间格式敏感，非法值会让任务注册失败 —— 所以这里不报错、
// 只回落到默认值，和 KlinePrefs 的处理方式一致。
func sanitizeClock(value, fallback string) string {
	v := strings.TrimSpace(value)
	if v == "" {
		return fallback
	}
	// "1730" -> "17:30"
	if len(v) == 4 && !strings.Contains(v, ":") {
		v = v[:2] + ":" + v[2:]
	}
	parts := strings.Split(v, ":")
	if len(parts) != 2 {
		return fallback
	}
	hour, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	minute, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err1 != nil || err2 != nil {
		return fallback
	}
	if hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return fallback
	}
	return fmt.Sprintf("%02d:%02d", hour, minute)
}

// UpdatePrefs 返回已保存的设置；第二个返回值表示是否保存过。
func (s *Store) UpdatePrefs() (UpdatePrefs, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.UpdatePrefs == nil {
		return DefaultUpdatePrefs(), false
	}
	return s.data.UpdatePrefs.Sanitize(), true
}

// SetUpdatePrefs 保存设置。写入失败会被忽略（内存中仍生效）。
func (s *Store) SetUpdatePrefs(prefs UpdatePrefs) UpdatePrefs {
	sanitized := prefs.Sanitize()
	s.mu.Lock()
	s.data.UpdatePrefs = &sanitized
	path := s.path
	payload, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	s.write(path, payload, err)
	return sanitized
}

// ---------------------------------------------------------------------------
// 组合调仓日报的邮件设置
// ---------------------------------------------------------------------------

// 邮件传输加密方式。
//
// 三种都要支持，因为它们对应的是**服务商端口约定**，不是口味问题：
//   - 465（QQ/163 的 SMTP 默认）是**连上就握手**的隐式 TLS，不能发 STARTTLS；
//   - 587/25 是明文连接再用 STARTTLS 升级；
//   - 少数内网 relay 什么都不加密。
//
// auto 按端口猜（465 → ssl，其余 → starttls），猜错时的现象是连接超时或
// "first record does not look like a TLS handshake"，所以界面上可以显式覆盖。
const (
	EmailEncryptAuto     = "auto"
	EmailEncryptSSL      = "ssl"
	EmailEncryptSTARTTLS = "starttls"
	EmailEncryptNone     = "none"
)

// 发送范围。
const (
	EmailScopeActive = "active" // 全部非归档组合（默认）
	EmailScopeOne    = "one"    // 指定一个组合
)

// EmailPrefs 是「每日调仓日报」的邮件设置。
//
// ⚠ **授权码是明文存在 config.json 里的**，没有做加密。这是有意的取舍：
// 加密密钥只能存在同一台机器的同一个目录里，挡不住能读到该目录的人，
// 却会让"备份/迁移配置"变成一件需要解释的事。界面上把这件事写出来
// （见 EmailPrefsInfo.PasswordNote），而不是让用户以为它被加密了。
type EmailPrefs struct {
	Enabled bool `json:"enabled"`

	// ---- SMTP ----
	Host       string `json:"host"` // 如 smtp.qq.com
	Port       int    `json:"port"` // 465 / 587 / 25
	Username   string `json:"username"`
	Password   string `json:"password"`   // 授权码（明文，见上面的说明）
	Encryption string `json:"encryption"` // auto / ssl / starttls / none
	From       string `json:"from"`       // 发件人；留空用 Username
	FromName   string `json:"fromName"`   // 发件人显示名
	// TimeoutSeconds 是单次发送的超时（含建连与握手）。
	TimeoutSeconds int `json:"timeoutSeconds"`

	// ---- 收件 ----
	Recipients string `json:"recipients"` // 多个地址用换行/逗号/分号分隔
	Subject    string `json:"subject"`    // 允许 {date} / {portfolio} / {count} 占位

	// ---- 定时 ----
	SendTime string `json:"sendTime"` // "17:00"
	// Weekdays 是发送的星期（time.Weekday 的 0=周日 … 6=周六）。
	// 留空 = 每天都发；只勾周一~周五 = 只发交易日（节假日仍会发，见 README）。
	Weekdays []int `json:"weekdays"`

	// ---- 内容 ----
	Scope       string `json:"scope"`       // active / one
	PortfolioID string `json:"portfolioId"` // Scope=one 时用
	// RefreshScoresBeforeSend 表示"发送前先把评分刷到最新"。
	//
	// 为什么需要它：调仓建议是拿**某一帧评分**算出来的。那一帧如果还停在昨天
	// （应用刚启动、或当天盘后还没刷过），算出来的建议就是基于旧数据的 ——
	// 邮件内容看着正常，数字却是过期的。默认开。
	//
	// 代价是首刷一次全市场要几十秒（增量：当天已算过的不会重算），
	// 所以界面上要写出来，并允许用户关掉。
	RefreshScoresBeforeSend bool `json:"refreshScoresBeforeSend"`
	// ScoreLimit 是刷新评分时的只数上限，**0 = 全市场**（默认）。
	//
	// 与界面上的「评分只数」同一个含义：广度闸门按全市场统计，
	// 只评一部分会让它被系统性低估、进而判成"广度不足"并删光买入信号。
	// 所以默认全市场，只有确实想省掉那几十秒的用户才会去调它。
	ScoreLimit int `json:"scoreLimit"`
	// IncludeHoldings 附当前持仓明细（成本 / 现价 / 浮动盈亏）
	IncludeHoldings bool `json:"includeHoldings"`
	// IncludeCandidates 附候选池 Top N。默认关：候选池会让邮件长很多，
	// 而"今天要做什么"由调仓动作表回答。
	IncludeCandidates bool `json:"includeCandidates"`
	CandidateLimit    int  `json:"candidateLimit"`
}

// DefaultEmailPrefs 返回默认设置。
//
// **Enabled 默认关**：发邮件会真的把持仓数据发出去，必须由用户显式打开。
func DefaultEmailPrefs() EmailPrefs {
	return EmailPrefs{
		Enabled:    false,
		Host:       "",
		Port:       465,
		Encryption: EmailEncryptAuto,
		Subject:    "【FreeStock】{date} 调仓日报 · {portfolio}",
		SendTime:   "17:00",
		Weekdays:   []int{1, 2, 3, 4, 5},
		Scope:      EmailScopeActive,
		// 先刷评分再发：不刷的话建议可能基于昨天的评分，
		// "准时发出的一封过期邮件"比晚几十秒更有害
		RefreshScoresBeforeSend: true,
		ScoreLimit:              0, // 全市场
		IncludeHoldings:         true,
		IncludeCandidates:       false,
		CandidateLimit:          10,
		TimeoutSeconds:          30,
	}
}

// Sanitize 收敛非法值 —— 与 KlinePrefs / UpdatePrefs 一样"永不报错，只纠正"。
//
// ⚠ 收件人这里**只做格式归一**（去空白、统一成换行分隔），
// **不做地址合法性判定** —— 判定在 internal/email 里做，
// 那里能把非法地址原样报给用户看（"以下地址不合法：…"）而不是静默丢掉。
func (p EmailPrefs) Sanitize() EmailPrefs {
	def := DefaultEmailPrefs()

	p.Host = strings.TrimSpace(p.Host)
	p.Username = strings.TrimSpace(p.Username)
	p.From = strings.TrimSpace(p.From)
	p.FromName = strings.TrimSpace(p.FromName)
	p.PortfolioID = strings.TrimSpace(p.PortfolioID)

	// 端口：0 或越界都回落（0 是"没填"的常见形态）
	if p.Port <= 0 || p.Port > 65535 {
		p.Port = def.Port
	}

	switch p.Encryption {
	case EmailEncryptAuto, EmailEncryptSSL, EmailEncryptSTARTTLS, EmailEncryptNone:
	default:
		p.Encryption = def.Encryption
	}
	switch p.Scope {
	case EmailScopeActive, EmailScopeOne:
	default:
		p.Scope = def.Scope
	}

	p.SendTime = sanitizeClock(p.SendTime, def.SendTime)

	// 星期去重 + 收敛到 0~6；**空数组保留为空**（空 = 每天发，
	// 不能用默认值把它填回工作日 —— 那是两件不同的事）
	p.Weekdays = sanitizeWeekdays(p.Weekdays)

	if strings.TrimSpace(p.Subject) == "" {
		p.Subject = def.Subject
	}
	p.CandidateLimit = clampInt(p.CandidateLimit, 1, 50, def.CandidateLimit)
	p.TimeoutSeconds = clampInt(p.TimeoutSeconds, 5, 300, def.TimeoutSeconds)
	// ScoreLimit：**0 是合法值**（= 全市场），负数与超大值才收敛。
	// ⚠ 不能用 clampInt —— 它会把 0 当成"没填"回落到默认值，
	// 而这里 0 恰恰是默认值本身，语义相反。
	if p.ScoreLimit < 0 {
		p.ScoreLimit = 0
	}

	// 收件人：把逗号/分号/空白混写的输入统一成"一行一个"
	p.Recipients = NormalizeRecipients(p.Recipients)

	return p
}

// NormalizeRecipients 把各种分隔符混写的收件人统一成换行分隔。
//
// 支持换行、逗号（中英文）、分号（中英文）与空白分隔 —— 用户从
// Excel / 邮件客户端里粘出来的列表常常是这几种混在一起的。
func NormalizeRecipients(raw string) string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case '\n', '\r', ',', '，', ';', '；', '\t', ' ':
			return true
		}
		return false
	})
	out := make([]string, 0, len(fields))
	seen := make(map[string]bool, len(fields))
	for _, field := range fields {
		addr := strings.TrimSpace(field)
		if addr == "" {
			continue
		}
		key := strings.ToLower(addr)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, addr)
	}
	return strings.Join(out, "\n")
}

// sanitizeWeekdays 收敛星期集合。
func sanitizeWeekdays(values []int) []int {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(values))
	out := make([]int, 0, len(values))
	for _, day := range values {
		if day < 0 || day > 6 || seen[day] {
			continue
		}
		seen[day] = true
		out = append(out, day)
	}
	// 排序保证可比性（0=周日 排在最后更符合中文习惯，但这里统一升序即可）
	sort.Ints(out)
	return out
}

// EmailPrefs 返回已保存的邮件设置；第二个返回值表示是否保存过。
func (s *Store) EmailPrefs() (EmailPrefs, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.EmailPrefs == nil {
		return DefaultEmailPrefs(), false
	}
	return s.data.EmailPrefs.Sanitize(), true
}

// SetEmailPrefs 保存邮件设置。写入失败会被忽略（内存中仍生效）。
func (s *Store) SetEmailPrefs(prefs EmailPrefs) EmailPrefs {
	sanitized := prefs.Sanitize()
	s.mu.Lock()
	s.data.EmailPrefs = &sanitized
	path := s.path
	payload, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.Unlock()
	s.write(path, payload, err)
	return sanitized
}
