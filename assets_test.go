package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// TestEmbeddedFrontendIsServed 验证 frontend/dist 确实被编译进二进制，
// 并且能被 Wails 的资源处理器正确返回。
//
// 这是「窗口一片空白」最常见的成因：前端产物没被打进 exe，
// 或者资源路径对不上，页面里的 JS/CSS 全部 404。
func TestEmbeddedFrontendIsServed(t *testing.T) {
	handler := application.AssetFileServerFS(assets)
	server := httptest.NewServer(handler)
	defer server.Close()

	get := func(path string) (*http.Response, string) {
		t.Helper()
		resp, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s 失败: %v", path, err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("读取 %s 响应失败: %v", path, err)
		}
		return resp, string(body)
	}

	// 1. 首页
	resp, html := get("/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / 返回 %d，期望 200", resp.StatusCode)
	}
	if !strings.Contains(html, `id="app"`) {
		t.Fatalf("首页里没有 #app 挂载点，实际内容:\n%s", truncate(html, 400))
	}

	// 2. index.html 里引用的 JS / CSS 必须真的能取到
	refPattern := regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)
	refs := refPattern.FindAllStringSubmatch(html, -1)
	if len(refs) == 0 {
		t.Fatalf("首页里没有任何 /assets/ 引用，实际内容:\n%s", truncate(html, 400))
	}
	for _, match := range refs {
		assetPath := match[1]
		assetResp, body := get(assetPath)
		if assetResp.StatusCode != http.StatusOK {
			t.Errorf("GET %s 返回 %d，前端资源没有被正确打包", assetPath, assetResp.StatusCode)
			continue
		}
		if len(body) < 100 {
			t.Errorf("GET %s 只返回了 %d 字节，内容不完整", assetPath, len(body))
		}
		t.Logf("%-60s %6d 字节  200 OK", assetPath, len(body))
	}

	// 3. public/ 下的静态文件
	if resp, _ := get("/wails.png"); resp.StatusCode != http.StatusOK {
		t.Errorf("GET /wails.png 返回 %d", resp.StatusCode)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
