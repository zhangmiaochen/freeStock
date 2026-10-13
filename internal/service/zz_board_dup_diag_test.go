package service

import (
	"os"
	"testing"
)

// **板块成分重复诊断探针**：核对真实数据源里"同一只股票在一个板块里出现几次"。
//
//	FREESTOCK_BOARD_DIAG=1 go test ./internal/service/ -run TestBoardDuplicateProbe -v -count=1
//
// 存在的理由：数据湖的 industry_members / sector_members 是按 as_of_date 分区的
// 每日快照，读取时如果把多份快照当并集，同一个板块里每只股票就会出现 N 次 ——
// 界面上每只股票重复 N 行、成分数放大 N 倍（本机实测融资融券 3877 → 11610），
// 板块成交额/流通市值也被重复累加。这条探针能一眼看出是否回来了。
func TestBoardDuplicateProbe(t *testing.T) {
	if os.Getenv("FREESTOCK_BOARD_DIAG") != "1" {
		t.Skip("板块成分重复诊断：设 FREESTOCK_BOARD_DIAG=1 才跑")
	}
	svc := newLakeService(t)
	if svc == nil {
		t.Skip("本机没有可用的数据湖")
	}

	boards, err := svc.loadBoards()
	if err != nil {
		t.Fatalf("loadBoards: %v", err)
	}

	total, dupBoards, worst := 0, 0, 0
	for _, board := range boards {
		seen := make(map[string]int, len(board.Symbols))
		for _, code := range board.Symbols {
			seen[code]++
		}
		for _, count := range seen {
			if count > 1 {
				dupBoards++
				if count > worst {
					worst = count
				}
				break
			}
		}
		total += len(board.Symbols)
	}

	t.Logf("板块 %d 个，成分行合计 %d，仍有重复成分的板块 %d 个，最多重复 %d 次",
		len(boards), total, dupBoards, worst)
	if dupBoards > 0 {
		t.Errorf("%d 个板块的成分里有重复代码 —— 列表会重复显示、成交额会被重复累加", dupBoards)
	}

	for _, board := range boards {
		if board.Name == "融资融券" {
			t.Logf("融资融券（%s）成分数 = %d", board.Category, len(board.Symbols))
		}
	}
}
