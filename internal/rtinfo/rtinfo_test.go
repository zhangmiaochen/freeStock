package rtinfo

import "testing"

// cgroup 的 limit 文件有几种"表示不限制"的写法，解析必须都能识别成 0，
// 否则会算出一个天文数字的软上限（等于没设），问题被掩盖。
func TestParseLimit(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int64
	}{
		{"v2 正常值", "2147483648\n", 2147483648},
		{"v1 正常值", "1073741824\n", 1073741824},
		{"v2 不限制（max）", "max\n", 0},
		{"v1 不限制（超大值）", "9223372036854771712\n", 0},
		{"空文件", "", 0},
		{"只有空白", "  \n\t", 0},
		{"非法内容", "not-a-number", 0},
		{"零", "0", 0},
		{"负数", "-1", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseLimit(tc.in)
			if got != tc.want {
				t.Errorf("in=%q 应为 %d，实际 %d", tc.in, tc.want, got)
			}
		})
	}
}

// 日志里的可读单位（纯粹为了排错方便，算错不影响功能但要准）。
func TestFormat(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{512, "512B"},
		{2 << 20, "2MiB"},
		{2 << 30, "2.0GiB"},
	}
	for _, tc := range cases {
		if got := Format(tc.in); got != tc.want {
			t.Errorf("%d 应为 %q，实际 %q", tc.in, tc.want, got)
		}
	}
}
