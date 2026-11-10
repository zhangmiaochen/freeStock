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
//   - Linux 上 cne 自己的默认位置是 `~/.config/cnequity/cnequity.toml`，
//     用户把「工程目录」指到 `~/.config/cnequity` 时，toml **直接**在目录里，
//     没有 `configs/` 这一层。
//
// 只认第一种会把后者判成「配置不存在」。这个错误在容器里尤其难查 ——
// 它看起来跟"挂载没挂上"一模一样，而实际上是路径假设错了。
func cneConfigRelPaths() []string {
	return []string{
		filepath.Join("configs", "cnequity.toml"),
		"cnequity.toml",
	}
}

// ConfigPath 在工程目录里定位 cne 配置文件，找不到时把找过的路径都列出来。
func ConfigPath(projectDir string) (string, error) {
	dir := strings.TrimSpace(projectDir)
	if dir == "" {
		return "", fmt.Errorf("cne 工程目录为空")
	}

	tried := make([]string, 0, 2)
	for _, rel := range cneConfigRelPaths() {
		p := filepath.Join(dir, rel)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
		tried = append(tried, p)
	}
	return "", fmt.Errorf("工程目录 %s 里找不到 cne 配置（找过：%s）",
		dir, strings.Join(tried, "、"))
}
