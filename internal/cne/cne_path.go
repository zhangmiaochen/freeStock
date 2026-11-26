package cne

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// cneExeName 是各平台上 cne 的命令文件名（首选名）。
func cneExeName() string {
	names := cneExeNames()
	return names[0]
}

// cneExeNames 返回**真实可能存在的可执行文件名**。
//
// ⚠ 关键是第二个：pip 安装 CNEquity 后，console script 叫 `cnequity`
// （实测 Windows 上 `D:\...\Python311\Scripts\cnequity.exe`），不是 `cne`。
// 只找 `cne` 会得到"明明装了却报找不到"——用户当时就是这么撞上的：
// 界面上更新数据报"找不到 cne"，而同目录里就躺着 cnequity.exe。
func cneExeNames() []string {
	if runtime.GOOS == "windows" {
		return []string{"cne.exe", "cnequity.exe"}
	}
	return []string{"cne", "cnequity"}
}

// cneBinaryNames 是"用户填了个目录"时要依次尝试的相对路径。
//
// 覆盖三种真实布局：
//  1. 直接放进可执行文件的目录（如 /opt/cne/bin）→ cne / cnequity
//  2. **Python venv 根目录**（如 /opt/cne 或 nas 上的 cne-venv）→ bin/<name>
//  3. Windows 上 pip 的安装位置 → Scripts\<name>.exe
//
// 不跨平台的那些候选只是多 stat 一次，代价可忽略。
func cneBinaryNames() []string {
	names := cneExeNames()
	out := make([]string, 0, len(names)*3)
	for _, name := range names {
		out = append(out, name, filepath.Join("bin", name), filepath.Join("Scripts", name))
	}
	return out
}

// checkGood 确认候选路径确实能当可执行文件用。
//
// Unix 上额外检查执行位：从宿主机挂进来的目录常常是 0644，
// 只报 "permission denied" 很难定位，这里直接说清楚要 chmod。
func checkGood(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("cne 不可用（%s）：%w", path, err)
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s 是目录，不是可执行文件", path)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("cne 没有执行权限（%s，当前 %04o）：请 chmod +x", path, fi.Mode().Perm())
	}
	return path, nil
}

