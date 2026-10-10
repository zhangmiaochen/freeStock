package cne

import (
	"strings"
	"testing"
)

// ⚠ **方案 §2.4 的坑必须有单测守住**。
//
// 本机环境里同时存在 `NO_PROXY` 与 `no_proxy`，只清大写那份时，
// httpx 仍会把小写那份的 `::1,[::1]` 当成代理挂载点，报
// `InvalidURL: Invalid port: ':1]'`，导致**每个 step 都崩**。
//
// 而 Python 的 `os.environ` 在 Windows 上会把键名统一成大写，
// 从 Python 侧看不出重复项 —— 所以只能在这里守住。
func TestBuildEnvStripsAllProxyVars(t *testing.T) {
	// 构造大小写混杂的代理变量，模拟本机的真实情况
	t.Setenv("no_proxy", "::1,[::1]")
	t.Setenv("NO_PROXY", "localhost")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1080")
	t.Setenv("https_proxy", "http://127.0.0.1:1080")
	t.Setenv("All_Proxy", "socks5://127.0.0.1:1080")
	t.Setenv("FTP_PROXY", "http://127.0.0.1:1080")

	env := BuildEnv()

	for _, kv := range env {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if isProxyVar(key) {
			t.Errorf("**代理变量未被清掉**：%s —— 这会让 cne 的每个 step 都崩在 httpx.InvalidURL", kv)
		}
	}
	t.Logf("过滤后共 %d 个环境变量，其中没有任何 proxy 项", len(env))
}

// 大小写不敏感是这件事的关键 —— 单独钉一遍。
func TestIsProxyVarCaseInsensitive(t *testing.T) {
	cases := map[string]bool{
		"NO_PROXY":   true,
		"no_proxy":   true,
		"No_Proxy":   true,
		"HTTP_PROXY": true,
		"http_proxy": true,
		"ALL_PROXY":  true,
		"PATH":       false,
		"HOME":       false,
		"PYTHONUTF8": false,
		// 不能把正常的变量误伤
		"PROXY_MODE":  false,
		"NO_PROXYING": false,
	}
	for key, want := range cases {
		if got := isProxyVar(key); got != want {
			t.Errorf("isProxyVar(%q) = %v，期望 %v", key, got, want)
		}
	}
}

// UTF-8 相关变量必须被加上 —— 否则中文进度重定向到文件会变乱码。
func TestBuildEnvEnablesUTF8(t *testing.T) {
	env := BuildEnv()
	want := map[string]bool{
		"PYTHONUTF8=1":           false,
		"PYTHONIOENCODING=utf-8": false,
		"PYTHONUNBUFFERED=1":     false,
	}
	for _, kv := range env {
		if _, ok := want[kv]; ok {
			want[kv] = true
		}
	}
	for kv, found := range want {
		if !found {
			t.Errorf("环境里缺少 %s（中文日志会乱码，或进度无法实时 tail）", kv)
		}
	}
}
