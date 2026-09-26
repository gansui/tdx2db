package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jing2uo/tdx2db/database"
	"github.com/jing2uo/tdx2db/utils"
	"github.com/jing2uo/tdx2db/workflow"
)

func GetToday() time.Time {
	return time.Now().Truncate(24 * time.Hour)
}

// TempDir / VipdocDir 默认走 $TMPDIR (Linux 通常 /tmp), 通过
// utils.GetCacheDir 在 package init 时创建唯一子目录。
// 当 $TMPDIR 容量不够 (如 tmpfs 已被占用大半) 时, 用 --temp 在调用方
// 切到磁盘大目录, 见 OverrideTempDir。
var TempDir, _ = utils.GetCacheDir()
var VipdocDir = filepath.Join(TempDir, "vipdoc")

// recentCountWindow 报告逐日条数的最近交易日窗口。
const recentCountWindow = 7

// printLatestKlineDate 打印库中日线最新入库日期，以及最近 7 个交易日的
// 逐日条数，便于一眼识别某天原始数据是否异常偏少（如凑数只有几十条）。
func printLatestKlineDate(db database.DataRepository) {
	row, err := db.GetLatestKlineDate()
	if err != nil {
		fmt.Printf("⚠️ 无法获取日线最新入库日期: %v\n", err)
		return
	}
	if row.Count == 0 || row.Latest.IsZero() {
		fmt.Println("📅 当前库中日线数据为空")
		return
	}
	fmt.Printf("📅 库中日线最新入库日期: %s（%d 条）\n",
		row.Latest.Format("2006-01-02"), row.Count)

	holidays, err := db.GetHolidays()
	if err != nil {
		fmt.Printf("⚠️ 无法获取节假日数据，跳过逐日统计: %v\n", err)
		return
	}
	printRecentDailyCounts(db, holidays, row.Latest)
}

// printRecentDailyCounts 按交易日历从最新日期往前数 recentCountWindow 个交易日，
// 逐日打印库中日线条数；当中某天完全无数据会显示 0。
func printRecentDailyCounts(db database.DataRepository, holidays []time.Time, latest time.Time) {
	printRecentDailyCountsN(db, workflow.NewTradingCalendar(holidays), latest, recentCountWindow)
}

// printRecentDailyCountsN 同 printRecentDailyCounts，可指定回溯的交易日数量 n。
func printRecentDailyCountsN(db database.DataRepository, cal *workflow.TradingCalendar, latest time.Time, n int) {
	if n < 1 {
		n = 1
	}
	days := make([]time.Time, 0, n)
	for d := latest; len(days) < n; d = cal.LastTradingDayOnOrBefore(d.AddDate(0, 0, -1)) {
		days = append(days, d)
	}
	start := days[len(days)-1]

	counts, err := db.GetKlineCountByDate(start)
	if err != nil {
		fmt.Printf("⚠️ 无法获取逐日条数统计: %v\n", err)
		return
	}
	byDate := make(map[string]int64, len(counts))
	for _, c := range counts {
		byDate[c.Date.Format("2006-01-02")] = c.Count
	}

	fmt.Printf("📊 最近 %d 个交易日日线条数（某日明显偏少即原始数据可能不全）：\n", n)
	for i := len(days) - 1; i >= 0; i-- {
		key := days[i].Format("2006-01-02")
		fmt.Printf("   %s   %d 条\n", key, byDate[key])
	}
}

// OverrideTempDir 把默认 TempDir 切到 parent 下的新 mkdtemp 目录,
// 同时更新 VipdocDir, 并清掉 package init 创建的原临时目录。
// 失败时不动现有 TempDir, 调用方可以照常退出。
func OverrideTempDir(parent string) error {
	if parent == "" {
		return nil
	}
	// 绝对化: 不然 mkdtemp 出来的是相对路径, 后面 datatool 走
	// exec.Command(toolPath) + cmd.Dir = cacheDir 时,
	// 子进程先 chdir 到相对 Dir, 再 execve 相对 Path,
	// 两段相对路径叠加 → "no such file or directory"。
	abs, err := filepath.Abs(parent)
	if err != nil {
		return fmt.Errorf("abs temp parent %s: %w", parent, err)
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return fmt.Errorf("create temp parent %s: %w", abs, err)
	}
	dir, err := os.MkdirTemp(abs, "tdx2db-temp-")
	if err != nil {
		return fmt.Errorf("mkdir temp under %s: %w", abs, err)
	}
	oldTemp := TempDir
	TempDir = dir
	VipdocDir = filepath.Join(TempDir, "vipdoc")
	if oldTemp != "" {
		_ = os.RemoveAll(oldTemp)
	}
	return nil
}