// ResolveCNE 把设置里填的 cne 配置解析成**可以直接 exec 的路径**。
//
// 设计目标：用户不该被追着填到"可执行文件"这一层。容器/NAS 场景里，
// 他手里通常是一个**目录**（宿主机上 cne 的安装位置，挂载进容器后还是那个路径），
// 所以目录也要认。
//
// 支持三种填法：
//
//  1. 留空            → 按 PATH 找 cne，再兜底几个约定位置
//  2. 可执行文件路径   → 校验后直接用
//  3. **目录**         → 在目录里找 cne / bin/cne / Scripts/cne.exe
//
// 另外裸命令名（不含路径分隔符，如 "cne"）仍按 PATH 解析 ——
// 这是设置项为空时同一条路径，保留它是为了兼容老配置。
func ResolveCNE(configured string) (string, error) {
	p := strings.TrimSpace(configured)

	if p == "" {
		return findCNEOnPath()
	}

	// 裸命令名：交给 PATH（注意 "cne.exe" 这种名字里没有分隔符，
	// 只看 os.Stat 会把它误判成路径不存在）
	if !strings.ContainsAny(p, `/\`) {
		if found, err := exec.LookPath(p); err == nil {
			return checkGood(found)
		}
		// 用户习惯写 "cne"，而 pip 装出来的命令叫 "cnequity" —— 只在这两个
		// 已知别名之间互相回退；随手写错的名字仍然报错，不静默换成别的程序。
		if isCNEAlias(p) {
			for _, name := range cneExeNames() {
				if name == p {
					continue
				}
				if found, err := exec.LookPath(name); err == nil {
					return checkGood(found)
				}
			}
		}
		return "", fmt.Errorf("在 PATH 上找不到 %q（若它是目录，请填绝对路径；pip 装出来的命令名通常是 cnequity）", p)
	}

	fi, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("配置的 cne 路径不可用（%s）：%w", p, err)
	}
	if !fi.IsDir() {
		return checkGood(p)
	}

	// 目录：按约定布局找
	tried := make([]string, 0, 3)
	for _, name := range cneBinaryNames() {
		cand := filepath.Join(p, name)
		if st, statErr := os.Stat(cand); statErr == nil && !st.IsDir() {
			return checkGood(cand)
		}
		tried = append(tried, cand)
	}
	return "", fmt.Errorf("目录 %s 里没有 cne（找过：%s）", p, strings.Join(tried, "、"))
}

// defaultCNECandidates 返回未配置时的约定路径，单独导出给同包回归测试。
//
// Windows 上不写死某一台机器的安装位置，而是扫 PATH 与常见 Python 目录
// （pip 会把 cne.exe 装到 <Python>\Scripts 下，那个目录不保证在 PATH 里）。
func defaultCNECandidates() []string {
	cands := []string{
		"/vol2/1001/docker/cne-venv/bin/cne",      // fnOS compose 示例：宿主机 venv 映射到此容器路径
		"/vol2/1001/docker/cne-venv/bin/cnequity", // 同上，pip 实际安装的名字
		"/data/cne-venv/bin/cne",                  // 容器：装在数据卷上的 venv
		"/data/cne-venv/bin/cnequity",
		"/opt/cne-venv/bin/cne", // 容器：构建镜像时装进去的 venv
		"/opt/cne-venv/bin/cnequity",
	}
	if runtime.GOOS == "windows" {
		cands = append(windowsCNECandidates(), cands...)
	}
	return cands
}

// windowsCNECandidates 在 Windows 上扫一遍 cne / cnequity 可能出现的位置。
func windowsCNECandidates() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	names := cneExeNames()
	out := make([]string, 0, 16)
	seen := make(map[string]bool, 16)
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		for _, name := range names {
			add(filepath.Join(dir, name))
		}
	}
	bases := []string{
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Python"),
		filepath.Join(os.Getenv("APPDATA"), "Python"),
		filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Roaming", "Python"),
		`C:\Python`,
		`C:\Program Files\Python`,
	}
	for _, base := range bases {
		if strings.TrimSpace(base) == "" {
			continue
		}
		for _, name := range names {
			for _, pattern := range []string{
				filepath.Join(base, "Python*", "Scripts", name),
				filepath.Join(base, "Python*", name),
			} {
				matches, err := filepath.Glob(pattern)
				if err != nil {
					continue
				}
				for _, match := range matches {
					add(match)
				}
			}
		}
	}
	return out
}

// inContainer 判断当前进程是否真的跑在容器里。
//
// 用它只是为了**把错误提示说对**：在 Windows 桌面版上说"宿主机路径对容器不可见"
// 会让人以为程序认错了系统（实测用户就是这么反馈的）。
func inContainer() bool {
	if runtime.GOOS == "windows" {
		return false
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	if raw, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		text := string(raw)
		for _, marker := range []string{"docker", "kubepods", "containerd", "lxc", "podman"} {
			if strings.Contains(text, marker) {
				return true
			}
		}
	}
	return false
}

// cneNotFoundError 按**实际运行环境**给出可操作的提示。
func cneNotFoundError(tried []string) error {
	list := strings.Join(tried, "、")
	if list == "" {
		list = "（无）"
	}
	switch {
	case inContainer():
		return fmt.Errorf("找不到 cne：容器里没有 cne。请把 cne venv 挂载进容器（例如 /vol2/.../cne-venv:/vol2/.../cne-venv:ro）并在设置里填容器内的 cne 路径，或在镜像里安装 cne。找过：%s", list)
	case runtime.GOOS == "windows":
		return fmt.Errorf("找不到 cne：PATH 和常见 Python 安装目录里都没有 cne.exe / cnequity.exe。"+
			"请先在命令行执行 pip install cnequity（装到哪个 Python，就填哪个 Python 的 Scripts 目录），"+
			"或在「设置 → 数据源」把 cne 填成完整路径（例如 <Python安装目录>\\Scripts\\cnequity.exe）。"+
			"可用 where cnequity / pip show cnequity 确认它装在哪。找过：%s", list)
	default:
		return fmt.Errorf("找不到 cne：PATH 与常见安装目录里都没有 cne / cnequity。"+
			"请先安装（pip install cnequity），或在设置里填 cne 的完整路径。找过：%s", list)
	}
}

// findCNEOnPath 在没配置（或配置为空）时的发现顺序。
func findCNEOnPath() (string, error) {
	// 直接搜 PATH：两个名字都要试（pip 装出来的 console script 叫 cnequity）。
	for _, name := range cneExeNames() {
		if p, err := exec.LookPath(name); err == nil {
			if good, checkErr := checkGood(p); checkErr == nil {
				return good, nil
			}
		}
	}
	// 兜底常见位置（Windows 上包含 PATH 与各 Python 的 Scripts 目录）。
	tried := make([]string, 0, 8)
	for _, cand := range defaultCNECandidates() {
		if _, err := os.Stat(cand); err == nil {
			if good, checkErr := checkGood(cand); checkErr == nil {
				return good, nil
			}
		}
		tried = append(tried, cand)
	}
	return "", cneNotFoundError(tried)
}

// isCNEAlias 判断裸命令名是不是 cne 的已知别名（cne / cnequity，可带 .exe）。
func isCNEAlias(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	n = strings.TrimSuffix(n, ".exe")
	return n == "cne" || n == "cnequity"
}
