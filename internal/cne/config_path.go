package cne

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cneConfigRelPaths 是 cne 配置在"工程目录"下的可能位置，按优先级排列。
//
// 为什么有两个：
//   - Windows / CNEquity 源码布局一直是 `<工程>/configs/cnequity.toml`；
//   - Linux 上 cne 自己的默认位置是 `~/.config/cnequity/`，
//     用户把「工程目录」指到那里时，toml **直接**在目录里，没有 `configs/` 这一层。
//
// 只认第一种会把后者判成「配置不存在」。这个错误在容器里尤其难查 ——
// 它看起来跟"挂载没挂上"一模一样，而实际上是路径假设错了。
func cneConfigRelPaths() []string {
	return []string{
		filepath.Join("configs", "cnequity.toml"),
		"cnequity.toml",
	}
}

// ConfigPath 在工程目录里定位 cne 配置文件。
//
// 解析顺序：
//  1. `<工程>/configs/cnequity.toml`（传统布局，Windows 一直如此）
//  2. `<工程>/cnequity.toml`（Linux 上 ~/.config/cnequity 这种平铺布局）
//  3. 目录里**唯一的** `*.toml`（cne 不同版本/不同安装方式文件名不一定一样）
//
// 都失败时，错误信息里会列出**目录里实际有什么** —— 排查"是没挂上、
// 还是文件名不对"全靠这一句，否则只能靠猜。
func ConfigPath(projectDir string) (string, error) {
	dir := strings.TrimSpace(projectDir)
	if dir == "" {
		return "", fmt.Errorf("cne 工程目录为空")
	}

	tried := make([]string, 0, len(cneConfigRelPaths()))
	for _, rel := range cneConfigRelPaths() {
		p := filepath.Join(dir, rel)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		tried = append(tried, p)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("工程目录 %s 读不了：%w", dir, err)
	}

	// 兜底：目录里正好只有一个 .toml，就用它。
	var tomls []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ".toml") {
			tomls = append(tomls, e.Name())
		}
	}
	if len(tomls) == 1 {
		return filepath.Join(dir, tomls[0]), nil
	}

	base := fmt.Sprintf("工程目录 %s 里找不到 cne 配置（找过：%s）",
		dir, strings.Join(tried, "、"))

	if len(tomls) > 1 {
		// 多个 toml 时不能瞎猜，否则可能改错配置。
		return "", fmt.Errorf("%s；目录里有多个 toml，请把「工程目录」指到只含目标文件的那一级：%s",
			base, strings.Join(tomls, "、"))
	}

	return "", fmt.Errorf("%s；目录里实际有：%s。"+
		"容器里请确认该目录已挂载，且挂载路径与宿主机完全一致", base, listDir(entries))
}

// listDir 把目录内容压成一行，供错误信息使用。
func listDir(entries []os.DirEntry) string {
	if len(entries) == 0 {
		return "（空目录）"
	}
	const max = 12
	names := make([]string, 0, max+1)
	for i, e := range entries {
		if i >= max {
			names = append(names, "…")
			break
		}
		n := e.Name()
		if e.IsDir() {
			n += "/"
		}
		names = append(names, n)
	}
	return strings.Join(names, "、")
}
