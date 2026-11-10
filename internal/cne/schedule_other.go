//go:build !windows

package cne

// 非 Windows 平台（Linux / NAS / Docker）没有 schtasks，也没有可靠可用的
// cron（容器里通常没有 cron 守护进程，NAS 的 crond 也各家不同）。
//
// 所以这里换一种**同语义、不同实现**的做法：**进程内调度器**。
//
//   - Windows：schtasks 注册系统任务，应用关着也会到点跑
//   - Linux/NAS：应用自己常驻（Docker 容器 / systemd 服务），到点由
//     进程内的 goroutine 触发；应用停着就不会跑 —— 这与"容器没起就没有服务"
//     的部署模型是一致的
//
// ⚠ **本文件不依赖 config 包**：cne 是"调用外部 cne 命令"的底层包，
// 让它反向依赖设置层会形成难以测试的耦合。真正的状态（开关 + 时间）
// 存在 config.json 里，由 service 层实现 `LocalScheduler` 注入进来。
// 没注入时所有调用都返回明确的"未就绪"，而不是静默成功 ——
// 否则界面会显示"已开启自动更新"但其实什么都没做（这正是要避免的坑）。

const (
	TaskDaily = "cne-daily-update" // 盘后主日更
	TaskStale = "cne-daily-stale"  // 稍后补抓落后
)

// TaskInfo 与 Windows 版字段**完全一致**，前端不需要按平台分叉。
type TaskInfo struct {
	Name string `json:"name"`
	// Registered 表示任务是否存在。进程内调度器恒为 true：
	// 它不是一个需要"注册"的外部实体。
	Registered bool   `json:"registered"`
	NextRun    string `json:"nextRun"`
	// ConfiguredTime 是**配置里**设定的时间（HH:MM），与任务是否启用无关。
	//
	// 为什么要它：NextRun 只在任务启用时才有值（schtasks 对禁用任务报 N/A），
	// 而界面上"把时间从 17:30 改成 18:00"需要知道**当前值**。
	// 进程内调度器直接读 config.json，所以这里总是填得出来。
	ConfiguredTime string `json:"configuredTime,omitempty"`
	Status         string `json:"status"`
	LastResult     string `json:"lastResult"`
	LastRun        string `json:"lastRun"`
	Command        string `json:"command"`
	Enabled        bool   `json:"enabled"`
	// ScheduledState 是原始的 "Scheduled Task State" 字段（保持字段兼容）
	ScheduledState string `json:"scheduledState"`
	// StartWhenAvailable 表示"错过计划时间后是否补跑"。
	// 进程内调度器**总是补跑**：应用重启后若发现今天还没跑过、
	// 而计划时间已过，会立刻补一次。
	StartWhenAvailable      bool `json:"startWhenAvailable"`
	StartWhenAvailableKnown bool `json:"startWhenAvailableKnown"`
}

// ScheduleState 是界面上要显示的整体状态。
type ScheduleState struct {
	Daily       TaskInfo `json:"daily"`
	Stale       TaskInfo `json:"stale"`
	WrapperPath string   `json:"wrapperPath"`
	Summary     string   `json:"summary"`
	Note        string   `json:"note"`
}

// LocalScheduler 是 service 层注入的进程内调度器实现。
//
// 之所以用接口而不是直接读配置文件：调度器需要知道"改时间后要立刻生效"，
// 而这份状态由持有 config.Store 的 service 独占管理。
type LocalScheduler interface {
	Query(name string) (*TaskInfo, error)
	QuerySchedule() (*ScheduleState, error)
	CreateTask(name, scriptPath, at string) error
	ChangeTaskTime(name, at string) error
	DeleteTask(name string) error
	SetTaskEnabled(name string, enabled bool) error
	TaskEnabled(name string) (bool, error)
	ApplyRecommendedSettings(name string) error
	StartWhenAvailable(name string) (bool, bool)
}

// localScheduler 是当前注册的实现；nil 表示尚未就绪。
var localScheduler LocalScheduler

// RegisterLocalScheduler 由 service 层在构造时注入实现。
//
// 必须在应用启动、界面可用之前调用一次；重复调用会覆盖（测试用）。
func RegisterLocalScheduler(s LocalScheduler) { localScheduler = s }

// errNoLocalScheduler 是"还没注入实现"时的统一错误。
//
// 刻意不是"当前平台不支持计划任务" —— 后者会让用户以为 Linux 上
// 自动更新永远不可用。真实原因只是初始化顺序不对，应当报得准确。
type errNoLocalScheduler struct{}

func (errNoLocalScheduler) Error() string {
	return "进程内调度器尚未初始化（这是内部错误，请重启应用）"
}

// Query 读取一个任务的当前状态。
func Query(name string) (*TaskInfo, error) {
	if localScheduler == nil {
		return &TaskInfo{Name: name}, errNoLocalScheduler{}
	}
	return localScheduler.Query(name)
}

// QuerySchedule 读回两个任务的状态。
func QuerySchedule() (*ScheduleState, error) {
	if localScheduler == nil {
		return nil, errNoLocalScheduler{}
	}
	return localScheduler.QuerySchedule()
}

// CreateTask 在进程内调度器里没有"注册"这一步，直接设置时间。
func CreateTask(name, scriptPath, at string) error {
	if localScheduler == nil {
		return errNoLocalScheduler{}
	}
	return localScheduler.CreateTask(name, scriptPath, at)
}

// ChangeTaskTime 改一个任务的运行时间。
func ChangeTaskTime(name, at string) error {
	if localScheduler == nil {
		return errNoLocalScheduler{}
	}
	return localScheduler.ChangeTaskTime(name, at)
}

// DeleteTask 删除一个任务（等价于关掉它）。
func DeleteTask(name string) error {
	if localScheduler == nil {
		return errNoLocalScheduler{}
	}
	return localScheduler.DeleteTask(name)
}
