// Command freestock-migrate 执行可回滚的组合核心库迁移。
//
// 用法：
//
//	go run ./cmd/freestock-migrate -source data/portfolio.db -target data/portfolio-core.db
//
// 工具不会覆盖目标文件；失败会删除不完整目标，源库保持不变。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"freestock/internal/portfolio"
)

func main() {
	source := flag.String("source", "", "旧组合库路径")
	target := flag.String("target", "", "新核心库路径（必须不存在）")
	flag.Parse()
	if *source == "" || *target == "" {
		flag.Usage()
		os.Exit(2)
	}

	report, err := portfolio.MigrateCoreDatabase(*source, *target)
	if err != nil {
		fmt.Fprintf(os.Stderr, "核心库迁移失败：%v\n", err)
		os.Exit(1)
	}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(encoded))
}
