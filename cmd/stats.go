package cmd

import (
	"context"
	"fmt"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/workflow"
)

// Stats 统计最近 days 个交易日逐日入库条数，用于一眼识别某日原始数据缺失；
// 纯只读查询，不下载任何数据。
func Stats(ctx context.Context, dbURI string, days int) error {
	db, err := database.NewDB(dbURI)
	if err != nil {
		return fmt.Errorf("failed to create database driver: %w", err)
	}
	if err := db.Connect(); err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}
	defer db.Close()

	if err := db.InitSchema(); err != nil {
		return fmt.Errorf("failed to initialize schema: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	row, err := db.GetLatestKlineDate()
	if err != nil {
		return fmt.Errorf("failed to query latest kline date: %w", err)
	}
	if row.Count == 0 || row.Latest.IsZero() {
		fmt.Println("📅 当前库中日线数据为空")
		return nil
	}
	fmt.Printf("📅 库中日线最新入库日期: %s（%d 条）\n",
		row.Latest.Format("2006-01-02"), row.Count)

	holidays, err := db.GetHolidays()
	if err != nil {
		return fmt.Errorf("failed to load holidays: %w", err)
	}
	printRecentDailyCountsN(db, workflow.NewTradingCalendar(holidays), row.Latest, days)
	return nil
}
