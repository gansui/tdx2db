package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/workflow"
)

func Cron(ctx context.Context, dbURI string, min bool, gapDays int, downloadDate time.Time) error {
	db, err := database.NewDB(dbURI)
	if err != nil {
		return fmt.Errorf("failed to create database driver: %w", err)
	}

	if err := db.Connect(); err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	if err := db.InitSchema(); err != nil {
		return fmt.Errorf("failed to initialize schema: %w", err)
	}

	if err := checkSchemaVersion(db); err != nil {
		return err
	}

	defer db.Close()

	if err := ctx.Err(); err != nil {
		return err
	}

	today := GetToday()

	plan, err := workflow.BuildWorkPlan(db, today)
	if err != nil {
		return err
	}

	// --date 手动补齐时，补齐后必须全量重算 basic/factor：
	// 否则缺口导致的前一交易日错连会保留（如 09-22 缺 → 09-23 涨幅按 09-21 计算仍旧错误）。
	if !downloadDate.IsZero() {
		plan.NeedDaily = true
		plan.NeedGbbq = true
		plan.NeedBasic = true
		plan.NeedFactor = true
		plan.NeedHolidays = true
		plan.Reason = "🗓️ 手动 --date 补齐指定日期，将触发日线重灌 + basic/factor 全量重算"
	}

	if plan.Reason != "" {
		fmt.Println(plan.Reason)
	}
	// --date 指定了手动补齐日期时，即使常规增量判断无事可做也必须继续
	// （否则补历史缺口会被这里提前 return 挡住）。
	if !plan.AnyNeeded() && downloadDate.IsZero() {
		return nil
	}

	executor := workflow.NewTaskExecutor(db, workflow.GetRegisteredTasks())

	args := &workflow.TaskArgs{
		Min:          min,
		TempDir:      TempDir,
		VipdocDir:    VipdocDir,
		Today:        today,
		Plan:         plan,
		GapDays:      gapDays,
		DownloadDate: downloadDate,
	}

	taskNames := workflow.GetUpdateTaskNames()

	if err := executor.Run(ctx, taskNames, args); err != nil {
		return fmt.Errorf("workflow execution failed: %w", err)
	}

	printLatestKlineDate(db)
	fmt.Println("🚀 今日任务执行成功")
	return nil
}
