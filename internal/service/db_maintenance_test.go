package service

import "testing"

// 维护开关的解析：必须"默认安全"——默认压缩（无损），但默认**不删**数据。
func TestDBMaintenancePrefsFromEnv(t *testing.T) {
	t.Setenv("FREESTOCK_DB_COMPACT", "")
	t.Setenv("FREESTOCK_SCORE_RETENTION_DAYS", "")
	def := dbMaintenancePrefsFromEnv()
	if !def.Compact {
		t.Error("默认应开启压缩（VACUUM 是无损的）")
	}
	if def.RetentionDays != 0 {
		t.Errorf("默认不应清理评分（删除是破坏性的），实际 %d", def.RetentionDays)
	}

	t.Setenv("FREESTOCK_DB_COMPACT", "false")
	if dbMaintenancePrefsFromEnv().Compact {
		t.Error("FREESTOCK_DB_COMPACT=false 应关闭压缩")
	}

	t.Setenv("FREESTOCK_SCORE_RETENTION_DAYS", "1095")
	if got := dbMaintenancePrefsFromEnv().RetentionDays; got != 1095 {
		t.Errorf("保留天数应为 1095，实际 %d", got)
	}

	// 非法值不能变成"删数据"
	t.Setenv("FREESTOCK_SCORE_RETENTION_DAYS", "-5")
	if got := dbMaintenancePrefsFromEnv().RetentionDays; got != 0 {
		t.Errorf("负数应视为不清理，实际 %d", got)
	}
	t.Setenv("FREESTOCK_SCORE_RETENTION_DAYS", "abc")
	if got := dbMaintenancePrefsFromEnv().RetentionDays; got != 0 {
		t.Errorf("非法值应视为不清理，实际 %d", got)
	}
}

// 空洞比例决定"要不要压缩"：算错会让压缩要么白跑、要么永远不触发。
func TestFreeRatio(t *testing.T) {
	cases := []struct {
		stats dbSizeStats
		want  float64
	}{
		{dbSizeStats{Main: 100, Free: 25}, 0.25},
		{dbSizeStats{Main: 100, Free: 0}, 0},
		{dbSizeStats{Main: 0, Free: 10}, 0}, // 除零保护
	}
	for _, tc := range cases {
		if got := tc.stats.freeRatio(); got != tc.want {
			t.Errorf("Main=%d Free=%d 应为 %.2f，实际 %.2f",
				tc.stats.Main, tc.stats.Free, tc.want, got)
		}
	}
}

// 体积文本要能读懂（日志里靠它判断"有没有变小"）。
func TestHumanMB(t *testing.T) {
	if got := humanMB(1 << 20); got != "1.0MB" {
		t.Errorf("1MiB 应显示 1.0MB，实际 %q", got)
	}
}
