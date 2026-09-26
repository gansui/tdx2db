// 本文件汇集 TDX 相关任务共用的工具: 按日期范围下载并解压 zip 的通用流程。
package workflow

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jing2uo/tdx2db/utils"
)

// pullSource 描述一组按日期下载的 TDX zip 源。
type pullSource struct {
	targetDir   string // 解压目标目录（绝对路径）
	urlTemplate string // 形如 "https://.../%s.zip"
	fileSuffix  string // 文件名末尾标识，例如 "day" / "tic"
	label       string // 日志中文标签，例如 "日线" / "分时"
}

// pullDateRange 从 since 之后到 today 之间逐日下载 zip 并解压。
// 404 状态会结合 args.Plan.Calendar 区分"节假日跳过"/"数据尚未发布"。
// 返回实际成功下载（200）的日期列表，调用方据此决定后续转档/导入。
func pullDateRange(ctx context.Context, since time.Time, src pullSource, args *TaskArgs) ([]time.Time, error) {
	var dates []time.Time
	for d := since.Add(24 * time.Hour); !d.After(args.Today); d = d.Add(24 * time.Hour) {
		dates = append(dates, d)
	}
	if len(dates) == 0 {
		return nil, nil
	}

	if err := os.MkdirAll(src.targetDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create target directory: %w", err)
	}

	fmt.Printf("🐌 开始下载%s数据\n", src.label)

	validDates := make([]time.Time, 0, len(dates))

	for _, date := range dates {
		ok, err := pullOneDate(ctx, date, src, args)
		if err != nil {
			return validDates, err
		}
		if ok {
			validDates = append(validDates, date)
		}
	}

	return validDates, nil
}

// pullSingleDate 只下载并解压指定单一交易日的 zip。
// 成功返回该日期，失败/未发布/非交易日返回零值（原因已打印）。
func pullSingleDate(ctx context.Context, date time.Time, src pullSource, args *TaskArgs) (time.Time, error) {
	if err := os.MkdirAll(src.targetDir, 0755); err != nil {
		return time.Time{}, fmt.Errorf("failed to create target directory: %w", err)
	}

	fmt.Printf("🐌 开始下载%s数据\n", src.label)

	ok, err := pullOneDate(ctx, date, src, args)
	if err != nil {
		return time.Time{}, err
	}
	if !ok {
		return time.Time{}, nil
	}
	return date, nil
}

// pullOneDate 下载并解压单个日期的 zip，返回是否成功（200 + 解压成功）。
func pullOneDate(ctx context.Context, date time.Time, src pullSource, args *TaskArgs) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	dateStr := date.Format("20060102")
	url := fmt.Sprintf(src.urlTemplate, dateStr)
	fileName := fmt.Sprintf("%s%s.zip", dateStr, src.fileSuffix)
	filePath := filepath.Join(src.targetDir, fileName)

	status, err := utils.DownloadFile(url, filePath)
	switch status {
	case 200:
		fmt.Printf("✅ 已下载 %s 的数据\n", dateStr)
		if err := utils.UnzipFile(filePath, src.targetDir); err != nil {
			fmt.Printf("⚠️ 解压文件 %s 失败: %v\n", filePath, err)
			return false, nil
		}
		return true, nil
	case 404:
		var cal *TradingCalendar
		if args.Plan != nil {
			cal = args.Plan.Calendar
		}
		switch {
		case cal != nil && cal.IsHoliday(date):
			fmt.Printf("🎉 %s 为节假日，跳过\n", dateStr)
		case cal != nil && cal.IsWeekend(date):
			fmt.Printf("🌴 %s 为周末，跳过\n", dateStr)
		case date.Equal(args.Today):
			fmt.Printf("⏳ %s 数据尚未发布，请等待收盘后重试\n", dateStr)
		default:
			fmt.Printf("🟡 %s 数据尚未发布\n", dateStr)
		}
		return false, nil
	default:
		if err != nil {
			return false, fmt.Errorf("download failed: %w", err)
		}
	}

	return false, nil
}
