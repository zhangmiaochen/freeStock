package cne

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// cneExeName 是各平台上 cne 的命令文件名。
func cneExeName() string {
	if runtime.GOOS == "windows" {
		return "cne.exe"
	}
	return "cne"
}

// cneBinaryNames 是"用户填了个目录"时要依次尝试的相对路径。
//
// 覆盖三种真实布局：
//  1. 直接放进可执行文件的目录（如 /opt/cne/bin）→ cne
//  2. **Python venv 根目录**（如 /opt/cne 或 nas 上的 cne-venv）→ bin/cne
//  3. Windows 上 pip 的安装位置 → Scripts\cne.exe
//
// 不跨平台的那些候选只是多 stat 一次，代价可忽略。
func cneBinaryNames() []string {
	name := cneExeName()
	return []string{
		name,
		filepath.Join("bin", name),
		filepath.Join("Scripts", name),
	}
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
		return "", fmt.Errorf("在 PATH 上找不到 %q（若它是目录，请填绝对路径）", p)
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

// findCNEOnPath 在没配置（或配置为空）时的发现顺序。
func findCNEOnPath() (string, error) {
	if p, err := exec.LookPath("cne"); err == nil {
		return checkGood(p)
	}
	// 兜底常见位置。
	//
	// Windows 的 `D:\softInstal\...` 是实测那台机器的路径；
	// 容器/NAS 上对应的是镜像约定好的 venv 位置（见 Dockerfile 的 PATH）——
	// 这几个兜底只是"少填一个设置项"的便利，找不到也不影响：
	// 用户仍可在界面里显式指定目录或可执行文件。
	for _, cand := range []string{
		`D:\softInstal\python3.11.3\Scripts\cne.exe`,
		"/data/cne-venv/bin/cne", // 容器：装在数据卷上的 venv
		"/opt/cne-venv/bin/cne",  // 容器：构建镜像时装进去的 venv
	} {
		if _, err := os.Stat(cand); err == nil {
			return checkGood(cand)
		}
	}
	return "", fmt.Errorf("找不到 cne：请在设置里填 cne 的目录（或可执行文件），或把它放进 PATH")
}
